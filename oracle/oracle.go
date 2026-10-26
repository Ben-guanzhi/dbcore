// Package oracle implements the dbcore SQLDriver/SQLTxDriver for Oracle
// Database via github.com/sijms/go-ora/v2 (pure Go, no CGO). It mirrors the
// other SQL drivers so every engine shares identical call patterns
// (DB-CORE-CONTRACT.md §5).
//
// DSN example: "oracle://user:pass@localhost:1521/FREEPDB1".
// Oracle schemas are users; pass the owner name explicitly (it is matched
// case-sensitively) or leave schema empty to use the session's current schema.
package oracle

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/sijms/go-ora/v2" // registers the "oracle" driver with database/sql

	"github.com/dbcore/dbcore"
)

// DriverName is the go-ora registered driver name.
const DriverName = "oracle"

// OracleDriver is a dbcore.SQLTxDriver backed by a *sql.DB opened with go-ora.
type OracleDriver struct {
	*dbcore.SessionCore
	db      *sql.DB
	dialect dbcore.Dialect
	owned   bool
}

// NewFromDB wraps an existing *sql.DB without taking ownership: Close becomes a
// no-op so callers such as client pools keep managing the handle.
func NewFromDB(db *sql.DB) *OracleDriver {
	d0 := &OracleDriver{db: db, dialect: &OracleDialect{}}
	d0.initWide()
	return d0
}

// Open connects to Oracle using a go-ora DSN.
func Open(dsn string) (*OracleDriver, error) {
	db, err := sql.Open(DriverName, dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	d0 := &OracleDriver{db: db, dialect: &OracleDialect{}, owned: true}
	d0.initWide()
	return d0, nil
}

func (d *OracleDriver) Kind() dbcore.DatabaseKind { return dbcore.KindOracle }
func (d *OracleDriver) Close() error {
	if !d.owned {
		return nil
	}
	return d.db.Close()
}
func (d *OracleDriver) Ping(ctx context.Context) error { return d.db.PingContext(ctx) }

func (d *OracleDriver) AsSQL() (dbcore.SQLDriver, bool) { return d, true }
func (d *OracleDriver) AsKV() (dbcore.KVDriver, bool)   { return nil, false }

func (d *OracleDriver) Execute(ctx context.Context, query string, args ...dbcore.Value) (*dbcore.QueryResult, error) {
	if dbcore.IsReadQuery(query) {
		return dbcore.RunQuery(ctx, d.db, query, args)
	}
	affected, lastID, err := dbcore.RunExec(ctx, d.db, query, args)
	if err != nil {
		return nil, err
	}
	return &dbcore.QueryResult{AffectedRows: affected, LastInsertID: lastID}, nil
}

func (d *OracleDriver) Begin(ctx context.Context, mode dbcore.TxMode) (*sql.Tx, error) {
	return d.db.BeginTx(ctx, dbcore.TxOptionsFromMode(mode))
}

func (d *OracleDriver) ExecuteIn(exec dbcore.SQLTx, ctx context.Context, query string, args ...dbcore.Value) (*dbcore.QueryResult, error) {
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
func (d *OracleDriver) resolveSchema(ctx context.Context, schema string) (string, error) {
	if schema != "" {
		return schema, nil
	}
	res, err := dbcore.RunQuery(ctx, d.db, `SELECT SYS_CONTEXT('USERENV','CURRENT_SCHEMA') FROM dual`, nil)
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 || res.Rows[0][0].Kind != dbcore.ValText {
		return "", fmt.Errorf("oracle: cannot resolve the current schema")
	}
	return res.Rows[0][0].S, nil
}

// maintainedSchemas lists Oracle's built-in maintenance schemas that a GUI
// should not offer as connection targets (consolidated from polydb, P1-5).
var maintainedSchemas = map[string]bool{
	"SYS": true, "SYSTEM": true, "OUTLN": true, "DBSNMP": true, "APPQOSSYS": true,
	"CTXSYS": true, "MDSYS": true, "ORDSYS": true, "ORDDATA": true, "ORDPLUGINS": true,
	"SI_INFORMTN_SCHEMA": true, "WMSYS": true, "XDB": true, "XS$NULL": true, "DVSYS": true,
	"AUDSYS": true, "GSMADMIN_INTERNAL": true, "OJVMSYS": true, "LBACSYS": true,
}

func (d *OracleDriver) ListSchemas(ctx context.Context) ([]dbcore.SchemaInfo, error) {
	// oracle_maintained (12c+) marks Oracle-supplied accounts directly; older
	// instances lack the column and fall back to the exclusion list.
	res, err := dbcore.RunQuery(ctx, d.db,
		`SELECT username FROM all_users WHERE oracle_maintained = 'N' ORDER BY username`, nil)
	if err != nil {
		var where []string
		for s := range maintainedSchemas {
			where = append(where, "'"+s+"'")
		}
		q := "SELECT username FROM all_users WHERE username NOT IN (" + strings.Join(where, ",") + ") ORDER BY username"
		res, err = dbcore.RunQuery(ctx, d.db, q, nil)
		if err != nil {
			return nil, err
		}
	}
	out := make([]dbcore.SchemaInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		out = append(out, dbcore.SchemaInfo{Name: r[0].S})
	}
	return out, nil
}

func (d *OracleDriver) ListTables(ctx context.Context, schema string) ([]dbcore.TableInfo, error) {
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a := d.dialect.CatalogQuery(dbcore.CatalogTables, s, "")
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
		info := dbcore.TableInfo{Schema: r[0].S, Name: r[1].S, Type: kind, Comment: r[3].S, Rows: r[4].I}
		info.ApproxRows = kind == "table" && info.Rows >= 0
		out = append(out, info)
	}
	return out, nil
}

func (d *OracleDriver) ListColumns(ctx context.Context, schema, table string) ([]dbcore.ColumnInfo, error) {
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a := d.dialect.CatalogQuery(dbcore.CatalogColumns, s, table)
	res, err := dbcore.RunQuery(ctx, d.db, q, a)
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.ColumnInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		ci := dbcore.ColumnInfo{
			Name:         r[0].S,
			Table:        table,
			Type:         r[1].S,
			Nullable:     r[2].S == "Y",
			IsPrimaryKey: r[4].I != 0,
			Comment:      r[5].S,
			// r[6] = COLUMN_ID, r[7] = 1/0 identity-column flag.
			OrdinalPosition: int32(r[6].I),
			IsAutoIncrement: r[7].I != 0,
		}
		if r[3].Kind == dbcore.ValText && r[3].S != "" {
			s := r[3].S
			ci.Default = &s
		}
		out = append(out, ci)
	}
	return out, nil
}

