package clickhouse

// clickhouse.go — the driver: catalog reads, row browsing and the dialect.
//
// Every catalog query reads system.* tables, which is the engine's own
// metadata rather than an information_schema shim: ClickHouse's
// information_schema exists but omits the engine-specific columns (skip
// indexes, the sorting key, the engine name) that make the tree useful.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/Ben-guanzhi/dbcore"
)

// ClickHouseDriver is the database/sql-backed driver.
type ClickHouseDriver struct {
	db    *sql.DB
	owned bool // Close closes db (false when the caller supplied one)
}

// Kind implements dbcore.Driver.
func (d *ClickHouseDriver) Kind() dbcore.DatabaseKind { return dbcore.KindClickHouse }

// AsSQL reports the SQL surface.
func (d *ClickHouseDriver) AsSQL() (dbcore.SQLDriver, bool) { return d, true }

// AsKV reports that this engine has no key/value surface.
func (d *ClickHouseDriver) AsKV() (dbcore.KVDriver, bool) { return nil, false }

// Ping checks the connection.
func (d *ClickHouseDriver) Ping(ctx context.Context) error { return d.db.PingContext(ctx) }

// Close releases the pool this driver opened.
func (d *ClickHouseDriver) Close() error {
	if d.db == nil || !d.owned {
		return nil
	}
	return d.db.Close()
}

// Execute runs one statement. ClickHouse answers a SELECT with rows and a
// DDL/DML statement with no result set; the affected-row count is only
// available for mutations, so it is reported as 0 when the engine gives none.
func (d *ClickHouseDriver) Execute(ctx context.Context, query string, args ...dbcore.Value) (*dbcore.QueryResult, error) {
	// The statement kind decides the path BEFORE any call is made. ClickHouse's
	// driver answers a DDL statement through QueryContext with an empty result
	// set and no error, so "try query, fall back to exec" would report success
	// for a statement that never ran — a DROP that silently did nothing.
	if looksLikeCommand(query) {
		anyArgs := make([]any, len(args))
		for i, a := range args {
			anyArgs[i] = nativeArg(a)
		}
		execRes, err := d.db.ExecContext(ctx, query, anyArgs...)
		if err != nil {
			return nil, err
		}
		n, _ := execRes.RowsAffected()
		return &dbcore.QueryResult{AffectedRows: n}, nil
	}
	return dbcore.RunQuery(ctx, d.db, query, args)
}

// nativeArg converts a kernel Value into a database/sql bind argument.
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

// looksLikeCommand reports whether the statement is one that returns no rows.
// The check is deliberately a prefix test on the first keyword: the only thing
// it decides is whether to retry through Exec after Query failed.
func looksLikeCommand(query string) bool {
	for _, w := range strings.Fields(strings.ToUpper(query)) {
		switch w {
		case "SELECT", "WITH", "SHOW", "DESCRIBE", "DESC", "EXPLAIN", "EXISTS":
			return false
		case "INSERT", "CREATE", "ALTER", "DROP", "TRUNCATE", "OPTIMIZE",
			"RENAME", "ATTACH", "DETACH", "SET", "SYSTEM", "KILL", "GRANT", "REVOKE":
			return true
		default:
			return false
		}
	}
	return false
}

// ListSchemas lists the server's databases. ClickHouse has no schema layer
// inside a database, so its database IS the schema a client browses.
func (d *ClickHouseDriver) ListSchemas(ctx context.Context) ([]dbcore.SchemaInfo, error) {
	res, err := dbcore.RunQuery(ctx, d.db,
		`SELECT name FROM system.databases ORDER BY name`, nil)
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.SchemaInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		out = append(out, dbcore.SchemaInfo{Name: r[0].S})
	}
	return out, nil
}

