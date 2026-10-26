// Package postgres implements the dbcore SQLDriver/SQLTxDriver for PostgreSQL via
// jackc/pgx/v5 (database/sql compatibility layer). It mirrors the SQLite driver so
// the two share identical call patterns (contract §5).
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/dbcore/dbcore"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver with database/sql
)

// DriverName is the pgx registered driver name for database/sql.
const DriverName = "pgx"

// PostgresDriver is a dbcore.SQLTxDriver backed by a *sql.DB opened with pgx.
type PostgresDriver struct {
	*dbcore.SessionCore
	db      *sql.DB
	dialect dbcore.Dialect
	owned   bool
}

// NewFromDB wraps an existing *sql.DB without taking ownership: Close becomes a
// no-op so callers such as client pools keep managing the handle. The handle may
// be opened with any database/sql driver that speaks PostgreSQL (pgx or lib/pq).
func NewFromDB(db *sql.DB) *PostgresDriver {
	d0 := &PostgresDriver{db: db, dialect: &PostgresDialect{}}
	d0.initWide()
	return d0
}

// Open connects to PostgreSQL using a pgx DSN (e.g.
// "postgres://user:pass@localhost:5432/db?sslmode=disable").
func Open(dsn string) (*PostgresDriver, error) {
	db, err := sql.Open(DriverName, dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	d0 := &PostgresDriver{db: db, dialect: &PostgresDialect{}, owned: true}
	d0.initWide()
	return d0, nil
}

func (d *PostgresDriver) Kind() dbcore.DatabaseKind { return dbcore.KindPostgres }
func (d *PostgresDriver) Close() error {
	if !d.owned {
		return nil
	}
	return d.db.Close()
}
func (d *PostgresDriver) Ping(ctx context.Context) error { return d.db.PingContext(ctx) }

func (d *PostgresDriver) AsSQL() (dbcore.SQLDriver, bool) { return d, true }
func (d *PostgresDriver) AsKV() (dbcore.KVDriver, bool)   { return nil, false }

func (d *PostgresDriver) Execute(ctx context.Context, query string, args ...dbcore.Value) (*dbcore.QueryResult, error) {
	if dbcore.IsReadQuery(query) {
		return dbcore.RunQuery(ctx, d.db, query, args)
	}
	affected, lastID, err := dbcore.RunExec(ctx, d.db, query, args)
	if err != nil {
		return nil, err
	}
	return &dbcore.QueryResult{AffectedRows: affected, LastInsertID: lastID}, nil
}

func (d *PostgresDriver) Begin(ctx context.Context, mode dbcore.TxMode) (*sql.Tx, error) {
	return d.db.BeginTx(ctx, dbcore.TxOptionsFromMode(mode))
}

func (d *PostgresDriver) ExecuteIn(exec dbcore.SQLTx, ctx context.Context, query string, args ...dbcore.Value) (*dbcore.QueryResult, error) {
	if dbcore.IsReadQuery(query) {
		return dbcore.RunQuery(ctx, exec, query, args)
	}
	affected, lastID, err := dbcore.RunExec(ctx, exec, query, args)
	if err != nil {
		return nil, err
	}
	return &dbcore.QueryResult{AffectedRows: affected, LastInsertID: lastID}, nil
}

// resolveSchema returns schema, or the session's current schema when empty.
func (d *PostgresDriver) resolveSchema(ctx context.Context, schema string) (string, error) {
	if schema != "" {
		return schema, nil
	}
	res, err := dbcore.RunQuery(ctx, d.db, `SELECT COALESCE(current_schema(), 'public')`, nil)
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 || res.Rows[0][0].Kind != dbcore.ValText {
		return "", fmt.Errorf("postgres: cannot resolve the current schema")
	}
	return res.Rows[0][0].S, nil
}

func (d *PostgresDriver) ListSchemas(ctx context.Context) ([]dbcore.SchemaInfo, error) {
	res, err := dbcore.RunQuery(ctx, d.db,
		`SELECT schema_name FROM information_schema.schemata WHERE schema_name NOT LIKE 'pg\_%' AND schema_name <> 'information_schema' ORDER BY 1`, nil)
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.SchemaInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		out = append(out, dbcore.SchemaInfo{Name: r[0].S})
	}
	return out, nil
}

