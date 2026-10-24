// Package mysql implements the dbcore SQLDriver/SQLTxDriver for MySQL and
// MariaDB via go-sql-driver/mysql. It mirrors the SQLite and PostgreSQL drivers
// so all engines share identical call patterns (DB-CORE-CONTRACT.md §5).
//
// DSN example: "user:pass@tcp(127.0.0.1:3306)/dbname?parseTime=true".
// The catalog treats the connected database as the schema (MySQL has no
// separate schema layer), so ListTables/ListColumns take the database name.
package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/go-sql-driver/mysql" // registers the "mysql" driver with database/sql

	"github.com/dbcore/dbcore"
)

// DriverName is the go-sql-driver/mysql registered driver name.
const DriverName = "mysql"

// MySQLDriver is a dbcore.SQLTxDriver backed by a *sql.DB opened with go-sql-driver.
type MySQLDriver struct {
	db      *sql.DB
	dialect dbcore.Dialect
	owned   bool
}

// NewFromDB wraps an existing *sql.DB without taking ownership: Close becomes a
// no-op so callers such as client pools keep managing the handle.
func NewFromDB(db *sql.DB) *MySQLDriver {
	return &MySQLDriver{db: db, dialect: &MySQLDialect{}}
}

// Open connects to MySQL using a go-sql-driver DSN.
func Open(dsn string) (*MySQLDriver, error) {
	db, err := sql.Open(DriverName, dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &MySQLDriver{db: db, dialect: &MySQLDialect{}, owned: true}, nil
}

func (d *MySQLDriver) Kind() dbcore.DatabaseKind { return dbcore.KindMySQL }
func (d *MySQLDriver) Close() error {
	if !d.owned {
		return nil
	}
	return d.db.Close()
}
func (d *MySQLDriver) Ping(ctx context.Context) error { return d.db.PingContext(ctx) }

func (d *MySQLDriver) AsSQL() (dbcore.SQLDriver, bool) { return d, true }
func (d *MySQLDriver) AsKV() (dbcore.KVDriver, bool)   { return nil, false }

func (d *MySQLDriver) Execute(ctx context.Context, query string, args ...dbcore.Value) (*dbcore.QueryResult, error) {
	if dbcore.IsReadQuery(query) {
		return dbcore.RunQuery(ctx, d.db, query, args)
	}
	affected, lastID, err := dbcore.RunExec(ctx, d.db, query, args)
	if err != nil {
		return nil, err
	}
	return &dbcore.QueryResult{AffectedRows: affected, LastInsertID: lastID}, nil
}

func (d *MySQLDriver) Begin(ctx context.Context, mode dbcore.TxMode) (*sql.Tx, error) {
	return d.db.BeginTx(ctx, dbcore.TxOptionsFromMode(mode))
}

func (d *MySQLDriver) ExecuteIn(exec dbcore.SQLTx, ctx context.Context, query string, args ...dbcore.Value) (*dbcore.QueryResult, error) {
	if dbcore.IsReadQuery(query) {
		return dbcore.RunQuery(ctx, exec, query, args)
	}
	affected, lastID, err := dbcore.RunExec(ctx, exec, query, args)
	if err != nil {
		return nil, err
	}
	return &dbcore.QueryResult{AffectedRows: affected, LastInsertID: lastID}, nil
}

// resolveSchema returns schema, or the session's current database when empty.
func (d *MySQLDriver) resolveSchema(ctx context.Context, schema string) (string, error) {
	if schema != "" {
		return schema, nil
	}
	res, err := dbcore.RunQuery(ctx, d.db, `SELECT DATABASE()`, nil)
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 || res.Rows[0][0].Kind != dbcore.ValText {
		return "", nil // no database selected: catalog queries return no rows
	}
	return res.Rows[0][0].S, nil
}

func (d *MySQLDriver) ListSchemas(ctx context.Context) ([]dbcore.SchemaInfo, error) {
	q, a := d.dialect.CatalogQuery(dbcore.CatalogSchemas, "", "")
	res, err := dbcore.RunQuery(ctx, d.db, q, a)
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.SchemaInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		out = append(out, dbcore.SchemaInfo{Name: r[0].S})
	}
	return out, nil
}