func (d *OracleDriver) ListIndexes(ctx context.Context, schema, table string) ([]dbcore.IndexInfo, error) {
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a := d.dialect.CatalogQuery(dbcore.CatalogIndexes, s, table)
	res, err := dbcore.RunQuery(ctx, d.db, q, a)
	if err != nil {
		return nil, nil
	}
	out := make([]dbcore.IndexInfo, 0, len(res.Rows))
	index := make(map[string]int, len(res.Rows))
	for _, r := range res.Rows {
		name := r[0].S
		part := oracleIndexPart(r)
		if i, ok := index[name]; ok {
			out[i].Parts = append(out[i].Parts, part)
			if part.Column != "" {
				out[i].Columns = append(out[i].Columns, part.Column)
			}
			continue
		}
		index[name] = len(out)
		info := dbcore.IndexInfo{
			Name:    name,
			Kind:    r[3].S,
			Unique:  r[1].I != 0,
			Primary: r[4].I != 0,
			Method:  r[5].S,
			Parts:   []dbcore.IndexPart{part},
		}
		if part.Column != "" {
			info.Columns = []string{part.Column}
		}
		out = append(out, info)
	}
	return out, nil
}

// oracleIndexPart reads one key part from a CatalogIndexes row. A
// function-based index stores a hidden SYS_NC column, so ALL_IND_EXPRESSIONS
// (r[6]) carries the real text: a bare quoted identifier there is still a plain
// column, anything else is the expression. r[7] is ASC/DESC.
func oracleIndexPart(r []dbcore.Value) dbcore.IndexPart {
	part := dbcore.IndexPart{Column: r[2].S, Desc: strings.EqualFold(r[7].S, "DESC")}
	expr := strings.TrimSpace(r[6].S)
	if expr == "" {
		return part
	}
	if col, ok := unquoteOracleIdent(expr); ok {
		part.Column = col
		return part
	}
	part.Expression = expr
	return part
}

// unquoteOracleIdent returns the identifier inside a bare "NAME" expression,
// which ALL_IND_EXPRESSIONS reports for a plain column of a function-based
// index.
func unquoteOracleIdent(expr string) (string, bool) {
	if len(expr) < 3 || expr[0] != '"' || expr[len(expr)-1] != '"' {
		return "", false
	}
	inner := expr[1 : len(expr)-1]
	if strings.Contains(inner, `"`) {
		return "", false
	}
	return inner, true
}

