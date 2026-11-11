// Package sqlite implements the dbcore SQLDriver/SQLTxDriver for SQLite via
// modernc.org/sqlite (CGO-free). It is the reference dialect for the contract.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/dbcore/dbcore"
	_ "modernc.org/sqlite" // registers the "sqlite" driver with database/sql
)

// DriverName is the modernc.org/sqlite registered driver name.
const DriverName = "sqlite"

// SQLiteDriver is a dbcore.SQLTxDriver backed by a *sql.DB opened with modernc sqlite.
type SQLiteDriver struct {
	*dbcore.SessionCore
	db      *sql.DB
	dialect dbcore.Dialect
	owned   bool
}

// NewFromDB wraps an existing *sql.DB without taking ownership: Close becomes a
// no-op so callers such as client pools keep managing the handle.
func NewFromDB(db *sql.DB) *SQLiteDriver {
	d0 := &SQLiteDriver{db: db, dialect: &SQLiteDialect{}}
	d0.initWide()
	return d0
}

// Open opens (or creates) a SQLite database at dsn (e.g. ":memory:" or a file path).
func Open(dsn string) (*SQLiteDriver, error) {
	db, err := sql.Open(DriverName, dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	d0 := &SQLiteDriver{db: db, dialect: &SQLiteDialect{}, owned: true}
	d0.initWide()
	return d0, nil
}

func (d *SQLiteDriver) Kind() dbcore.DatabaseKind { return dbcore.KindSQLite }
func (d *SQLiteDriver) Close() error {
	if !d.owned {
		return nil
	}
	return d.db.Close()
}
func (d *SQLiteDriver) Ping(ctx context.Context) error { return d.db.PingContext(ctx) }

func (d *SQLiteDriver) AsSQL() (dbcore.SQLDriver, bool) { return d, true }
func (d *SQLiteDriver) AsKV() (dbcore.KVDriver, bool)   { return nil, false }

// Execute dispatches SELECT-like statements to QueryContext and DML to ExecContext.
func (d *SQLiteDriver) Execute(ctx context.Context, query string, args ...dbcore.Value) (*dbcore.QueryResult, error) {
	if dbcore.IsReadQuery(query) {
		return dbcore.RunQuery(ctx, d.db, query, args)
	}
	affected, lastID, err := dbcore.RunExec(ctx, d.db, query, args)
	if err != nil {
		return nil, err
	}
	return &dbcore.QueryResult{AffectedRows: affected, LastInsertID: lastID}, nil
}

func (d *SQLiteDriver) Begin(ctx context.Context, mode dbcore.TxMode) (*sql.Tx, error) {
	return d.db.BeginTx(ctx, dbcore.TxOptionsFromMode(mode))
}

func (d *SQLiteDriver) ExecuteIn(exec dbcore.SQLTx, ctx context.Context, query string, args ...dbcore.Value) (*dbcore.QueryResult, error) {
	if dbcore.IsReadQuery(query) {
		return dbcore.RunQuery(ctx, exec, query, args)
	}
	affected, lastID, err := dbcore.RunExec(ctx, exec, query, args)
	if err != nil {
		return nil, err
	}
	return &dbcore.QueryResult{AffectedRows: affected, LastInsertID: lastID}, nil
}

func (d *SQLiteDriver) ListSchemas(ctx context.Context) ([]dbcore.SchemaInfo, error) {
	res, err := dbcore.RunQuery(ctx, d.db, `SELECT name FROM pragma_database_list()`, nil)
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.SchemaInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		out = append(out, dbcore.SchemaInfo{Name: r[0].S})
	}
	return out, nil
}

func (d *SQLiteDriver) ListTables(ctx context.Context, schema string) ([]dbcore.TableInfo, error) {
	q, a := d.dialect.CatalogQuery(dbcore.CatalogTables, schema, "")
	res, err := dbcore.RunQuery(ctx, d.db, q, a)
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.TableInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		out = append(out, dbcore.TableInfo{Schema: schema, Name: r[0].S, Type: r[1].S, Rows: -1})
	}
	return out, nil
}