func (d *MySQLDriver) ListTables(ctx context.Context, schema string) ([]dbcore.TableInfo, error) {
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
		if strings.EqualFold(r[2].S, "VIEW") {
			kind = "view"
		}
		info := dbcore.TableInfo{Schema: r[0].S, Name: r[1].S, Type: kind, Comment: r[3].S, Rows: -1}
		if kind == "table" && r[4].Kind != dbcore.ValNull {
			info.Rows, info.ApproxRows = r[4].I, true
		}
		out = append(out, info)
	}
	return out, nil
}

func (d *MySQLDriver) ListColumns(ctx context.Context, schema, table string) ([]dbcore.ColumnInfo, error) {
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
			IsPrimaryKey: r[4].S == "PRI",
			Comment:      r[5].S,
		}
		if r[3].Kind != dbcore.ValNull {
			s := r[3].S
			ci.Default = &s
		}
		out = append(out, ci)
	}
	return out, nil
}

func (d *MySQLDriver) ListIndexes(ctx context.Context, schema, table string) ([]dbcore.IndexInfo, error) {
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
			out[i].Columns = append(out[i].Columns, r[2].S)
			continue
		}
		index[name] = len(out)
		out = append(out, dbcore.IndexInfo{
			Name:    name,
			Kind:    r[3].S,
			Unique:  r[1].I != 0,
			Primary: r[4].I != 0,
			Columns: []string{r[2].S},
		})
	}
	return out, nil
}

func (d *MySQLDriver) ListForeignKeys(ctx context.Context, schema, table string) ([]dbcore.ForeignKeyInfo, error) {
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

func (d *MySQLDriver) CreateTableSQL(ctx context.Context, schema, table string) (string, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return "", err
	}
	q := "SHOW CREATE TABLE " + d.dialect.QuoteIdent(schema, table)
	res, err := dbcore.RunQuery(ctx, d.db, q, nil)
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 || len(res.Rows[0]) < 2 {
		return "", fmt.Errorf("table %q not found", table)
	}
	switch res.Rows[0][1].Kind {
	case dbcore.ValText:
		return res.Rows[0][1].S, nil
	case dbcore.ValBlob:
		return string(res.Rows[0][1].B), nil
	default:
		return "", fmt.Errorf("table %q has no CREATE statement", table)
	}
}

func (d *MySQLDriver) BrowseRows(ctx context.Context, schema, table string, req *dbcore.TableRowsRequest) (*dbcore.TableRowsResult, error) {
	q, a := d.dialect.BuildRowsQuery(schema, table, req)
	res, err := dbcore.RunQuery(ctx, d.db, q, a)
	if err != nil {
		return nil, err
	}
	out := &dbcore.TableRowsResult{Columns: res.Columns, Rows: res.Rows}
	cq, ca := d.dialect.BuildCountQuery(schema, table, req)
	if cres, cerr := dbcore.RunQuery(ctx, d.db, cq, ca); cerr == nil && len(cres.Rows) > 0 {
		out.Total = uint64(cres.Rows[0][0].I)
	}
	return out, nil
}