func (d *OracleDriver) ListForeignKeys(ctx context.Context, schema, table string) ([]dbcore.ForeignKeyInfo, error) {
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a := d.dialect.CatalogQuery(dbcore.CatalogForeignKeys, s, table)
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
			// Oracle has no ON UPDATE clause; the reference is always NO ACTION.
			OnUpdate: dbcore.FKActionNoAction,
		})
	}
	return out, nil
}

// CreateTableSQL asks DBMS_METADATA for the original DDL. That package needs
// the SELECT_CATALOG_ROLE (or ownership of the object); when it is unavailable,
// the script is reconstructed from the catalog (columns + primary key).
func (d *OracleDriver) CreateTableSQL(ctx context.Context, schema, table string) (string, error) {
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return "", err
	}
	args := []dbcore.Value{{Kind: dbcore.ValText, S: s}, {Kind: dbcore.ValText, S: table}}
	if res, qerr := dbcore.RunQuery(ctx, d.db,
		`SELECT DBMS_METADATA.GET_DDL('TABLE', :1, :2) FROM dual`, args); qerr == nil && len(res.Rows) > 0 {
		v := res.Rows[0][0]
		ddl := strings.TrimSpace(v.S)
		if v.Kind == dbcore.ValBlob {
			ddl = strings.TrimSpace(string(v.B))
		}
		if ddl != "" {
			return ddl, nil
		}
	}
	return d.reconstructTableSQL(ctx, s, table)
}