func (d *SQLiteDriver) ListColumns(ctx context.Context, schema, table string) ([]dbcore.ColumnInfo, error) {
	q, a := d.dialect.CatalogQuery(dbcore.CatalogColumns, schema, table)
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
			Nullable:     r[2].I == 0,
			IsPrimaryKey: r[4].I > 0, // pragma_table_info.pk: 0 = not in the PK, n = PK position
			// r[5] = cid + 1. SQLite has no per-column auto-increment flag in the
			// pragma; INTEGER PRIMARY KEY rowid aliases stay IsAutoIncrement=false.
			OrdinalPosition: int32(r[5].I),
		}
		if r[3].Kind != dbcore.ValNull {
			s := r[3].S
			ci.Default = &s
		}
		out = append(out, ci)
	}
	return out, nil
}

// BatchColumns reads every table's and view's columns in one round trip
// (BatchColumnsReader). Row shape = CatalogColumns with the table name
// prepended, so the per-column mapping is the ListColumns mapping shifted by
// one position.
func (d *SQLiteDriver) BatchColumns(ctx context.Context, schema string) ([]dbcore.ColumnInfo, error) {
	q, _ := d.dialect.CatalogQuery(dbcore.CatalogBatchColumns, schema, "")
	res, err := dbcore.RunQuery(ctx, d.db, q, nil)
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.ColumnInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		ci := dbcore.ColumnInfo{
			Table:        r[0].S,
			Name:         r[1].S,
			Type:         r[2].S,
			Nullable:     r[3].I == 0,
			IsPrimaryKey: r[5].I > 0, // pragma_table_info.pk: 0 = not in the PK, n = PK position
			// r[6] = cid + 1; see ListColumns for the auto-increment note.
			OrdinalPosition: int32(r[6].I),
		}
		if r[4].Kind != dbcore.ValNull {
			s := r[4].S
			ci.Default = &s
		}
		out = append(out, ci)
	}
	return out, nil
}

func (d *SQLiteDriver) ListIndexes(ctx context.Context, schema, table string) ([]dbcore.IndexInfo, error) {
	// The index list and the stored CREATE INDEX text come from one query so a
	// partial/expression index can be described without a second lookup.
	q := `SELECT i.name, i."unique", i.origin, COALESCE(m.sql, '')
		FROM pragma_index_list(?) i
		LEFT JOIN sqlite_master m ON m.type = 'index' AND m.name = i.name`
	res, err := dbcore.RunQuery(ctx, d.db, q, []dbcore.Value{{Kind: dbcore.ValText, S: table}})
	if err != nil {
		// pragma_index_list 对视图/不存在的表返回空结果集而非报错——
		// 空结果路径已经覆盖了这些"无索引"场景。走到这里只可能是真实
		// 错误（权限拒绝、数据库锁定、表在列举后被删除等），必须传播：
		// 吞成 nil,nil 会让 schemadiff 把"查不到索引"当成"该删索引"，
		// 静默产出 DROP 计划。与 MySQL/Postgres/MSSQL/Oracle 驱动一致。
		return nil, err
	}
	out := make([]dbcore.IndexInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		name := r[0].S
		// Implicit indexes backing UNIQUE/PRIMARY KEY constraints are already
		// represented by the constraint itself.
		if strings.HasPrefix(name, "sqlite_autoindex") {
			continue
		}
		origin := r[2].S
		kind := dbcore.IndexKindIndex
		switch {
		case origin == "pk":
			kind = dbcore.IndexKindPrimary
		case r[1].I != 0: // a UNIQUE index may come from a constraint (u) or CREATE UNIQUE INDEX (c)
			kind = dbcore.IndexKindUnique
		}
		idx := dbcore.IndexInfo{
			Name:    name,
			Kind:    kind,
			Unique:  r[1].I != 0,
			Primary: origin == "pk",
			Method:  "btree", // SQLite builds B-tree indexes only
		}
		// pragma_index_xinfo supplies the key order and DESC flags and marks an
		// expression part with a NULL name; the expression text and a partial
		// index's WHERE clause exist only in the CREATE INDEX statement.
		parts, predicate, parsed := splitCreateIndex(r[3].S)
		xi, cerr := dbcore.RunQuery(ctx, d.db,
			`SELECT cid, name, "desc" FROM pragma_index_xinfo(?) ORDER BY seqno`,
			[]dbcore.Value{{Kind: dbcore.ValText, S: name}})
		if cerr == nil {
			part := 0
			for _, xr := range xi.Rows {
				if xr[0].I == -1 { // auxiliary rowid column, not a key part
					continue
				}
				ip := dbcore.IndexPart{Desc: xr[2].I != 0}
				if col := xr[1].S; col != "" {
					ip.Column = col
				} else if parsed && part < len(parts) {
					ip.Expression = stripIndexDirection(parts[part])
				}
				part++
				idx.Parts = append(idx.Parts, ip)
				if ip.Column != "" {
					idx.Columns = append(idx.Columns, ip.Column)
				}
			}
		}
		if parsed {
			idx.Predicate = predicate
		}
		out = append(out, idx)
	}
	return out, nil
}

