// Package mssql implements the dbcore SQLDriver/SQLTxDriver for Microsoft SQL
// Server via github.com/microsoft/go-mssqldb. It mirrors the SQLite, PostgreSQL
// and MySQL drivers so every engine shares identical call patterns
// (DB-CORE-CONTRACT.md §5).
//
// DSN example: "sqlserver://user:pass@localhost:1433?database=mydb&encrypt=disable".
// The connected database is implicit; schema names (usually "dbo") are passed
// explicitly to the catalog methods.
package mssql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/microsoft/go-mssqldb" // registers the "sqlserver" driver with database/sql

	"github.com/dbcore/dbcore"
)

// DriverName is the go-mssqldb registered driver name.
const DriverName = "sqlserver"

// MSSQLDriver is a dbcore.SQLTxDriver backed by a *sql.DB opened with go-mssqldb.
type MSSQLDriver struct {
	db      *sql.DB
	dialect dbcore.Dialect
	owned   bool
}

// NewFromDB wraps an existing *sql.DB without taking ownership: Close becomes a
// no-op so callers such as client pools keep managing the handle.
func NewFromDB(db *sql.DB) *MSSQLDriver {
	return &MSSQLDriver{db: db, dialect: &MSSQLDialect{}}
}

// Open connects to SQL Server using a go-mssqldb DSN.
func Open(dsn string) (*MSSQLDriver, error) {
	db, err := sql.Open(DriverName, dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &MSSQLDriver{db: db, dialect: &MSSQLDialect{}, owned: true}, nil
}

func (d *MSSQLDriver) Kind() dbcore.DatabaseKind { return dbcore.KindMSSQL }
func (d *MSSQLDriver) Close() error {
	if !d.owned {
		return nil
	}
	return d.db.Close()
}
func (d *MSSQLDriver) Ping(ctx context.Context) error { return d.db.PingContext(ctx) }

func (d *MSSQLDriver) AsSQL() (dbcore.SQLDriver, bool) { return d, true }
func (d *MSSQLDriver) AsKV() (dbcore.KVDriver, bool)   { return nil, false }

func (d *MSSQLDriver) Execute(ctx context.Context, query string, args ...dbcore.Value) (*dbcore.QueryResult, error) {
	if dbcore.IsReadQuery(query) {
		return dbcore.RunQuery(ctx, d.db, query, args)
	}
	affected, lastID, err := dbcore.RunExec(ctx, d.db, query, args)
	if err != nil {
		return nil, err
	}
	return &dbcore.QueryResult{AffectedRows: affected, LastInsertID: lastID}, nil
}

func (d *MSSQLDriver) Begin(ctx context.Context, mode dbcore.TxMode) (*sql.Tx, error) {
	return d.db.BeginTx(ctx, dbcore.TxOptionsFromMode(mode))
}

func (d *MSSQLDriver) ExecuteIn(exec dbcore.SQLTx, ctx context.Context, query string, args ...dbcore.Value) (*dbcore.QueryResult, error) {
	if dbcore.IsReadQuery(query) {
		return dbcore.RunQuery(ctx, exec, query, args)
	}
	affected, lastID, err := dbcore.RunExec(ctx, exec, query, args)
	if err != nil {
		return nil, err
	}
	return &dbcore.QueryResult{AffectedRows: affected, LastInsertID: lastID}, nil
}

// resolveSchema returns schema, or the login's default schema when empty.
func (d *MSSQLDriver) resolveSchema(ctx context.Context, schema string) (string, error) {
	if schema != "" {
		return schema, nil
	}
	res, err := dbcore.RunQuery(ctx, d.db, `SELECT COALESCE(SCHEMA_NAME(), 'dbo')`, nil)
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 || res.Rows[0][0].Kind != dbcore.ValText {
		return "", fmt.Errorf("mssql: cannot resolve the default schema")
	}
	return res.Rows[0][0].S, nil
}

func (d *MSSQLDriver) ListSchemas(ctx context.Context) ([]dbcore.SchemaInfo, error) {
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

func (d *MSSQLDriver) ListTables(ctx context.Context, schema string) ([]dbcore.TableInfo, error) {
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

func (d *MSSQLDriver) ListColumns(ctx context.Context, schema, table string) ([]dbcore.ColumnInfo, error) {
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
		}
		if r[3].Kind != dbcore.ValNull {
			s := r[3].S
			ci.Default = &s
		}
		out = append(out, ci)
	}
	return out, nil
}

func (d *MSSQLDriver) ListIndexes(ctx context.Context, schema, table string) ([]dbcore.IndexInfo, error) {
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

func (d *MSSQLDriver) ListForeignKeys(ctx context.Context, schema, table string) ([]dbcore.ForeignKeyInfo, error) {
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
// (with length/precision, nullability, defaults) plus the primary key. It is not
// byte-identical to the original script — SQL Server keeps no such script — and
// intentionally omits non-PK indexes, foreign keys, identity and computed
// columns.
func (d *MSSQLDriver) CreateTableSQL(ctx context.Context, schema, table string) (string, error) {
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

func (d *MSSQLDriver) BrowseRows(ctx context.Context, schema, table string, req *dbcore.TableRowsRequest) (*dbcore.TableRowsResult, error) {
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

func (d *MSSQLDriver) BrowseRowsCount(ctx context.Context, schema, table string, req *dbcore.TableRowsRequest) (uint64, error) {
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

// MSSQLDialect implements dbcore.Dialect for SQL Server.
type MSSQLDialect struct{}

func (MSSQLDialect) Kind() dbcore.DatabaseKind { return dbcore.KindMSSQL }

func (MSSQLDialect) QuoteIdent(parts ...string) string {
	q := make([]string, len(parts))
	for i, p := range parts {
		q[i] = "[" + strings.ReplaceAll(p, "]", "]]") + "]"
	}
	return strings.Join(q, ".")
}

func (MSSQLDialect) Placeholder(idx int) string { return fmt.Sprintf("@p%d", idx) }

func (MSSQLDialect) TypeName(col dbcore.ColumnInfo) string { return col.Type }

// LimitOffset renders the OFFSET/FETCH tail only. T-SQL requires an ORDER BY for
// OFFSET/FETCH, so BuildRowsQuery supplies one instead of AppendSortLimit.
func (MSSQLDialect) LimitOffset(limit, offset int) string {
	if limit <= 0 {
		return ""
	}
	return fmt.Sprintf("OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", offset, limit)
}

func (l MSSQLDialect) BuildRowsQuery(schema, table string, req *dbcore.TableRowsRequest) (string, []dbcore.Value) {
	var b strings.Builder
	b.WriteString("SELECT * FROM ")
	b.WriteString(l.QuoteIdent(schema, table))
	args := l.appendWhere(&b, req)
	l.appendOrderLimit(&b, req)
	return b.String(), args
}

func (l MSSQLDialect) BuildCountQuery(schema, table string, req *dbcore.TableRowsRequest) (string, []dbcore.Value) {
	var b strings.Builder
	b.WriteString("SELECT COUNT(*) FROM ")
	b.WriteString(l.QuoteIdent(schema, table))
	args := l.appendWhere(&b, req)
	return b.String(), args
}

func (l MSSQLDialect) appendOrderLimit(b *strings.Builder, req *dbcore.TableRowsRequest) {
	if len(req.Sort) > 0 {
		b.WriteString(" ORDER BY ")
		for i, s := range req.Sort {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(l.QuoteIdent(s.Column))
			if s.Desc {
				b.WriteString(" DESC")
			}
		}
	}
	limit, offset := req.PageSize, 0
	if req.Page > 1 {
		offset = (req.Page - 1) * req.PageSize
	}
	if limit <= 0 {
		return
	}
	if len(req.Sort) == 0 {
		// OFFSET/FETCH is only legal after ORDER BY; a constant sort is the
		// standard trick when the caller did not specify one.
		b.WriteString(" ORDER BY (SELECT NULL)")
	}
	b.WriteString(" ")
	b.WriteString(l.LimitOffset(limit, offset))
}

func (l MSSQLDialect) appendWhere(b *strings.Builder, req *dbcore.TableRowsRequest) []dbcore.Value {
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
		} else if op == "!=" {
			op = "<>"
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

// sqlTypeExpr renders a full T-SQL type from INFORMATION_SCHEMA.COLUMNS aliased
// as c, including length/precision that DATA_TYPE alone drops.
const sqlTypeExpr = `CASE
	WHEN c.DATA_TYPE IN (N'char', N'varchar', N'binary', N'varbinary', N'nchar', N'nvarchar') THEN
		c.DATA_TYPE + N'(' + CASE WHEN c.CHARACTER_MAXIMUM_LENGTH = -1 THEN N'max'
			ELSE CAST(c.CHARACTER_MAXIMUM_LENGTH AS nvarchar(10)) END + N')'
	WHEN c.DATA_TYPE IN (N'decimal', N'numeric') THEN
		c.DATA_TYPE + N'(' + CAST(c.NUMERIC_PRECISION AS nvarchar(10)) + N','
			+ CAST(c.NUMERIC_SCALE AS nvarchar(10)) + N')'
	WHEN c.DATA_TYPE IN (N'datetime2', N'datetimeoffset', N'time') THEN
		c.DATA_TYPE + N'(' + CAST(c.DATETIME_PRECISION AS nvarchar(10)) + N')'
	ELSE c.DATA_TYPE
END`

func (MSSQLDialect) CatalogQuery(kind dbcore.CatalogKind, schema, obj string) (string, []dbcore.Value) {
	switch kind {
	case dbcore.CatalogSchemas:
		return `SELECT name FROM sys.schemas
			WHERE name NOT IN (N'sys', N'INFORMATION_SCHEMA', N'guest')
			ORDER BY name`, nil
	case dbcore.CatalogTables:
		return `SELECT t.TABLE_SCHEMA, t.TABLE_NAME, t.TABLE_TYPE,
				CAST(ep.value AS nvarchar(4000)),
				COALESCE(pr.row_count, -1)
			FROM INFORMATION_SCHEMA.TABLES t
			LEFT JOIN sys.extended_properties ep
				ON ep.class = 1
				AND ep.major_id = OBJECT_ID(QUOTENAME(t.TABLE_SCHEMA) + N'.' + QUOTENAME(t.TABLE_NAME))
				AND ep.minor_id = 0 AND ep.name = N'MS_Description'
			LEFT JOIN (
				SELECT p.object_id, SUM(p.rows) AS row_count
				FROM sys.partitions p
				WHERE p.index_id IN (0, 1)
				GROUP BY p.object_id
			) pr
				ON pr.object_id = OBJECT_ID(QUOTENAME(t.TABLE_SCHEMA) + N'.' + QUOTENAME(t.TABLE_NAME))
			WHERE t.TABLE_SCHEMA = @p1 AND t.TABLE_TYPE IN (N'BASE TABLE', N'VIEW')
			ORDER BY t.TABLE_NAME`,
			[]dbcore.Value{{Kind: dbcore.ValText, S: schema}}
	case dbcore.CatalogColumns:
		return `SELECT c.COLUMN_NAME, ` + sqlTypeExpr + `, c.IS_NULLABLE, c.COLUMN_DEFAULT,
				CASE WHEN pk.COLUMN_NAME IS NULL THEN 0 ELSE 1 END,
				CAST(ep.value AS nvarchar(4000))
			FROM INFORMATION_SCHEMA.COLUMNS c
			LEFT JOIN (
				SELECT ku.TABLE_SCHEMA, ku.TABLE_NAME, ku.COLUMN_NAME
				FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS tc
				INNER JOIN INFORMATION_SCHEMA.KEY_COLUMN_USAGE ku
					ON ku.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA
					AND ku.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
					AND ku.TABLE_SCHEMA = tc.TABLE_SCHEMA
					AND ku.TABLE_NAME = tc.TABLE_NAME
				WHERE tc.CONSTRAINT_TYPE = N'PRIMARY KEY'
			) pk
				ON pk.TABLE_SCHEMA = c.TABLE_SCHEMA AND pk.TABLE_NAME = c.TABLE_NAME
				AND pk.COLUMN_NAME = c.COLUMN_NAME
			LEFT JOIN sys.extended_properties ep
				ON ep.class = 1
				AND ep.major_id = OBJECT_ID(QUOTENAME(c.TABLE_SCHEMA) + N'.' + QUOTENAME(c.TABLE_NAME))
				AND ep.minor_id = COLUMNPROPERTY(
					OBJECT_ID(QUOTENAME(c.TABLE_SCHEMA) + N'.' + QUOTENAME(c.TABLE_NAME)),
					c.COLUMN_NAME, 'ColumnId')
				AND ep.name = N'MS_Description'
			WHERE c.TABLE_SCHEMA = @p1 AND c.TABLE_NAME = @p2
			ORDER BY c.ORDINAL_POSITION`,
			[]dbcore.Value{{Kind: dbcore.ValText, S: schema}, {Kind: dbcore.ValText, S: obj}}
	case dbcore.CatalogIndexes:
		return `SELECT i.name, CAST(i.is_unique AS int), col.name,
				CASE WHEN i.is_primary_key = 1 THEN 'primary'
					WHEN i.is_unique = 1 THEN 'unique' ELSE 'index' END,
				CAST(i.is_primary_key AS int)
			FROM sys.indexes i
			INNER JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
			INNER JOIN sys.columns col ON col.object_id = ic.object_id AND col.column_id = ic.column_id
			WHERE i.object_id = OBJECT_ID(QUOTENAME(@p1) + N'.' + QUOTENAME(@p2))
				AND i.type > 0 AND i.is_hypothetical = 0 AND ic.is_included_column = 0
			ORDER BY i.name, ic.key_ordinal`,
			[]dbcore.Value{{Kind: dbcore.ValText, S: schema}, {Kind: dbcore.ValText, S: obj}}
	case dbcore.CatalogForeignKeys:
		return `SELECT fk.name, pc.name, rs.name, rt.name, rc.name,
				fk.delete_referential_action_desc, fk.update_referential_action_desc
			FROM sys.foreign_keys fk
			INNER JOIN sys.foreign_key_columns fkc ON fkc.constraint_object_id = fk.object_id
			INNER JOIN sys.columns pc ON pc.object_id = fkc.parent_object_id AND pc.column_id = fkc.parent_column_id
			INNER JOIN sys.tables rt ON rt.object_id = fkc.referenced_object_id
			INNER JOIN sys.schemas rs ON rs.schema_id = rt.schema_id
			INNER JOIN sys.columns rc ON rc.object_id = fkc.referenced_object_id AND rc.column_id = fkc.referenced_column_id
			WHERE fk.parent_object_id = OBJECT_ID(QUOTENAME(@p1) + N'.' + QUOTENAME(@p2))
			ORDER BY fk.name, fkc.constraint_column_id`,
			[]dbcore.Value{{Kind: dbcore.ValText, S: schema}, {Kind: dbcore.ValText, S: obj}}
	case dbcore.CatalogPrimaryKey:
		return `SELECT tc.CONSTRAINT_NAME, kcu.COLUMN_NAME
			FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS tc
			INNER JOIN INFORMATION_SCHEMA.KEY_COLUMN_USAGE kcu
				ON kcu.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA
				AND kcu.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
				AND kcu.TABLE_SCHEMA = tc.TABLE_SCHEMA
				AND kcu.TABLE_NAME = tc.TABLE_NAME
			WHERE tc.CONSTRAINT_TYPE = N'PRIMARY KEY'
				AND tc.TABLE_SCHEMA = @p1 AND tc.TABLE_NAME = @p2
			ORDER BY kcu.ORDINAL_POSITION`,
			[]dbcore.Value{{Kind: dbcore.ValText, S: schema}, {Kind: dbcore.ValText, S: obj}}
	default:
		return `SELECT name FROM sys.schemas
			WHERE name NOT IN (N'sys', N'INFORMATION_SCHEMA', N'guest')
			ORDER BY name`, nil
	}
}
