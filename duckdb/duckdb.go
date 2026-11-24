package duckdb

// duckdb.go — the driver: catalog reads, foreign keys, indexes and browsing.
//
// DuckDB exposes an information_schema close to PostgreSQL's, which is what
// the catalog reads use. It is a real relational engine, so unlike ClickHouse
// it answers for primary keys, foreign keys and indexes rather than refusing.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/Ben-guanzhi/dbcore"
)

// DuckDBDriver is the database/sql-backed driver.
type DuckDBDriver struct {
	db    *sql.DB
	owned bool
}

// Kind implements dbcore.Driver.
func (d *DuckDBDriver) Kind() dbcore.DatabaseKind { return dbcore.KindDuckDB }

// AsSQL reports the SQL surface.
func (d *DuckDBDriver) AsSQL() (dbcore.SQLDriver, bool) { return d, true }

// AsKV reports no key/value surface.
func (d *DuckDBDriver) AsKV() (dbcore.KVDriver, bool) { return nil, false }

// Ping checks the database.
func (d *DuckDBDriver) Ping(ctx context.Context) error { return d.db.PingContext(ctx) }

// Close releases the pool.
func (d *DuckDBDriver) Close() error {
	if d.db == nil || !d.owned {
		return nil
	}
	return d.db.Close()
}

// Execute runs one statement. DuckDB's driver returns rows for a SELECT and
// none for DDL, and it does report an error for a bad statement, so the path
// is chosen from the statement — the lesson ClickHouse taught applies to any
// engine whose driver is lenient about DDL.
func (d *DuckDBDriver) Execute(ctx context.Context, query string, args ...dbcore.Value) (*dbcore.QueryResult, error) {
	if looksLikeCommand(query) {
		anyArgs := make([]any, len(args))
		for i, a := range args {
			anyArgs[i] = nativeArg(a)
		}
		res, err := d.db.ExecContext(ctx, query, anyArgs...)
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		return &dbcore.QueryResult{AffectedRows: n}, nil
	}
	return dbcore.RunQuery(ctx, d.db, query, args)
}

// nativeArg converts a kernel Value into a bind argument.
func nativeArg(v dbcore.Value) any {
	switch v.Kind {
	case dbcore.ValNull:
		return nil
	case dbcore.ValInt:
		return v.I
	case dbcore.ValFloat:
		return v.F
	case dbcore.ValBool:
		return v.Bool
	case dbcore.ValBlob:
		return v.B
	case dbcore.ValTime:
		return v.T
	default:
		return v.S
	}
}

// looksLikeCommand reports whether the statement returns no rows.
func looksLikeCommand(query string) bool {
	for _, w := range strings.Fields(strings.ToUpper(query)) {
		switch w {
		case "SELECT", "WITH", "SHOW", "DESCRIBE", "DESC", "EXPLAIN", "PRAGMA", "TABLE", "VALUES", "SUMMARIZE":
			return false
		case "INSERT", "UPDATE", "DELETE", "CREATE", "ALTER", "DROP",
			"TRUNCATE", "COPY", "ATTACH", "DETACH", "INSTALL", "LOAD",
			"EXPORT", "IMPORT", "CHECKPOINT", "VACUUM", "COMMENT", "SET", "RESET":
			return true
		default:
			return false
		}
	}
	return false
}

// ListSchemas lists the database's schemas. DuckDB keeps a real schema layer
// (main, plus whatever a user creates), so this is the same shape as
// PostgreSQL's.
func (d *DuckDBDriver) ListSchemas(ctx context.Context) ([]dbcore.SchemaInfo, error) {
	res, err := dbcore.RunQuery(ctx, d.db,
		`SELECT schema_name FROM information_schema.schemata
			WHERE schema_name NOT IN ('information_schema','pg_catalog')
			ORDER BY schema_name`, nil)
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.SchemaInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		out = append(out, dbcore.SchemaInfo{Name: r[0].S})
	}
	return out, nil
}

// ListTables lists a schema's tables and views.
func (d *DuckDBDriver) ListTables(ctx context.Context, schema string) ([]dbcore.TableInfo, error) {
	res, err := dbcore.RunQuery(ctx, d.db,
		`SELECT table_name, table_type FROM information_schema.tables
			WHERE table_schema = ? ORDER BY table_name`,
		dbcore.ValuesOf([]interface{}{schema}))
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.TableInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		t := dbcore.TableInfo{Schema: schema, Name: r[0].S, Type: "table", Rows: -1}
		if len(r) > 1 && strings.Contains(strings.ToUpper(r[1].S), "VIEW") {
			t.Type = "view"
		}
		out = append(out, t)
	}
	return out, nil
}

