package trino

// trino.go — the driver. The catalog model is the whole story: see the
// package comment for why "schema" means CATALOG here.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/Ben-guanzhi/dbcore"
)

// TrinoDriver is the database/sql-backed driver.
type TrinoDriver struct {
	db    *sql.DB
	owned bool
}

// Kind implements dbcore.Driver.
func (d *TrinoDriver) Kind() dbcore.DatabaseKind { return dbcore.KindTrino }

// AsSQL reports the SQL surface.
func (d *TrinoDriver) AsSQL() (dbcore.SQLDriver, bool) { return d, true }

// AsKV reports no key/value surface.
func (d *TrinoDriver) AsKV() (dbcore.KVDriver, bool) { return nil, false }

// Ping checks the connection.
func (d *TrinoDriver) Ping(ctx context.Context) error { return d.db.PingContext(ctx) }

// Close releases the pool this driver opened.
func (d *TrinoDriver) Close() error {
	if d.db == nil || !d.owned {
		return nil
	}
	return d.db.Close()
}

// Execute runs one statement.
//
// Trino's client answers some statements through Query with no rows and no
// error, so the path is chosen from the STATEMENT rather than from a failure —
// the same lesson ClickHouse taught: a DROP that "succeeded" without running
// is the worst kind of bug.
func (d *TrinoDriver) Execute(ctx context.Context, query string, args ...dbcore.Value) (*dbcore.QueryResult, error) {
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
		case "SELECT", "WITH", "SHOW", "DESCRIBE", "DESC", "EXPLAIN", "TABLE", "VALUES":
			return false
		case "INSERT", "UPDATE", "DELETE", "MERGE", "CREATE", "ALTER", "DROP",
			"TRUNCATE", "COMMENT", "GRANT", "REVOKE", "SET", "RESET", "USE",
			"CALL", "PREPARE", "DEALLOCATE", "ANALYZE":
			return true
		default:
			return false
		}
	}
	return false
}

// ListSchemas lists the CATALOGS.
//
// Trino's own vocabulary is inverted from a single-database engine: what the
// kernel calls a schema is a catalog here, and what it calls a table is
// catalog.schema.table. Listing catalogs at this level is the only answer that
// lets a client browse: a list of every catalog's schemas flattened together
// would lose which catalog each one belongs to.
func (d *TrinoDriver) ListSchemas(ctx context.Context) ([]dbcore.SchemaInfo, error) {
	res, err := dbcore.RunQuery(ctx, d.db, `SHOW CATALOGS`, nil)
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.SchemaInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		if len(r) > 0 && r[0].S != "" {
			out = append(out, dbcore.SchemaInfo{Name: r[0].S})
		}
	}
	return out, nil
}

// ListTables lists what a catalog (or one schema inside it) holds.
//
// The argument is "catalog" or "catalog.schema". Without a schema the answer
// spans every schema of the catalog, and each table carries its schema in
// TableInfo.Schema — a client that flattened that away could not address the
// table again.
func (d *TrinoDriver) ListTables(ctx context.Context, spec string) ([]dbcore.TableInfo, error) {
	catalog, schema, hasSchema := splitCatalogSchema(spec)
	if catalog == "" {
		return nil, fmt.Errorf("trino: 需要一个 catalog 名（catalog 或 catalog.schema）")
	}
	if !hasSchema {
		schemas, err := d.catalogSchemas(ctx, catalog)
		if err != nil {
			return nil, err
		}
		var out []dbcore.TableInfo
		for _, s := range schemas {
			tables, err := d.tablesIn(ctx, catalog, s)
			if err != nil {
				// One broken schema must not hide the whole catalog: a
				// connector may be down while the others answer.
				continue
			}
			out = append(out, tables...)
		}
		return out, nil
	}
	return d.tablesIn(ctx, catalog, schema)
}

// catalogSchemas lists one catalog's schemas.
func (d *TrinoDriver) catalogSchemas(ctx context.Context, catalog string) ([]string, error) {
	res, err := dbcore.RunQuery(ctx, d.db,
		`SELECT schema_name FROM `+dbcore.QuoteDoubleIdent(catalog)+`.information_schema.schemata ORDER BY 1`, nil)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(res.Rows))
	for _, r := range res.Rows {
		if len(r) > 0 {
			out = append(out, r[0].S)
		}
	}
	return out, nil
}

// tablesIn lists one catalog.schema's tables and views.
func (d *TrinoDriver) tablesIn(ctx context.Context, catalog, schema string) ([]dbcore.TableInfo, error) {
	res, err := dbcore.RunQuery(ctx, d.db,
		`SELECT table_name, table_type FROM `+dbcore.QuoteDoubleIdent(catalog)+
			`.information_schema.tables WHERE table_schema = ? ORDER BY table_name`,
		dbcore.ValuesOf([]interface{}{schema}))
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.TableInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		t := dbcore.TableInfo{Schema: schema, Name: r[0].S, Type: "table", Rows: -1}
		// table_type is BASE TABLE / VIEW.
		if len(r) > 1 && strings.Contains(strings.ToUpper(r[1].S), "VIEW") {
			t.Type = "view"
		}
		out = append(out, t)
	}
	return out, nil
}