func (d *PostgresDriver) ListTables(ctx context.Context, schema string) ([]dbcore.TableInfo, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a := d.dialect.CatalogQuery(dbcore.CatalogTables, schema, "")
	res, err := dbcore.RunQuery(ctx, d.db, q, a)
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.TableInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		kind := "table"
		switch {
		case strings.EqualFold(r[2].S, "MATERIALIZED VIEW"):
			kind = "materialized_view"
		case strings.EqualFold(r[2].S, "VIEW"):
			kind = "view"
		}
		info := dbcore.TableInfo{Schema: r[0].S, Name: r[1].S, Type: kind, Comment: r[3].S, Rows: r[4].I}
		info.ApproxRows = kind == "table" && info.Rows >= 0
		out = append(out, info)
	}
	return out, nil
}

func (d *PostgresDriver) ListColumns(ctx context.Context, schema, table string) ([]dbcore.ColumnInfo, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a := d.dialect.CatalogQuery(dbcore.CatalogColumns, schema, table)
	res, err := dbcore.RunQuery(ctx, d.db, q, a)
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.ColumnInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		ci := dbcore.ColumnInfo{
			Name:         r[0].S,
			Type:         r[1].S,
			Nullable:     r[2].S == "YES",
			IsPrimaryKey: r[4].I != 0,
			Comment:      r[5].S,
			// r[6] = attnum (1-based), r[7] = 1/0 identity, generated or
			// serial (nextval default) flag.
			OrdinalPosition: int32(r[6].I),
			IsAutoIncrement: r[7].I != 0,
		}
		if r[3].Kind != dbcore.ValNull {
			s := r[3].S
			ci.Default = &s
		}
		out = append(out, ci)
	}
	return out, nil
}

func (d *PostgresDriver) ListIndexes(ctx context.Context, schema, table string) ([]dbcore.IndexInfo, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a := d.dialect.CatalogQuery(dbcore.CatalogIndexes, schema, table)
	res, err := dbcore.RunQuery(ctx, d.db, q, a)
	if err != nil {
		return nil, nil
	}
	out := make([]dbcore.IndexInfo, 0, len(res.Rows))
	index := make(map[string]int, len(res.Rows))
	for _, r := range res.Rows {
		name := r[0].S
		if i, ok := index[name]; ok {
			out[i].Parts = append(out[i].Parts, pgIndexPart(r))
			if col := pgIndexPart(r).Column; col != "" {
				out[i].Columns = append(out[i].Columns, col)
			}
			continue
		}
		index[name] = len(out)
		info := dbcore.IndexInfo{
			Name:      name,
			Kind:      r[3].S,
			Unique:    r[1].I != 0,
			Primary:   r[4].I != 0,
			Method:    r[5].S,
			Predicate: r[6].S,
		}
		if part := pgIndexPart(r); part.Column != "" {
			info.Columns = []string{part.Column}
		}
		info.Parts = []dbcore.IndexPart{pgIndexPart(r)}
		out = append(out, info)
	}
	return out, nil
}

// pgIndexPart reads one key part from a CatalogIndexes row: a plain column
// (r[2]) or an expression rendered by pg_get_indexdef (r[7]), plus its DESC
// flag (r[8], from pg_index.indoption).
func pgIndexPart(r []dbcore.Value) dbcore.IndexPart {
	part := dbcore.IndexPart{Desc: r[8].I != 0}
	if expr := r[7].S; expr != "" {
		part.Expression = expr
	} else {
		part.Column = r[2].S
	}
	return part
}

func (d *PostgresDriver) ListForeignKeys(ctx context.Context, schema, table string) ([]dbcore.ForeignKeyInfo, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a := d.dialect.CatalogQuery(dbcore.CatalogForeignKeys, schema, table)
	res, err := dbcore.RunQuery(ctx, d.db, q, a)
	if err != nil {
		return nil, nil
	}
	out := make([]dbcore.ForeignKeyInfo, 0, len(res.Rows))
	index := make(map[string]int, len(res.Rows))
	for _, r := range res.Rows {
		name := r[0].S
		if i, ok := index[name]; ok {
			out[i].Columns = append(out[i].Columns, r[1].S)
			out[i].RefColumns = append(out[i].RefColumns, r[4].S)
			continue
		}
		index[name] = len(out)
		out = append(out, dbcore.ForeignKeyInfo{
			Name:       name,
			Columns:    []string{r[1].S},
			RefSchema:  r[2].S,
			RefTable:   r[3].S,
			RefColumns: []string{r[4].S},
			OnDelete:   dbcore.NormalizeFKAction(r[5].S),
			OnUpdate:   dbcore.NormalizeFKAction(r[6].S),
		})
	}
	return out, nil
}

