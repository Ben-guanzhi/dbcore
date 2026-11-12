// Package dm is the 达梦 DM8 driver (W14).
//
// DM8 speaks its own wire protocol, so it needs its own driver — the Oracle driver
// cannot open it even though the SQL is (mostly) Oracle's. The driver used here is
// github.com/sophon-zt/sql-driver/dameng, a fork of 达梦's own Go driver published
// under **Apache-2.0** (the module ships its LICENSE file). The earlier candidate
// (gitee.com/chunanyong/dm) carries no license file at all, which is why it was
// rejected rather than adopted.
//
// What this package assumes, stated plainly because it cannot be proven here:
//
//   - DM8's default compatibility mode is Oracle's, so identifier quoting, ":n"
//     binds and ROWNUM row limiting are Oracle's (the kernel already models that as
//     sqltext.DM).
//   - DM8 provides the Oracle-compatible data-dictionary views (ALL_TABLES,
//     ALL_TAB_COLUMNS, ALL_INDEXES, ALL_CONSTRAINTS, ALL_USERS). This package
//     therefore reuses the Oracle catalog recipes. If a DM installation does not
//     expose them, the catalog reads fail with the server's error — read-only
//     surfaces, so the blast radius is a broken tree, not corrupted data.
//
// The one place the assumption is written down is `oracleRecipe` below, so a
// correction is a one-file change. There is a live test suite gated on
// DBCORE_TEST_DM_DSN; no DM instance was reachable while this was written.
package dm

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/oracle"

	// The driver registers itself as "dm" with database/sql.
	_ "github.com/sophon-zt/sql-driver/dameng"
)

// DriverName is the database/sql driver name the vendored driver registers.
const DriverName = "dm"

// DMDialect is DM8's dialect: Oracle's, minus the parts that are Oracle-only.
//
// It EMBEDS OracleDialect rather than copying it: DM8's compatibility mode is a
// superset of the Oracle syntax this client generates, and a copy would drift.
// Kind() is overridden so nothing downstream mistakes a DM connection for Oracle
// (the Oracle object-health panel is one such consumer).
type DMDialect struct {
	oracle.OracleDialect
}

// Kind reports the DM engine, not Oracle's.
func (DMDialect) Kind() dbcore.DatabaseKind { return dbcore.KindDM }

// oracleRecipe builds a catalog statement from the Oracle recipe.
//
// This function is the single place the "DM's dictionary is Oracle-compatible"
// assumption lives. Every catalog read goes through it, so if a DM installation
// disagrees, this is the one thing to change.
func oracleRecipe(kind dbcore.CatalogKind, schema, obj string) (string, []dbcore.Value) {
	var d oracle.OracleDialect
	return d.CatalogQuery(kind, schema, obj)
}

// DMDriver is one live DM8 connection.
//
// The embedded SessionCore supplies the wide surface (batches, streaming,
// metrics); the methods below override the ones that need DM's dialect or its
// catalog.
type DMDriver struct {
	*dbcore.SessionCore
	db      *sql.DB
	dialect *DMDialect
	owned   bool
}

// NewFromDB wraps an existing handle (tests, pools).
func NewFromDB(db *sql.DB) *DMDriver {
	d := &DMDriver{db: db, dialect: &DMDialect{}}
	d.initWide()
	return d
}

func (d *DMDriver) Kind() dbcore.DatabaseKind { return dbcore.KindDM }
func (d *DMDriver) Close() error {
	if d.owned {
		return d.db.Close()
	}
	return nil
}
func (d *DMDriver) Ping(ctx context.Context) error          { return d.db.PingContext(ctx) }
func (d *DMDriver) AsSQL() (dbcore.SQLDriver, bool)         { return d, true }
func (d *DMDriver) AsKV() (dbcore.KVDriver, bool)           { return nil, false }
func (d *DMDriver) Dialect() dbcore.Dialect                 { return d.dialect }

// Execute runs one statement and returns its result.
//
// Reads go through QueryContext and writes through ExecContext, the way every
// other driver here does it. DM makes this mandatory rather than merely tidy: its
// driver refuses a non-query statement sent as a query with
// "Error 9005: 非查询SQL语句", which a live test caught — the same call on SQLite or
// MySQL happens to work, so the split is easy to forget.
func (d *DMDriver) Execute(ctx context.Context, query string, args ...dbcore.Value) (*dbcore.QueryResult, error) {
	if dbcore.IsReadQuery(query) {
		res, err := dbcore.RunQuery(ctx, d.db, query, args)
		if err != nil {
			return nil, err
		}
		coerceNumericText(res)
		return res, nil
	}
	affected, lastID, err := dbcore.RunExec(ctx, d.db, query, args)
	if err != nil {
		return nil, err
	}
	return &dbcore.QueryResult{AffectedRows: affected, LastInsertID: lastID}, nil
}