func (d *OracleDriver) reconstructTableSQL(ctx context.Context, schema, table string) (string, error) {
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
		if c.Default != nil && *c.Default != "" {
			b.WriteString(" DEFAULT ")
			b.WriteString(*c.Default)
		}
		if c.Nullable {
			b.WriteString(" NULL")
		} else {
			b.WriteString(" NOT NULL")
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

func (d *OracleDriver) BrowseRows(ctx context.Context, schema, table string, req *dbcore.TableRowsRequest) (*dbcore.TableRowsResult, error) {
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a := d.dialect.BuildRowsQuery(s, table, req)
	res, err := dbcore.RunQuery(ctx, d.db, q, a)
	if err != nil {
		return nil, err
	}
	out := &dbcore.TableRowsResult{Columns: res.Columns, Rows: res.Rows}
	cq, ca := d.dialect.BuildCountQuery(s, table, req)
	if cres, cerr := dbcore.RunQuery(ctx, d.db, cq, ca); cerr == nil && len(cres.Rows) > 0 {
		out.Total = uint64(cres.Rows[0][0].I)
		out.TotalFound = true
	}
	return out, nil
}

func (d *OracleDriver) BrowseRowsCount(ctx context.Context, schema, table string, req *dbcore.TableRowsRequest) (uint64, error) {
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return 0, err
	}
	q, a := d.dialect.BuildCountQuery(s, table, req)
	res, err := dbcore.RunQuery(ctx, d.db, q, a)
	if err != nil {
		return 0, err
	}
	if len(res.Rows) == 0 {
		return 0, nil
	}
	return uint64(res.Rows[0][0].I), nil
}

// OracleDialect implements dbcore.Dialect for Oracle 12c and newer.
type OracleDialect struct{}

func (OracleDialect) Kind() dbcore.DatabaseKind { return dbcore.KindOracle }

func (OracleDialect) QuoteIdent(parts ...string) string {
	q := make([]string, len(parts))
	for i, p := range parts {
		q[i] = `"` + strings.ReplaceAll(p, `"`, `""`) + `"`
	}
	return strings.Join(q, ".")
}

func (OracleDialect) Placeholder(idx int) string { return fmt.Sprintf(":%d", idx) }

func (OracleDialect) TypeName(col dbcore.ColumnInfo) string { return col.Type }

func (OracleDialect) LimitOffset(limit, offset int) string {
	if limit <= 0 {
		return ""
	}
	return fmt.Sprintf("OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", offset, limit)
}

func (l OracleDialect) BuildRowsQuery(schema, table string, req *dbcore.TableRowsRequest) (string, []dbcore.Value) {
	var b strings.Builder
	b.WriteString("SELECT * FROM ")
	b.WriteString(l.QuoteIdent(schema, table))
	args := l.appendWhere(&b, req)
	args = append(args, dbcore.AppendKeyset(&b, l, req, len(args))...)
	dbcore.AppendSortLimit(&b, l, req)
	return b.String(), args
}

func (l OracleDialect) BuildCountQuery(schema, table string, req *dbcore.TableRowsRequest) (string, []dbcore.Value) {
	var b strings.Builder
	b.WriteString("SELECT COUNT(*) FROM ")
	b.WriteString(l.QuoteIdent(schema, table))
	args := l.appendWhere(&b, req)
	return b.String(), args
}

func (l OracleDialect) appendWhere(b *strings.Builder, req *dbcore.TableRowsRequest) []dbcore.Value {
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

// sqlTypeExpr renders a full Oracle type from ALL_TAB_COLUMNS aliased as c,
// restoring the length/precision that DATA_TYPE alone drops. CHAR_LENGTH is the
// declared length for CHAR-semantics columns; DATA_LENGTH is in bytes there.
// The CHAR marker is only legal for VARCHAR2/CHAR (NVARCHAR2/NCHAR take no unit).
const sqlTypeExpr = `c.DATA_TYPE ||
	CASE
		WHEN c.DATA_TYPE IN ('VARCHAR2','NVARCHAR2','CHAR','NCHAR','RAW') THEN
			'(' || CASE WHEN c.CHAR_USED = 'C' THEN c.CHAR_LENGTH ELSE c.DATA_LENGTH END ||
				CASE WHEN c.CHAR_USED = 'C' AND c.DATA_TYPE IN ('VARCHAR2','CHAR')
					THEN ' CHAR' ELSE '' END ||
			')'
		WHEN c.DATA_TYPE = 'NUMBER' AND c.DATA_PRECISION IS NOT NULL THEN
			'(' || c.DATA_PRECISION ||
				CASE WHEN c.DATA_SCALE IS NOT NULL THEN ',' || c.DATA_SCALE ELSE '' END ||
			')'
		WHEN c.DATA_TYPE = 'FLOAT' AND c.DATA_PRECISION IS NOT NULL THEN
			'(' || c.DATA_PRECISION || ')'
		ELSE ''
	END`

func (OracleDialect) CatalogQuery(kind dbcore.CatalogKind, schema, obj string) (string, []dbcore.Value) {
	switch kind {
	case dbcore.CatalogSchemas:
		return `SELECT username FROM all_users ORDER BY username`, nil
	case dbcore.CatalogTables:
		return `SELECT o.OWNER, o.OBJECT_NAME, o.OBJECT_TYPE, NVL(c.COMMENTS, ''),
				NVL(t.NUM_ROWS, -1)
			FROM ALL_OBJECTS o
			LEFT JOIN ALL_TAB_COMMENTS c
				ON c.OWNER = o.OWNER AND c.TABLE_NAME = o.OBJECT_NAME
				AND c.TABLE_TYPE = o.OBJECT_TYPE
			LEFT JOIN ALL_TABLES t
				ON t.OWNER = o.OWNER AND t.TABLE_NAME = o.OBJECT_NAME
			WHERE o.OWNER = :1 AND o.OBJECT_TYPE IN ('TABLE', 'VIEW')
				AND o.OBJECT_NAME NOT LIKE 'BIN$%'
			ORDER BY o.OBJECT_NAME`,
			[]dbcore.Value{{Kind: dbcore.ValText, S: schema}}
	case dbcore.CatalogColumns:
		return `SELECT c.COLUMN_NAME, ` + sqlTypeExpr + `, c.NULLABLE, c.DATA_DEFAULT,
				CASE WHEN pk.COLUMN_NAME IS NULL THEN 0 ELSE 1 END,
				NVL(cm.COMMENTS, ''),
				c.COLUMN_ID,
				CASE WHEN ic.COLUMN_NAME IS NOT NULL THEN 1 ELSE 0 END
			FROM ALL_TAB_COLUMNS c
			LEFT JOIN (
				SELECT cc.OWNER, cc.TABLE_NAME, cc.COLUMN_NAME
				FROM ALL_CONSTRAINTS k
				INNER JOIN ALL_CONS_COLUMNS cc
					ON cc.OWNER = k.OWNER AND cc.CONSTRAINT_NAME = k.CONSTRAINT_NAME
				WHERE k.CONSTRAINT_TYPE = 'P'
			) pk
				ON pk.OWNER = c.OWNER AND pk.TABLE_NAME = c.TABLE_NAME
				AND pk.COLUMN_NAME = c.COLUMN_NAME
			LEFT JOIN ALL_COL_COMMENTS cm
				ON cm.OWNER = c.OWNER AND cm.TABLE_NAME = c.TABLE_NAME
				AND cm.COLUMN_NAME = c.COLUMN_NAME
			LEFT JOIN ALL_TAB_IDENTITY_COLS ic
				ON ic.OWNER = c.OWNER AND ic.TABLE_NAME = c.TABLE_NAME
				AND ic.COLUMN_NAME = c.COLUMN_NAME
			WHERE c.OWNER = :1 AND c.TABLE_NAME = :2
			ORDER BY c.COLUMN_ID`,
			[]dbcore.Value{{Kind: dbcore.ValText, S: schema}, {Kind: dbcore.ValText, S: obj}}
	case dbcore.CatalogIndexes:
		// ALL_IND_EXPRESSIONS is a LONG column: it must be selected bare (no
		// COALESCE/NVL), and NULL for a plain index column. A function-based
		// index keys a hidden SYS_NC column, so the expression is the real name.
		return `SELECT i.INDEX_NAME,
				CASE WHEN i.UNIQUENESS = 'UNIQUE' THEN 1 ELSE 0 END,
				c.COLUMN_NAME,
				CASE WHEN k.CONSTRAINT_TYPE = 'P' THEN 'primary'
					WHEN k.CONSTRAINT_TYPE = 'U' THEN 'unique'
					WHEN i.UNIQUENESS = 'UNIQUE' THEN 'unique' ELSE 'index' END,
				CASE WHEN k.CONSTRAINT_TYPE = 'P' THEN 1 ELSE 0 END,
				i.INDEX_TYPE,
				e.COLUMN_EXPRESSION,
				c.DESCEND
			FROM ALL_INDEXES i
			INNER JOIN ALL_IND_COLUMNS c
				ON c.INDEX_OWNER = i.OWNER AND c.INDEX_NAME = i.INDEX_NAME
			LEFT JOIN ALL_IND_EXPRESSIONS e
				ON e.INDEX_OWNER = i.OWNER AND e.INDEX_NAME = i.INDEX_NAME
				AND e.COLUMN_POSITION = c.COLUMN_POSITION
			LEFT JOIN ALL_CONSTRAINTS k
				ON k.OWNER = i.TABLE_OWNER AND k.TABLE_NAME = i.TABLE_NAME
				AND k.INDEX_NAME = i.INDEX_NAME
				AND k.CONSTRAINT_TYPE IN ('P', 'U')
			WHERE i.TABLE_OWNER = :1 AND i.TABLE_NAME = :2 AND c.COLUMN_NAME IS NOT NULL
			ORDER BY i.INDEX_NAME, c.COLUMN_POSITION`,
			[]dbcore.Value{{Kind: dbcore.ValText, S: schema}, {Kind: dbcore.ValText, S: obj}}
	case dbcore.CatalogForeignKeys:
		return `SELECT a.CONSTRAINT_NAME, a.COLUMN_NAME, r.OWNER, r.TABLE_NAME, r.COLUMN_NAME, k.DELETE_RULE
			FROM ALL_CONS_COLUMNS a
			INNER JOIN ALL_CONSTRAINTS k
				ON k.OWNER = a.OWNER AND k.CONSTRAINT_NAME = a.CONSTRAINT_NAME
				AND k.CONSTRAINT_TYPE = 'R'
			INNER JOIN ALL_CONS_COLUMNS r
				ON r.OWNER = k.R_OWNER AND r.CONSTRAINT_NAME = k.R_CONSTRAINT_NAME
				AND r.POSITION = a.POSITION
			WHERE a.OWNER = :1 AND a.TABLE_NAME = :2
			ORDER BY a.CONSTRAINT_NAME, a.POSITION`,
			[]dbcore.Value{{Kind: dbcore.ValText, S: schema}, {Kind: dbcore.ValText, S: obj}}
	case dbcore.CatalogPrimaryKey:
		return `SELECT k.CONSTRAINT_NAME, c.COLUMN_NAME
			FROM ALL_CONSTRAINTS k
			INNER JOIN ALL_CONS_COLUMNS c
				ON c.OWNER = k.OWNER AND c.CONSTRAINT_NAME = k.CONSTRAINT_NAME
			WHERE k.CONSTRAINT_TYPE = 'P' AND k.OWNER = :1 AND k.TABLE_NAME = :2
			ORDER BY c.POSITION`,
			[]dbcore.Value{{Kind: dbcore.ValText, S: schema}, {Kind: dbcore.ValText, S: obj}}
	default:
		return `SELECT username FROM all_users ORDER BY username`, nil
	}
}