// ListColumns lists one table's columns, with the declared type verbatim
// (DuckDB keeps its own type names: BIGINT, VARCHAR, STRUCT(...), LIST).
func (d *DuckDBDriver) ListColumns(ctx context.Context, schema, table string) ([]dbcore.ColumnInfo, error) {
	res, err := dbcore.RunQuery(ctx, d.db,
		`SELECT column_name, data_type, is_nullable, column_default
			FROM information_schema.columns
			WHERE table_schema = ? AND table_name = ?
			ORDER BY ordinal_position`,
		dbcore.ValuesOf([]interface{}{schema, table}))
	if err != nil {
		return nil, err
	}
	// The primary-key columns come from a second read: information_schema in
	// DuckDB does not flag them on the column row.
	pks := d.primaryKeyColumns(ctx, schema, table)
	out := make([]dbcore.ColumnInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		c := dbcore.ColumnInfo{Name: r[0].S, Type: r[1].S, Table: table}
		if len(r) > 2 {
			c.Nullable = strings.EqualFold(r[2].S, "YES")
		}
		if len(r) > 3 && r[3].S != "" {
			def := r[3].S
			c.Default = &def
		}
		c.IsPrimaryKey = pks[strings.ToLower(c.Name)]
		out = append(out, c)
	}
	return out, nil
}

// primaryKeyColumns returns the table's primary-key column names, lower-cased
// for comparison.
func (d *DuckDBDriver) primaryKeyColumns(ctx context.Context, schema, table string) map[string]bool {
	out := map[string]bool{}
	res, err := dbcore.RunQuery(ctx, d.db,
		`SELECT constraint_column_names
			FROM duckdb_constraints()
			WHERE schema_name = ? AND table_name = ? AND constraint_type = 'PRIMARY KEY'`,
		dbcore.ValuesOf([]interface{}{schema, table}))
	if err != nil {
		return out
	}
	for _, r := range res.Rows {
		// The column list comes back as DuckDB's list text "[id, name]".
		for _, col := range listTextOf(r[0].S) {
			out[strings.ToLower(col)] = true
		}
	}
	return out
}

// listTextOf parses DuckDB's list rendering ("[a, b]" or "[]") into its
// elements. Names with commas are not a concern here: column names are quoted
// when they can contain one, and a key list is built from identifiers.
func listTextOf(s string) []string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		if s == "" {
			return nil
		}
		return []string{s}
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return nil
	}
	parts := strings.Split(inner, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.Trim(strings.TrimSpace(p), `"`); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// ListIndexes reports the table's indexes. DuckDB keeps real (ART) indexes,
// so this answers from duckdb_indexes() rather than refusing.
func (d *DuckDBDriver) ListIndexes(ctx context.Context, schema, table string) ([]dbcore.IndexInfo, error) {
	res, err := dbcore.RunQuery(ctx, d.db,
		`SELECT index_name, is_unique, sql FROM duckdb_indexes()
			WHERE schema_name = ? AND table_name = ? ORDER BY index_name`,
		dbcore.ValuesOf([]interface{}{schema, table}))
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.IndexInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		ix := dbcore.IndexInfo{Name: r[0].S}
		if len(r) > 1 {
			ix.Unique = strings.EqualFold(r[1].S, "true") || r[1].I == 1
		}
		// The CREATE INDEX text is the only place the columns appear, so
		// they are lifted out of it rather than guessed.
		if len(r) > 2 {
			ix.Columns = indexColumnsOf(r[2].S)
		}
		out = append(out, ix)
	}
	return out, nil
}