func (d *SQLiteDriver) ListForeignKeys(ctx context.Context, schema, table string) ([]dbcore.ForeignKeyInfo, error) {
	q, a := d.dialect.CatalogQuery(dbcore.CatalogForeignKeys, schema, table)
	res, err := dbcore.RunQuery(ctx, d.db, q, a)
	if err != nil {
		// 传播而非吞成"无外键"——pragma_foreign_key_list 对无外键的表返回
		// 空结果（非错误），只有真实 SQL 错误（权限、连接、表名损坏）才走到
		// 这里。吞掉后调用方误以为表没有外键，schemadiff 生成错误的 DROP FK
		// 计划。BatchForeignKeys（第 280 行）和其它驱动都传播错误。
		return nil, err
	}
	// pragma_foreign_key_list answers one row per column pair under a shared id;
	// SQLite stores no constraint name, so the id becomes a stable synthetic one.
	out := make([]dbcore.ForeignKeyInfo, 0, len(res.Rows))
	index := make(map[int64]int, len(res.Rows))
	for _, r := range res.Rows {
		id := r[0].I
		if i, ok := index[id]; ok {
			out[i].Columns = append(out[i].Columns, r[2].S)
			out[i].RefColumns = append(out[i].RefColumns, r[3].S)
			continue
		}
		index[id] = len(out)
		out = append(out, dbcore.ForeignKeyInfo{
			Name:       fmt.Sprintf("fk_%d", id),
			Columns:    []string{r[2].S},
			RefTable:   r[1].S,
			RefColumns: []string{r[3].S},
			OnUpdate:   dbcore.NormalizeFKAction(r[4].S),
			OnDelete:   dbcore.NormalizeFKAction(r[5].S),
		})
	}
	return out, nil
}

// BatchForeignKeys reads every table's foreign keys in one round trip
// (BatchForeignKeysReader). Row shape = CatalogForeignKeys with the table
// name prepended; the per-(table, id) grouping mirrors ListForeignKeys, and
// the synthetic name namespaces the id per table so the two paths agree.
func (d *SQLiteDriver) BatchForeignKeys(ctx context.Context, schema string) ([]dbcore.TableForeignKeys, error) {
	q, _ := d.dialect.CatalogQuery(dbcore.CatalogBatchForeignKeys, schema, "")
	res, err := dbcore.RunQuery(ctx, d.db, q, nil)
	if err != nil {
		return nil, err
	}
	// Rows arrive ordered by (table, id, seq): one row per column pair under
	// a shared (table, id) pair. A map would need a second sort to restore
	// the row order; an append-only walk of the ordering keeps both stable.
	var out []dbcore.TableForeignKeys
	index := make(map[[2]any]int, len(res.Rows))
	for _, r := range res.Rows {
		table := r[0].S
		key := [2]any{table, r[1].I}
		i, ok := index[key]
		if !ok {
			// New table → new group; new (table, id) → new FK in that group.
			if len(out) == 0 || out[len(out)-1].Table != table {
				out = append(out, dbcore.TableForeignKeys{Table: table})
			}
			cur := &out[len(out)-1]
			cur.ForeignKeys = append(cur.ForeignKeys, dbcore.ForeignKeyInfo{
				Name:       fmt.Sprintf("fk_%s_%d", table, r[1].I),
				Columns:    []string{r[3].S},
				RefTable:   r[2].S,
				RefColumns: []string{r[4].S},
				OnUpdate:   dbcore.NormalizeFKAction(r[5].S),
				OnDelete:   dbcore.NormalizeFKAction(r[6].S),
			})
			index[key] = len(cur.ForeignKeys) - 1
			continue
		}
		cur := &out[len(out)-1]
		cur.ForeignKeys[i].Columns = append(cur.ForeignKeys[i].Columns, r[3].S)
		cur.ForeignKeys[i].RefColumns = append(cur.ForeignKeys[i].RefColumns, r[4].S)
	}
	return out, nil
}