// splitCatalogSchema splits "catalog" or "catalog.schema".
func splitCatalogSchema(spec string) (catalog, schema string, hasSchema bool) {
	spec = strings.TrimSpace(spec)
	if i := strings.Index(spec, "."); i > 0 {
		return spec[:i], spec[i+1:], true
	}
	return spec, "", false
}

// tableRef renders a fully qualified table name, resolving a bare "catalog"
// spec to the connection's own schema when it has one.
func tableRef(spec, table string) string {
	catalog, schema, hasSchema := splitCatalogSchema(spec)
	if !hasSchema || schema == "" {
		// A catalog with no schema cannot address a table; the caller's own
		// connection schema is the only sensible default and the engine will
		// reject it if it is wrong.
		return dbcore.QuoteDoubleIdent(catalog) + "." + dbcore.QuoteDoubleIdent(table)
	}
	return dbcore.QuoteDoubleIdent(catalog) + "." + dbcore.QuoteDoubleIdent(schema) + "." + dbcore.QuoteDoubleIdent(table)
}

// ListColumns lists one table's columns. A column's type is Trino's own
// spelling (varchar(50), array(bigint), row(a bigint, b varchar)) and is
// reported verbatim: the type IS the value's behaviour.
func (d *TrinoDriver) ListColumns(ctx context.Context, spec, table string) ([]dbcore.ColumnInfo, error) {
	catalog, schema, _ := splitCatalogSchema(spec)
	q := `SELECT column_name, data_type, COALESCE(is_nullable, ''), COALESCE(column_default, ''), COALESCE(comment, '')
		FROM ` + dbcore.QuoteDoubleIdent(catalog) + `.information_schema.columns
		WHERE table_name = ?`
	args := []interface{}{table}
	if schema != "" {
		q += ` AND table_schema = ?`
		args = append(args, schema)
	}
	q += ` ORDER BY ordinal_position`
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(args))
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.ColumnInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		c := dbcore.ColumnInfo{Name: r[0].S, Type: r[1].S, Table: table}
		if len(r) > 2 {
			// is_nullable is the standard 'YES'/'NO'.
			c.Nullable = strings.EqualFold(r[2].S, "YES")
		}
		if len(r) > 3 && r[3].S != "" {
			def := r[3].S
			c.Default = &def
		}
		if len(r) > 4 {
			c.Comment = r[4].S
		}
		out = append(out, c)
	}
	return out, nil
}

// ListIndexes answers that Trino has no indexes: a table's storage is whatever
// the connector's underlying system does, and Trino does not model it.
func (d *TrinoDriver) ListIndexes(ctx context.Context, spec, table string) ([]dbcore.IndexInfo, error) {
	return nil, &dbcore.Error{Code: dbcore.ErrCodeNotSupported,
		Message: "trino: 引擎不建模索引（存储由各 connector 决定）"}
}

// ListForeignKeys answers that Trino has no foreign keys.
func (d *TrinoDriver) ListForeignKeys(ctx context.Context, spec, table string) ([]dbcore.ForeignKeyInfo, error) {
	return nil, &dbcore.Error{Code: dbcore.ErrCodeNotSupported,
		Message: "trino: 引擎不建模外键"}
}

// CreateTableSQL renders a table's CREATE from information_schema. Trino's
// SHOW CREATE TABLE exists but is per-connector text, so the catalog form is
// the portable one.
func (d *TrinoDriver) CreateTableSQL(ctx context.Context, spec, table string) (string, error) {
	cols, err := d.ListColumns(ctx, spec, table)
	if err != nil {
		return "", err
	}
	if len(cols) == 0 {
		return "", fmt.Errorf("trino: table %q not found", table)
	}
	var defs []string
	for _, c := range cols {
		def := "  " + dbcore.QuoteDoubleIdent(c.Name) + " " + c.Type
		if !c.Nullable {
			def += " NOT NULL"
		}
		if c.Comment != "" {
			def += " COMMENT " + quoteLiteral(c.Comment)
		}
		defs = append(defs, def)
	}
	return "CREATE TABLE " + tableRef(spec, table) + " (\n" + strings.Join(defs, ",\n") + "\n);", nil
}

// quoteLiteral renders a SQL string literal.
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// BrowseRows reads one page.
func (d *TrinoDriver) BrowseRows(ctx context.Context, spec, table string, req *dbcore.TableRowsRequest) (*dbcore.TableRowsResult, error) {
	if req == nil {
		req = &dbcore.TableRowsRequest{Page: 1, PageSize: 200}
	}
	dial := TrinoDialect{}
	query, args := dial.BuildRowsQuery(spec, table, req)
	res, err := dbcore.RunQuery(ctx, d.db, query, args)
	if err != nil {
		return nil, err
	}
	out := &dbcore.TableRowsResult{Columns: res.Columns, Rows: res.Rows}
	if !req.NoCount {
		cq, cargs := dial.BuildCountQuery(spec, table, req)
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
func (d *TrinoDriver) BrowseRowsCount(ctx context.Context, spec, table string, req *dbcore.TableRowsRequest) (uint64, error) {
	dial := TrinoDialect{}
	query, args := dial.BuildCountQuery(spec, table, req)
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