// ListTables lists a database's tables and views, with the row estimate the
// server keeps in system.tables (0 means "never counted", which is common on
// a fresh table, so it is reported as -1 = unknown rather than as an empty
// table).
func (d *ClickHouseDriver) ListTables(ctx context.Context, schema string) ([]dbcore.TableInfo, error) {
	res, err := dbcore.RunQuery(ctx, d.db,
		`SELECT name, engine, COALESCE(total_rows, 0), COALESCE(comment, '')
			FROM system.tables
			WHERE database = ? AND is_temporary = 0
			ORDER BY name`, dbcore.ValuesOf([]interface{}{schema}))
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.TableInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		t := dbcore.TableInfo{Schema: schema, Name: r[0].S, Type: "table", Rows: -1, Comment: r[3].S}
		// ClickHouse spells views as the table ENGINE (View/MaterializedView);
		// there is no separate object kind in system.tables.
		if strings.Contains(strings.ToLower(r[1].S), "view") {
			t.Type = "view"
		}
		if n, ok := dbcore.IntFrom(r[2]); ok && n > 0 {
			t.Rows = n
			// total_rows is an estimate from the background merge counter.
			t.ApproxRows = true
		}
		out = append(out, t)
	}
	return out, nil
}

// ListColumns lists one table's columns. The declared type is reported as the
// engine spells it (Nullable(Int64), LowCardinality(String)): translating
// ClickHouse's type system into SQL names would lose the wrapper information
// the type's whole behaviour depends on.
func (d *ClickHouseDriver) ListColumns(ctx context.Context, schema, table string) ([]dbcore.ColumnInfo, error) {
	res, err := dbcore.RunQuery(ctx, d.db,
		`SELECT name, type, COALESCE(comment, '')
			FROM system.columns
			WHERE database = ? AND table = ?
			ORDER BY position`, dbcore.ValuesOf([]interface{}{schema, table}))
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.ColumnInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		out = append(out, dbcore.ColumnInfo{
			Name: r[0].S,
			Type: r[1].S,
			// A Nullable(...) wrapper is the only way ClickHouse allows NULL,
			// so nullability is read out of the type text.
			Nullable: strings.HasPrefix(r[1].S, "Nullable("),
			Comment:  r[2].S,
			Table:    table,
		})
	}
	return out, nil
}

// ListIndexes reports the table's ROW KEY columns as one index entry.
//
// This is the honest mapping, not a shortcut: ClickHouse has no B-tree
// indexes. The primary key is a sorting key — the physical order rows are
// stored in — so it decides range-scan efficiency exactly the way an index
// does on an OLTP engine. Declared skip indexes are reported separately as
// secondary entries, because they answer a different question (skip granules
// that cannot match a predicate).
func (d *ClickHouseDriver) ListIndexes(ctx context.Context, schema, table string) ([]dbcore.IndexInfo, error) {
	var out []dbcore.IndexInfo
	res, err := dbcore.RunQuery(ctx, d.db,
		`SELECT primary_key, sorting_key FROM system.tables
			WHERE database = ? AND name = ?`, dbcore.ValuesOf([]interface{}{schema, table}))
	if err != nil {
		return nil, err
	}
	if len(res.Rows) > 0 {
		// The keys are comma-separated expression lists, not identifiers:
		// split on commas outside parentheses and keep each expression whole.
		if cols := splitKeyExprs(res.Rows[0][0].S); len(cols) > 0 {
			out = append(out, dbcore.IndexInfo{Name: "PRIMARY KEY", Columns: cols, Primary: true})
		}
		sortKey := res.Rows[0][1].S
		if sortKey != "" && sortKey != res.Rows[0][0].S {
			if cols := splitKeyExprs(sortKey); len(cols) > 0 {
				out = append(out, dbcore.IndexInfo{Name: "ORDER BY", Columns: cols})
			}
		}
	}
	// The skip indexes (data skipping indices) are the secondary structures.
	sres, err := dbcore.RunQuery(ctx, d.db,
		`SELECT name, expr FROM system.data_skipping_indices
			WHERE database = ? AND table = ? ORDER BY name`, dbcore.ValuesOf([]interface{}{schema, table}))
	if err == nil {
		for _, r := range sres.Rows {
			out = append(out, dbcore.IndexInfo{
				Name:    r[0].S,
				Columns: splitKeyExprs(r[1].S),
			})
		}
	}
	return out, nil
}

// splitKeyExprs splits a comma-separated key expression list, keeping
// parenthesised groups together (a key may contain tuple(a, b) or f(x, y)).
func splitKeyExprs(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	depth := 0
	cur := strings.Builder{}
	for _, r := range s {
		switch r {
		case '(':
			depth++
			cur.WriteRune(r)
		case ')':
			depth--
			cur.WriteRune(r)
		case ',':
			if depth == 0 {
				if v := strings.TrimSpace(cur.String()); v != "" {
					out = append(out, v)
				}
				cur.Reset()
				continue
			}
			cur.WriteRune(r)
		default:
			cur.WriteRune(r)
		}
	}
	if v := strings.TrimSpace(cur.String()); v != "" {
		out = append(out, v)
	}
	return out
}