// CreateTableSQL reconstructs a CREATE TABLE script from the catalog: columns
// (with type, nullability, defaults, identity) plus the primary key. PostgreSQL
// keeps no queryable copy of the original text, so this is a reconstruction,
// not byte-identical DDL; it intentionally omits non-PK indexes, foreign keys
// and sequence ownership.
func (d *PostgresDriver) CreateTableSQL(ctx context.Context, schema, table string) (string, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return "", err
	}
	cols, err := d.ListColumns(ctx, schema, table)
	if err != nil {
		return "", err
	}
	if len(cols) == 0 {
		return "", fmt.Errorf("table %q not found", table)
	}

	var b strings.Builder
	b.WriteString("CREATE TABLE ")
	b.WriteString(d.dialect.QuoteIdent(schema, table))
	b.WriteString(" (\n")
	for i, c := range cols {
		b.WriteString("    ")
		b.WriteString(d.dialect.QuoteIdent(c.Name))
		b.WriteString(" ")
		b.WriteString(c.Type)
		if c.IsAutoIncrement {
			b.WriteString(" GENERATED ALWAYS AS IDENTITY")
		}
		if c.Nullable {
			b.WriteString(" NULL")
		} else {
			b.WriteString(" NOT NULL")
		}
		if c.Default != nil && *c.Default != "" {
			b.WriteString(" DEFAULT ")
			b.WriteString(*c.Default)
		}
		if i < len(cols)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString(")")

	q, a := d.dialect.CatalogQuery(dbcore.CatalogPrimaryKey, schema, table)
	pk, err := dbcore.RunQuery(ctx, d.db, q, a)
	if err == nil && len(pk.Rows) > 0 {
		name := pk.Rows[0][0].S
		quoted := make([]string, 0, len(pk.Rows))
		for _, r := range pk.Rows {
			quoted = append(quoted, d.dialect.QuoteIdent(r[1].S))
		}
		b.WriteString(";\nALTER TABLE ")
		b.WriteString(d.dialect.QuoteIdent(schema, table))
		b.WriteString(" ADD CONSTRAINT ")
		b.WriteString(d.dialect.QuoteIdent(name))
		b.WriteString(" PRIMARY KEY (")
		b.WriteString(strings.Join(quoted, ", "))
		b.WriteString(")")
	}
	return b.String(), nil
}

func (d *PostgresDriver) BrowseRows(ctx context.Context, schema, table string, req *dbcore.TableRowsRequest) (*dbcore.TableRowsResult, error) {
	q, a := d.dialect.BuildRowsQuery(schema, table, req)
	res, err := dbcore.RunQuery(ctx, d.db, q, a)
	if err != nil {
		return nil, err
	}
	out := &dbcore.TableRowsResult{Columns: res.Columns, Rows: res.Rows}
	cq, ca := d.dialect.BuildCountQuery(schema, table, req)
	if cres, cerr := dbcore.RunQuery(ctx, d.db, cq, ca); cerr == nil && len(cres.Rows) > 0 {
		out.Total = uint64(cres.Rows[0][0].I)
		out.TotalFound = true
	}
	return out, nil
}

func (d *PostgresDriver) BrowseRowsCount(ctx context.Context, schema, table string, req *dbcore.TableRowsRequest) (uint64, error) {
	q, a := d.dialect.BuildCountQuery(schema, table, req)
	res, err := dbcore.RunQuery(ctx, d.db, q, a)
	if err != nil {
		return 0, err
	}
	if len(res.Rows) == 0 {
		return 0, nil
	}
	return uint64(res.Rows[0][0].I), nil
}

// PostgresDialect implements dbcore.Dialect for PostgreSQL.
type PostgresDialect struct{}

func (PostgresDialect) Kind() dbcore.DatabaseKind { return dbcore.KindPostgres }

func (PostgresDialect) QuoteIdent(parts ...string) string {
	q := make([]string, len(parts))
	for i, p := range parts {
		q[i] = `"` + strings.ReplaceAll(p, `"`, `""`) + `"`
	}
	return strings.Join(q, ".")
}