func (d *MySQLDriver) BrowseRowsCount(ctx context.Context, schema, table string, req *dbcore.TableRowsRequest) (uint64, error) {
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

// MySQLDialect implements dbcore.Dialect for MySQL/MariaDB.
type MySQLDialect struct{}

func (MySQLDialect) Kind() dbcore.DatabaseKind { return dbcore.KindMySQL }

func (MySQLDialect) QuoteIdent(parts ...string) string {
	q := make([]string, len(parts))
	for i, p := range parts {
		q[i] = "`" + strings.ReplaceAll(p, "`", "``") + "`"
	}
	return strings.Join(q, ".")
}

func (MySQLDialect) Placeholder(idx int) string { return "?" }

func (MySQLDialect) TypeName(col dbcore.ColumnInfo) string { return col.Type }

func (MySQLDialect) LimitOffset(limit, offset int) string {
	if limit <= 0 {
		return ""
	}
	if offset <= 0 {
		return fmt.Sprintf("LIMIT %d", limit)
	}
	return fmt.Sprintf("LIMIT %d OFFSET %d", limit, offset)
}

func (l MySQLDialect) BuildRowsQuery(schema, table string, req *dbcore.TableRowsRequest) (string, []dbcore.Value) {
	var b strings.Builder
	b.WriteString("SELECT * FROM ")
	b.WriteString(l.QuoteIdent(schema, table))
	args := l.appendWhere(&b, req)
	dbcore.AppendSortLimit(&b, l, req)
	return b.String(), args
}

func (l MySQLDialect) BuildCountQuery(schema, table string, req *dbcore.TableRowsRequest) (string, []dbcore.Value) {
	var b strings.Builder
	b.WriteString("SELECT COUNT(*) FROM ")
	b.WriteString(l.QuoteIdent(schema, table))
	args := l.appendWhere(&b, req)
	return b.String(), args
}

func (l MySQLDialect) appendWhere(b *strings.Builder, req *dbcore.TableRowsRequest) []dbcore.Value {
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

func (l MySQLDialect) CatalogQuery(kind dbcore.CatalogKind, schema, obj string) (string, []dbcore.Value) {
	switch kind {
	case dbcore.CatalogSchemas:
		return `SELECT SCHEMA_NAME FROM information_schema.SCHEMATA
			WHERE SCHEMA_NAME NOT IN ('information_schema','mysql','performance_schema','sys')
			ORDER BY SCHEMA_NAME`, nil
	case dbcore.CatalogTables:
		return `SELECT TABLE_SCHEMA, TABLE_NAME, TABLE_TYPE, TABLE_COMMENT, TABLE_ROWS
			FROM information_schema.TABLES
			WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME`,
			[]dbcore.Value{{Kind: dbcore.ValText, S: schema}}
	case dbcore.CatalogColumns:
		return `SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT, COLUMN_KEY, COLUMN_COMMENT
			FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION`,
			[]dbcore.Value{{Kind: dbcore.ValText, S: schema}, {Kind: dbcore.ValText, S: obj}}
	case dbcore.CatalogIndexes:
		return `SELECT INDEX_NAME,
			CASE WHEN NON_UNIQUE = 0 THEN 1 ELSE 0 END,
			COLUMN_NAME,
			CASE WHEN INDEX_NAME = 'PRIMARY' THEN 'primary'
				WHEN NON_UNIQUE = 0 THEN 'unique' ELSE 'index' END,
			CASE WHEN INDEX_NAME = 'PRIMARY' THEN 1 ELSE 0 END
			FROM information_schema.STATISTICS
			WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY INDEX_NAME, SEQ_IN_INDEX`,
			[]dbcore.Value{{Kind: dbcore.ValText, S: schema}, {Kind: dbcore.ValText, S: obj}}
	case dbcore.CatalogForeignKeys:
		return `SELECT k.CONSTRAINT_NAME, k.COLUMN_NAME, k.REFERENCED_TABLE_SCHEMA,
			k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME, r.DELETE_RULE, r.UPDATE_RULE
			FROM information_schema.KEY_COLUMN_USAGE k
			INNER JOIN information_schema.REFERENTIAL_CONSTRAINTS r
				ON r.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA
				AND r.CONSTRAINT_NAME = k.CONSTRAINT_NAME
				AND r.TABLE_NAME = k.TABLE_NAME
			WHERE k.TABLE_SCHEMA = ? AND k.TABLE_NAME = ? AND k.REFERENCED_TABLE_NAME IS NOT NULL
			ORDER BY k.CONSTRAINT_NAME, k.ORDINAL_POSITION`,
			[]dbcore.Value{{Kind: dbcore.ValText, S: schema}, {Kind: dbcore.ValText, S: obj}}
	default:
		return `SELECT SCHEMA_NAME FROM information_schema.SCHEMATA
			WHERE SCHEMA_NAME NOT IN ('information_schema','mysql','performance_schema','sys')
			ORDER BY SCHEMA_NAME`, nil
	}
}