func (d *SQLiteDriver) CreateTableSQL(ctx context.Context, schema, table string) (string, error) {
	q := fmt.Sprintf("SELECT sql FROM sqlite_master WHERE type='table' AND name=%s", d.dialect.Placeholder(1))
	res, err := dbcore.RunQuery(ctx, d.db, q, []dbcore.Value{{Kind: dbcore.ValText, S: table}})
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 {
		return "", fmt.Errorf("table %q not found", table)
	}
	return res.Rows[0][0].S, nil
}

// resolveSchema maps an empty schema to SQLite's default ("main"): the
// row-browse path used to quote the empty name into `""."table"`, which
// is not a valid table reference. Engines with a live "current schema"
// resolve through a query; SQLite's is static.
func (d *SQLiteDriver) resolveSchema(schema string) string {
	if strings.TrimSpace(schema) == "" {
		return "main"
	}
	return schema
}

func (d *SQLiteDriver) BrowseRows(ctx context.Context, schema, table string, req *dbcore.TableRowsRequest) (*dbcore.TableRowsResult, error) {
	schema = d.resolveSchema(schema)
	q, a := d.dialect.BuildRowsQuery(schema, table, req)
	res, err := dbcore.RunQuery(ctx, d.db, q, a)
	if err != nil {
		return nil, err
	}
	out := &dbcore.TableRowsResult{Columns: res.Columns, Rows: res.Rows}
	cq, ca := d.dialect.BuildCountQuery(schema, table, req)
	if cres, cerr := dbcore.RunQuery(ctx, d.db, cq, ca); cerr == nil && len(cres.Rows) > 0 {
		// Kind-aware: modernc may deliver the count as int64 or text —
		// reading .I blind answered 0 for the text shape.
		if total, ok := dbcore.UintFromValue(cres.Rows[0][0]); ok {
			out.Total = total
			out.TotalFound = true
		}
	}
	return out, nil
}

func (d *SQLiteDriver) BrowseRowsCount(ctx context.Context, schema, table string, req *dbcore.TableRowsRequest) (uint64, error) {
	schema = d.resolveSchema(schema)
	q, a := d.dialect.BuildCountQuery(schema, table, req)
	res, err := dbcore.RunQuery(ctx, d.db, q, a)
	if err != nil {
		return 0, err
	}
	if len(res.Rows) == 0 {
		return 0, nil
	}
	if n, ok := dbcore.UintFromValue(res.Rows[0][0]); ok {
		return n, nil
	}
	return 0, fmt.Errorf("count query returned a non-integer value")
}

// SQLiteDialect implements dbcore.Dialect for SQLite.
type SQLiteDialect struct{}

func (SQLiteDialect) Kind() dbcore.DatabaseKind { return dbcore.KindSQLite }

func (SQLiteDialect) QuoteIdent(parts ...string) string {
	q := make([]string, len(parts))
	for i, p := range parts {
		q[i] = `"` + strings.ReplaceAll(p, `"`, `""`) + `"`
	}
	return strings.Join(q, ".")
}

func (SQLiteDialect) Placeholder(idx int) string { return "?" }

func (SQLiteDialect) TypeName(col dbcore.ColumnInfo) string { return col.Type }

func (SQLiteDialect) LimitOffset(limit, offset int) string {
	if limit <= 0 {
		return ""
	}
	if offset <= 0 {
		return fmt.Sprintf("LIMIT %d", limit)
	}
	return fmt.Sprintf("LIMIT %d OFFSET %d", limit, offset)
}