func (PostgresDialect) Placeholder(idx int) string { return fmt.Sprintf("$%d", idx) }

func (PostgresDialect) TypeName(col dbcore.ColumnInfo) string { return col.Type }

func (PostgresDialect) LimitOffset(limit, offset int) string {
	if limit <= 0 {
		return ""
	}
	if offset <= 0 {
		return fmt.Sprintf("LIMIT %d", limit)
	}
	return fmt.Sprintf("LIMIT %d OFFSET %d", limit, offset)
}

func (l PostgresDialect) BuildRowsQuery(schema, table string, req *dbcore.TableRowsRequest) (string, []dbcore.Value) {
	var b strings.Builder
	b.WriteString("SELECT * FROM ")
	b.WriteString(l.QuoteIdent(schema, table))
	args := l.appendWhere(&b, req)
	args = append(args, dbcore.AppendKeyset(&b, l, req, len(args))...)
	dbcore.AppendSortLimit(&b, l, req)
	return b.String(), args
}

func (l PostgresDialect) BuildCountQuery(schema, table string, req *dbcore.TableRowsRequest) (string, []dbcore.Value) {
	var b strings.Builder
	b.WriteString("SELECT COUNT(*) FROM ")
	b.WriteString(l.QuoteIdent(schema, table))
	args := l.appendWhere(&b, req)
	return b.String(), args
}

func (l PostgresDialect) appendWhere(b *strings.Builder, req *dbcore.TableRowsRequest) []dbcore.Value {
	if len(req.Filter) == 0 {
		return nil
	}
	b.WriteString(" WHERE ")
	args := make([]dbcore.Value, 0, len(req.Filter))
	for i, f := range req.Filter {
		if i > 0 {
			b.WriteString(" AND ")
		}
		op := string(f.Op)
		if op == "" {
			op = "="
		}
		b.WriteString(l.QuoteIdent(f.Column))
		b.WriteString(" ")
		b.WriteString(op)
		b.WriteString(" ")
		b.WriteString(l.Placeholder(len(args) + 1))
		args = append(args, f.Value)
	}
	return args
}