// indexColumnsOf lifts the column list out of a CREATE INDEX statement.
func indexColumnsOf(sql string) []string {
	open := strings.Index(sql, "(")
	closeIdx := strings.LastIndex(sql, ")")
	if open < 0 || closeIdx <= open {
		return nil
	}
	inner := sql[open+1 : closeIdx]
	parts := strings.Split(inner, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		v := strings.TrimSpace(p)
		v = strings.Trim(v, `"`)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// ListForeignKeys reports the table's foreign keys. DuckDB enforces them, so
// this is a real answer rather than a refusal.
//
// duckdb_constraints() is used instead of information_schema: the latter's
// constraint_column_usage view reports the CHILD table on a live DuckDB, so
// joining it for the referenced table silently produced a self-reference.
// The DuckDB-specific table returns both ends of the edge explicitly.
func (d *DuckDBDriver) ListForeignKeys(ctx context.Context, schema, table string) ([]dbcore.ForeignKeyInfo, error) {
	res, err := dbcore.RunQuery(ctx, d.db,
		`SELECT constraint_name, constraint_column_names,
				referenced_table, referenced_column_names
			FROM duckdb_constraints()
			WHERE schema_name = ? AND table_name = ? AND constraint_type = 'FOREIGN KEY'
			ORDER BY constraint_name`,
		dbcore.ValuesOf([]interface{}{schema, table}))
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.ForeignKeyInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		out = append(out, dbcore.ForeignKeyInfo{
			Name:       r[0].S,
			Columns:    listTextOf(r[1].S),
			RefTable:   r[2].S,
			RefColumns: listTextOf(r[3].S),
		})
	}
	return out, nil
}

// CreateTableSQL renders a table's CREATE from the catalog.
func (d *DuckDBDriver) CreateTableSQL(ctx context.Context, schema, table string) (string, error) {
	res, err := dbcore.RunQuery(ctx, d.db,
		`SELECT column_name, data_type, is_nullable, column_default
			FROM information_schema.columns
			WHERE table_schema = ? AND table_name = ?
			ORDER BY ordinal_position`,
		dbcore.ValuesOf([]interface{}{schema, table}))
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 {
		return "", fmt.Errorf("duckdb: table %q not found", table)
	}
	pks := d.primaryKeyColumns(ctx, schema, table)
	var defs []string
	var keyCols []string
	for _, r := range res.Rows {
		def := "  " + dbcore.QuoteDoubleIdent(r[0].S) + " " + r[1].S
		if len(r) > 3 && r[3].S != "" {
			def += " DEFAULT " + r[3].S
		}
		if len(r) > 2 && !strings.EqualFold(r[2].S, "YES") {
			def += " NOT NULL"
		}
		defs = append(defs, def)
		if pks[strings.ToLower(r[0].S)] {
			keyCols = append(keyCols, dbcore.QuoteDoubleIdent(r[0].S))
		}
	}
	if len(keyCols) > 0 {
		defs = append(defs, "  PRIMARY KEY ("+strings.Join(keyCols, ", ")+")")
	}
	return "CREATE TABLE " + dbcore.QuoteDoubleIdent(schema) + "." + dbcore.QuoteDoubleIdent(table) +
		" (\n" + strings.Join(defs, ",\n") + "\n);", nil
}

// BrowseRows reads one page.
func (d *DuckDBDriver) BrowseRows(ctx context.Context, schema, table string, req *dbcore.TableRowsRequest) (*dbcore.TableRowsResult, error) {
	if req == nil {
		req = &dbcore.TableRowsRequest{Page: 1, PageSize: 200}
	}
	dial := DuckDBDialect{}
	query, args := dial.BuildRowsQuery(schema, table, req)
	res, err := dbcore.RunQuery(ctx, d.db, query, args)
	if err != nil {
		return nil, err
	}
	out := &dbcore.TableRowsResult{Columns: res.Columns, Rows: res.Rows}
	if !req.NoCount {
		cq, cargs := dial.BuildCountQuery(schema, table, req)
		if cres, cerr := dbcore.RunQuery(ctx, d.db, cq, cargs); cerr == nil && len(cres.Rows) > 0 {
			if n, ok := dbcore.IntFrom(cres.Rows[0][0]); ok {
				out.Total = uint64(n)
				out.TotalFound = true
			}
		}
	}
	return out, nil
}

// BrowseRowsCount counts the rows a request would return.
func (d *DuckDBDriver) BrowseRowsCount(ctx context.Context, schema, table string, req *dbcore.TableRowsRequest) (uint64, error) {
	dial := DuckDBDialect{}
	query, args := dial.BuildCountQuery(schema, table, req)
	res, err := dbcore.RunQuery(ctx, d.db, query, args)
	if err != nil {
		return 0, err
	}
	if len(res.Rows) == 0 {
		return 0, nil
	}
	n, _ := dbcore.IntFrom(res.Rows[0][0])
	return uint64(n), nil
}