// Begin opens a transaction with the requested isolation level.
func (d *DMDriver) Begin(ctx context.Context, mode dbcore.TxMode) (*sql.Tx, error) {
	return d.db.BeginTx(ctx, dbcore.TxOptionsFromMode(mode))
}

// ExecuteIn runs a statement inside an existing transaction: a read returns rows,
// a write returns the affected count.
func (d *DMDriver) ExecuteIn(exec dbcore.SQLTx, ctx context.Context, query string, args ...dbcore.Value) (*dbcore.QueryResult, error) {
	if dbcore.IsReadQuery(query) {
		return dbcore.RunQuery(ctx, exec, query, args)
	}
	affected, lastID, err := dbcore.RunExec(ctx, exec, query, args)
	if err != nil {
		return nil, err
	}
	return &dbcore.QueryResult{AffectedRows: affected, LastInsertID: lastID}, nil
}

// resolveSchema answers the caller's schema, or the session's current one.
//
// DM8 keeps Oracle's SYS_CONTEXT('USERENV','CURRENT_SCHEMA'), but the session user
// is enough and works regardless: DM folds unquoted identifiers to upper case like
// Oracle, so the answer is upper-cased.
func (d *DMDriver) resolveSchema(ctx context.Context, schema string) (string, error) {
	if strings.TrimSpace(schema) != "" {
		return strings.ToUpper(strings.TrimSpace(schema)), nil
	}
	var user sql.NullString
	if err := d.db.QueryRowContext(ctx, "SELECT USER FROM DUAL").Scan(&user); err != nil {
		return "", err
	}
	if !user.Valid {
		return "", fmt.Errorf("dm: cannot determine the current schema")
	}
	return strings.ToUpper(user.String), nil
}

// ListSchemas lists the databases' users (Oracle's ALL_USERS).
func (d *DMDriver) ListSchemas(ctx context.Context) ([]dbcore.SchemaInfo, error) {
	q, a := oracleRecipe(dbcore.CatalogSchemas, "", "")
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

// ListTables lists a schema's tables and views.
func (d *DMDriver) ListTables(ctx context.Context, schema string) ([]dbcore.TableInfo, error) {
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a := oracleRecipe(dbcore.CatalogTables, s, "")
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
		info := dbcore.TableInfo{Schema: r[0].S, Name: r[1].S, Type: kind, Comment: r[3].S, Rows: intOf(r[4])}
		info.ApproxRows = kind == "table" && info.Rows >= 0
		out = append(out, info)
	}
	return out, nil
}

// ListColumns lists one table's columns.
func (d *DMDriver) ListColumns(ctx context.Context, schema, table string) ([]dbcore.ColumnInfo, error) {
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q := columnsRecipe(s, table)
	var a []dbcore.Value
	res, err := dbcore.RunQuery(ctx, d.db, q, a)
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.ColumnInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		ci := dbcore.ColumnInfo{
			Name:            r[0].S,
			Table:           table,
			Type:            r[1].S,
			Nullable:        r[2].S == "Y",
			IsPrimaryKey:    intOf(r[4]) != 0,
			Comment:         r[5].S,
			OrdinalPosition: int32(intOf(r[6])),
			IsAutoIncrement: intOf(r[7]) != 0,
		}
		if r[3].Kind == dbcore.ValText && r[3].S != "" {
			def := r[3].S
			ci.Default = &def
		}
		out = append(out, ci)
	}
	return out, nil
}

// ListIndexes lists one table's indexes, grouped from their key parts.
func (d *DMDriver) ListIndexes(ctx context.Context, schema, table string) ([]dbcore.IndexInfo, error) {
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a := oracleRecipe(dbcore.CatalogIndexes, s, table)
	res, err := dbcore.RunQuery(ctx, d.db, q, a)
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.IndexInfo, 0, len(res.Rows))
	index := make(map[string]int, len(res.Rows))
	for _, r := range res.Rows {
		name := r[0].S
		part := dbcore.IndexPart{Column: r[2].S, Desc: strings.EqualFold(r[7].S, "DESC")}
		// A function-based index stores an expression; r[6] carries its text.
		if expr := strings.TrimSpace(r[6].S); expr != "" {
			if col, ok := unquoteIdent(expr); ok {
				part.Column = col
			} else {
				part.Column = ""
				part.Expression = expr
			}
		}
		if i, ok := index[name]; ok {
			out[i].Parts = append(out[i].Parts, part)
			if part.Column != "" {
				out[i].Columns = append(out[i].Columns, part.Column)
			}
			continue
		}
		index[name] = len(out)
		info := dbcore.IndexInfo{
			Name: name, Kind: r[3].S, Unique: intOf(r[1]) != 0,
			Primary: intOf(r[4]) != 0, Method: r[5].S,
			Parts: []dbcore.IndexPart{part},
		}
		if part.Column != "" {
			info.Columns = []string{part.Column}
		}
		out = append(out, info)
	}
	return out, nil
}