func (l SQLiteDialect) BuildRowsQuery(schema, table string, req *dbcore.TableRowsRequest) (string, []dbcore.Value) {
	if req == nil {
		req = &dbcore.TableRowsRequest{}
	}
	var b strings.Builder
	b.WriteString("SELECT * FROM ")
	b.WriteString(l.QuoteIdent(schema, table))
	args := l.appendWhere(&b, req)
	args = append(args, dbcore.AppendKeyset(&b, l, req, len(args))...)
	dbcore.AppendSortLimit(&b, l, req)
	return b.String(), args
}

func (l SQLiteDialect) BuildCountQuery(schema, table string, req *dbcore.TableRowsRequest) (string, []dbcore.Value) {
	if req == nil {
		req = &dbcore.TableRowsRequest{}
	}
	var b strings.Builder
	b.WriteString("SELECT COUNT(*) FROM ")
	b.WriteString(l.QuoteIdent(schema, table))
	args := l.appendWhere(&b, req)
	return b.String(), args
}

func (l SQLiteDialect) appendWhere(b *strings.Builder, req *dbcore.TableRowsRequest) []dbcore.Value {
	if len(req.Filter) == 0 {
		return nil
	}
	b.WriteString(" WHERE ")
	args := make([]dbcore.Value, 0, len(req.Filter))
	for i, f := range req.Filter {
		if i > 0 {
			b.WriteString(" AND ")
		}
		if f.Op == dbcore.FilterIsNull || f.Op == dbcore.FilterIsNotNull {
			b.WriteString(l.QuoteIdent(f.Column))
			b.WriteString(" ")
			b.WriteString(string(f.Op))
			continue
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

func (l SQLiteDialect) CatalogQuery(kind dbcore.CatalogKind, schema, obj string) (string, []dbcore.Value) {
	switch kind {
	case dbcore.CatalogTables:
		return `SELECT name, type FROM sqlite_master WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%'`, nil
	case dbcore.CatalogColumns:
		// pragma_table_info is a table-valued function; the table name is a BOUND parameter (P0-4 safe).
		return `SELECT name, type, "notnull", dflt_value, pk, cid + 1 FROM pragma_table_info(?)`, []dbcore.Value{{Kind: dbcore.ValText, S: obj}}
	case dbcore.CatalogBatchColumns:
		// CatalogColumns with the table name prepended, one round trip for the
		// whole schema. Views included: CatalogTables lists them and
		// pragma_table_info works on them, so batch must cover the same names.
		// The LIKE filter mirrors CatalogTables verbatim (no ESCAPE) so the two
		// paths' name sets can never diverge — drivertest catalog_batch holds
		// them side by side.
		return `SELECT t.name, c.name, c.type, c."notnull", c.dflt_value, c.pk, c.cid + 1
			FROM sqlite_master t
			JOIN pragma_table_info(t.name) c
			WHERE t.type IN ('table','view') AND t.name NOT LIKE 'sqlite_%'
			ORDER BY t.name, c.cid`, nil
	case dbcore.CatalogIndexes:
		return `SELECT name, "unique", origin FROM pragma_index_list(?)`, []dbcore.Value{{Kind: dbcore.ValText, S: obj}}
	case dbcore.CatalogForeignKeys:
		return `SELECT id, "table", "from", "to", on_update, on_delete FROM pragma_foreign_key_list(?) ORDER BY id, seq`,
			[]dbcore.Value{{Kind: dbcore.ValText, S: obj}}
	case dbcore.CatalogBatchForeignKeys:
		// CatalogForeignKeys with the table name prepended, one round trip for
		// the whole schema. Only base tables: views cannot carry foreign keys,
		// and pragma_foreign_key_list on a view would error the join. The
		// grouping key is (t.name, fkl.id) — the synthetic fk_<table>_<id>
		// name keeps the two paths' names 1:1.
		return `SELECT t.name, fkl.id, fkl."table", fkl."from", fkl."to", fkl.on_update, fkl.on_delete
			FROM sqlite_master t
			JOIN pragma_foreign_key_list(t.name) fkl ON true
			WHERE t.type = 'table' AND t.name NOT LIKE 'sqlite_%'
			ORDER BY t.name, fkl.id, fkl.seq`, nil
	default:
		return `SELECT name FROM pragma_database_list()`, nil
	}
}