func (l PostgresDialect) CatalogQuery(kind dbcore.CatalogKind, schema, obj string) (string, []dbcore.Value) {
	switch kind {
	case dbcore.CatalogTables:
		return `SELECT n.nspname, c.relname,
				CASE c.relkind WHEN 'v' THEN 'VIEW' WHEN 'm' THEN 'MATERIALIZED VIEW' ELSE 'BASE TABLE' END,
				COALESCE(obj_description(c.oid, 'pg_class'), ''),
				CASE WHEN c.relkind IN ('r', 'p')
					THEN CAST(GREATEST(c.reltuples, -1) AS bigint) ELSE -1 END
			FROM pg_class c
			INNER JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname::text = $1 AND c.relkind IN ('r', 'p', 'v', 'm')
			ORDER BY c.relname`, []dbcore.Value{{Kind: dbcore.ValText, S: schema}}
	case dbcore.CatalogColumns:
		// pg_attribute is authoritative for comments and PK membership; attnum
		// survives dropped columns, unlike information_schema's ordinal_position.
		return `SELECT a.attname,
				format_type(a.atttypid, a.atttypmod),
				CASE WHEN a.attnotnull THEN 'NO' ELSE 'YES' END,
				pg_get_expr(d.adbin, d.adrelid),
				CASE WHEN EXISTS (
					SELECT 1 FROM pg_constraint k
					WHERE k.conrelid = a.attrelid AND k.contype = 'p' AND a.attnum = ANY(k.conkey)
				) THEN 1 ELSE 0 END,
				COALESCE(col_description(a.attrelid, a.attnum), ''),
				a.attnum,
				CASE WHEN COALESCE(a.attidentity <> '', false)
					OR COALESCE(a.attgenerated <> '', false)
					OR COALESCE(pg_get_expr(d.adbin, d.adrelid) LIKE 'nextval(%', false)
					THEN 1 ELSE 0 END
			FROM pg_attribute a
			INNER JOIN pg_class c ON c.oid = a.attrelid
			INNER JOIN pg_namespace n ON n.oid = c.relnamespace
			LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
			WHERE n.nspname::text = $1 AND c.relname::text = $2
				AND a.attnum > 0 AND NOT a.attisdropped
			ORDER BY a.attnum`,
			[]dbcore.Value{{Kind: dbcore.ValText, S: schema}, {Kind: dbcore.ValText, S: obj}}
	case dbcore.CatalogIndexes:
		// indkey pairs with indoption so a key part carries its DESC flag; an
		// expression part has attnum 0, and pg_get_indexdef renders its text
		// (a plain LEFT JOIN would silently drop the part instead).
		return `SELECT i.relname,
				CASE WHEN x.indisunique THEN 1 ELSE 0 END,
				COALESCE(a.attname, ''),
				CASE WHEN x.indisprimary THEN 'primary'
					WHEN x.indisunique THEN 'unique' ELSE 'index' END,
				CASE WHEN x.indisprimary THEN 1 ELSE 0 END,
				am.amname,
				COALESCE(pg_get_expr(x.indpred, x.indrelid), ''),
				CASE WHEN k.attnum = 0 THEN pg_get_indexdef(x.indexrelid, k.ord::int, true) ELSE '' END,
				CASE WHEN (k.opt & 1) = 1 THEN 1 ELSE 0 END
			FROM pg_index x
			INNER JOIN pg_class i ON i.oid = x.indexrelid
			INNER JOIN pg_class t ON t.oid = x.indrelid
			INNER JOIN pg_namespace n ON n.oid = t.relnamespace
			INNER JOIN pg_am am ON am.oid = i.relam
			INNER JOIN LATERAL unnest(x.indkey, x.indoption) WITH ORDINALITY AS k(attnum, opt, ord) ON true
			LEFT JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = k.attnum AND k.attnum > 0
			WHERE n.nspname::text = $1 AND t.relname::text = $2
			ORDER BY i.relname, k.ord`,
			[]dbcore.Value{{Kind: dbcore.ValText, S: schema}, {Kind: dbcore.ValText, S: obj}}
	case dbcore.CatalogForeignKeys:
		// conkey/confkey are paired arrays; unnest both together so composite
		// keys keep their column order instead of forming a cross product.
		return `SELECT k.conname, sa.attname, rn.nspname, rc.relname, ra.attname,
				CASE k.confdeltype WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL'
					WHEN 'd' THEN 'SET DEFAULT' WHEN 'r' THEN 'RESTRICT' ELSE 'NO ACTION' END,
				CASE k.confupdtype WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL'
					WHEN 'd' THEN 'SET DEFAULT' WHEN 'r' THEN 'RESTRICT' ELSE 'NO ACTION' END
			FROM pg_constraint k
			INNER JOIN pg_class c ON c.oid = k.conrelid
			INNER JOIN pg_namespace n ON n.oid = c.relnamespace
			INNER JOIN pg_class rc ON rc.oid = k.confrelid
			INNER JOIN pg_namespace rn ON rn.oid = rc.relnamespace
			INNER JOIN LATERAL unnest(k.conkey, k.confkey) WITH ORDINALITY AS cols(attnum, refnum, ord) ON true
			INNER JOIN pg_attribute sa ON sa.attrelid = k.conrelid AND sa.attnum = cols.attnum
			INNER JOIN pg_attribute ra ON ra.attrelid = k.confrelid AND ra.attnum = cols.refnum
			WHERE k.contype = 'f' AND n.nspname::text = $1 AND c.relname::text = $2
			ORDER BY k.conname, cols.ord`,
			[]dbcore.Value{{Kind: dbcore.ValText, S: schema}, {Kind: dbcore.ValText, S: obj}}
	case dbcore.CatalogPrimaryKey:
		return `SELECT k.conname, a.attname
			FROM pg_constraint k
			INNER JOIN pg_class c ON c.oid = k.conrelid
			INNER JOIN pg_namespace n ON n.oid = c.relnamespace
			INNER JOIN LATERAL unnest(k.conkey) WITH ORDINALITY AS cols(attnum, ord) ON true
			INNER JOIN pg_attribute a ON a.attrelid = k.conrelid AND a.attnum = cols.attnum
			WHERE k.contype = 'p' AND n.nspname::text = $1 AND c.relname::text = $2
			ORDER BY cols.ord`,
			[]dbcore.Value{{Kind: dbcore.ValText, S: schema}, {Kind: dbcore.ValText, S: obj}}
	default:
		return `SELECT schema_name FROM information_schema.schemata WHERE schema_name NOT LIKE 'pg\_%' AND schema_name <> 'information_schema' ORDER BY 1`, nil
	}
}