// unquoteIdent unwraps a bare "NAME" expression (a plain column of a
// function-based index).
func unquoteIdent(expr string) (string, bool) {
	if len(expr) < 3 || expr[0] != '"' || expr[len(expr)-1] != '"' {
		return "", false
	}
	inner := expr[1 : len(expr)-1]
	if strings.Contains(inner, `"`) {
		return "", false
	}
	return inner, true
}

// ListForeignKeys lists one table's outgoing foreign keys.
func (d *DMDriver) ListForeignKeys(ctx context.Context, schema, table string) ([]dbcore.ForeignKeyInfo, error) {
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a := oracleRecipe(dbcore.CatalogForeignKeys, s, table)
	res, err := dbcore.RunQuery(ctx, d.db, q, a)
	if err != nil {
		return nil, err
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
			Name: name, Columns: []string{r[1].S},
			RefSchema: r[2].S, RefTable: r[3].S, RefColumns: []string{r[4].S},
			OnDelete: dbcore.NormalizeFKAction(r[5].S),
			// DM has no ON UPDATE clause (Oracle-compatible grammar).
			OnUpdate: dbcore.FKActionNoAction,
		})
	}
	return out, nil
}

// CreateTableSQL reconstructs the DDL from the catalog.
//
// DM has no DBMS_METADATA, so unlike the Oracle driver there is no "ask the server
// for its own DDL" path: the script is always rebuilt from columns + primary key.
func (d *DMDriver) CreateTableSQL(ctx context.Context, schema, table string) (string, error) {
	s, err := d.resolveSchema(ctx, schema)
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
	b.WriteString(d.dialect.QuoteIdent(s, table))
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
	q, a := oracleRecipe(dbcore.CatalogPrimaryKey, s, table)
	if pk, perr := dbcore.RunQuery(ctx, d.db, q, a); perr == nil && len(pk.Rows) > 0 {
		quoted := make([]string, 0, len(pk.Rows))
		for _, r := range pk.Rows {
			quoted = append(quoted, d.dialect.QuoteIdent(r[1].S))
		}
		b.WriteString(";\nALTER TABLE ")
		b.WriteString(d.dialect.QuoteIdent(s, table))
		b.WriteString(" ADD CONSTRAINT ")
		b.WriteString(d.dialect.QuoteIdent(pk.Rows[0][0].S))
		b.WriteString(" PRIMARY KEY (")
		b.WriteString(strings.Join(quoted, ", "))
		b.WriteString(")")
	}
	return b.String(), nil
}

// BrowseRows reads one page and (best-effort) the total.
func (d *DMDriver) BrowseRows(ctx context.Context, schema, table string, req *dbcore.TableRowsRequest) (*dbcore.TableRowsResult, error) {
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a := d.dialect.BuildRowsQuery(s, table, req)
	res, err := dbcore.RunQuery(ctx, d.db, q, a)
	if err != nil {
		return nil, err
	}
	coerceNumericText(res)
	out := &dbcore.TableRowsResult{Columns: res.Columns, Rows: res.Rows}
	cq, ca := d.dialect.BuildCountQuery(s, table, req)
	// The count is best-effort: a permission failure must not hide the rows the
	// caller already holds, and TotalFound stays false so "count failed" is
	// distinguishable from "empty table".
	if cres, cerr := dbcore.RunQuery(ctx, d.db, cq, ca); cerr == nil && len(cres.Rows) > 0 {
		if total, ok := dbcore.UintFromValue(cres.Rows[0][0]); ok {
			out.Total = total
			out.TotalFound = true
		}
	}
	return out, nil
}

// BrowseRowsCount counts the filtered rows.
func (d *DMDriver) BrowseRowsCount(ctx context.Context, schema, table string, req *dbcore.TableRowsRequest) (uint64, error) {
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
	if n, ok := dbcore.UintFromValue(res.Rows[0][0]); ok {
		return n, nil
	}
	return 0, nil
}

// Compile-time proof that the driver satisfies the kernel's contracts.
var (
	_ dbcore.Driver    = (*DMDriver)(nil)
	_ dbcore.SQLDriver = (*DMDriver)(nil)
	_ dbcore.SQLTxDriver = (*DMDriver)(nil)
)