// ListForeignKeys answers that ClickHouse has no foreign keys. The concept
// does not exist: joins are written per query and nothing is enforced.
func (d *ClickHouseDriver) ListForeignKeys(ctx context.Context, schema, table string) ([]dbcore.ForeignKeyInfo, error) {
	return nil, &dbcore.Error{Code: dbcore.ErrCodeNotSupported,
		Message: "clickhouse: the engine has no foreign keys"}
}

// CreateTableSQL renders a table's CREATE statement from the catalog, because
// ClickHouse's own SHOW CREATE TABLE is not reachable through the driver's
// query path for every engine (the statement is stored, not computed).
func (d *ClickHouseDriver) CreateTableSQL(ctx context.Context, schema, table string) (string, error) {
	res, err := dbcore.RunQuery(ctx, d.db,
		`SELECT name, type, COALESCE(default_expression, '')
			FROM system.columns WHERE database = ? AND table = ?
			ORDER BY position`, dbcore.ValuesOf([]interface{}{schema, table}))
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 {
		return "", fmt.Errorf("clickhouse: table %q not found", table)
	}
	tres, err := dbcore.RunQuery(ctx, d.db,
		`SELECT engine_full, COALESCE(comment,'') FROM system.tables
			WHERE database = ? AND name = ?`, dbcore.ValuesOf([]interface{}{schema, table}))
	if err != nil {
		return "", err
	}
	var defs []string
	for _, r := range res.Rows {
		def := "  " + dbcore.QuoteDoubleIdent(r[0].S) + " " + r[1].S
		if r[2].S != "" {
			def += " DEFAULT " + r[2].S
		}
		defs = append(defs, def)
	}
	engine := "MergeTree"
	var comment string
	if len(tres.Rows) > 0 {
		if e := strings.TrimSpace(tres.Rows[0][0].S); e != "" {
			engine = e
		}
		comment = tres.Rows[0][1].S
	}
	stmt := "CREATE TABLE " + dbcore.QuoteDoubleIdent(schema) + "." + dbcore.QuoteDoubleIdent(table) +
		" (\n" + strings.Join(defs, ",\n") + "\n) ENGINE = " + engine
	if comment != "" {
		stmt += " COMMENT " + quoteLiteral(comment)
	}
	return stmt + ";", nil
}

// quoteLiteral renders a ClickHouse string literal: single quotes doubled,
// backslashes escaped (ClickHouse honours backslash escapes by default).
func quoteLiteral(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `''`)
	return "'" + r.Replace(s) + "'"
}

// BrowseRows reads one page of a table.
//
// ClickHouse cannot LIMIT ... OFFSET efficiently on a large table, but it is
// the only portable paging the kernel's request expresses without a keyset
// column the engine may not have; a keyset request is honoured when the
// caller supplies one.
func (d *ClickHouseDriver) BrowseRows(ctx context.Context, schema, table string, req *dbcore.TableRowsRequest) (*dbcore.TableRowsResult, error) {
	if req == nil {
		req = &dbcore.TableRowsRequest{Page: 1, PageSize: 200}
	}
	dial := ClickHouseDialect{}
	query, args := dial.BuildRowsQuery(schema, table, req)
	res, err := dbcore.RunQuery(ctx, d.db, query, args)
	if err != nil {
		return nil, err
	}
	out := &dbcore.TableRowsResult{Columns: res.Columns, Rows: res.Rows}
	if !req.NoCount {
		cq, cargs := dial.BuildCountQuery(schema, table, req)
		cres, cerr := dbcore.RunQuery(ctx, d.db, cq, cargs)
		if cerr == nil && len(cres.Rows) > 0 {
			if n, ok := dbcore.IntFrom(cres.Rows[0][0]); ok {
				out.Total = uint64(n)
				out.TotalFound = true
			}
		}
	}
	return out, nil
}

// BrowseRowsCount counts the rows a request would return.
func (d *ClickHouseDriver) BrowseRowsCount(ctx context.Context, schema, table string, req *dbcore.TableRowsRequest) (uint64, error) {
	dial := ClickHouseDialect{}
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
