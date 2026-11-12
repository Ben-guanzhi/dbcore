package dm

// The live half: everything that only a DM server can answer.
//
// Skipped unless DBCORE_TEST_DM_DSN is set, like every other engine's live suite.
// Start an instance with dm8-up.sh and run:
//
//	export DBCORE_TEST_DM_DSN='dm://SYSDBA:SYSDBA001@localhost:5236?schema=SYSDBA'
//	go test ./dm/ -run TestDMLive -v
//
// What these tests settled on a real DM8 (20241022 build):
//
//   - the Oracle-compatible dictionary views ARE there (ALL_USERS / ALL_TABLES /
//     ALL_TAB_COLUMNS / ALL_INDEXES / ALL_CONSTRAINTS), so oracleRecipe holds;
//   - ":n" binds work as written (sqltext.DM assumes it);
//   - writes MUST go through ExecContext: DM answers a DDL sent as a query with
//     "Error 9005: 非查询SQL语句" (caught here, fixed in Execute).

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/dbcore/dbcore"
)

func liveDM(t *testing.T) (dbcore.SQLDriver, context.Context, string) {
	t.Helper()
	dsn := os.Getenv("DBCORE_TEST_DM_DSN")
	if dsn == "" {
		t.Skip("set DBCORE_TEST_DM_DSN to run the DM live tests")
	}
	ctx := context.Background()
	drv, err := Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = drv.Close() })
	sqlDrv, ok := drv.AsSQL()
	if !ok {
		t.Fatal("the dm driver must speak SQL")
	}
	schema := strings.ToUpper(strings.TrimSpace(os.Getenv("DBCORE_TEST_DM_SCHEMA")))
	if schema == "" {
		schema = "SYSDBA"
	}
	return sqlDrv, ctx, schema
}

func TestDMLiveCatalog(t *testing.T) {
	drv, ctx, schema := liveDM(t)
	schemas, err := drv.ListSchemas(ctx)
	if err != nil {
		t.Fatalf("ListSchemas: %v", err)
	}
	if len(schemas) == 0 {
		t.Fatal("no schemas: the ALL_USERS view is not answering as expected")
	}
	var sawSYSDBA bool
	for _, s := range schemas {
		if strings.EqualFold(s.Name, "SYSDBA") {
			sawSYSDBA = true
		}
	}
	if !sawSYSDBA {
		t.Fatalf("the SYSDBA schema is missing from %d schemas", len(schemas))
	}
	if _, err := drv.ListTables(ctx, schema); err != nil {
		t.Fatalf("ListTables: %v (does this instance expose ALL_TABLES?)", err)
	}
}

func TestDMLiveRoundTrip(t *testing.T) {
	drv, ctx, schema := liveDM(t)
	const table = "TPG_DM_LIVE"
	_, _ = drv.Execute(ctx, "DROP TABLE "+schema+"."+table)
	if _, err := drv.Execute(ctx, "CREATE TABLE "+schema+"."+table+" (id INT PRIMARY KEY, name VARCHAR(50))"); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _, _ = drv.Execute(ctx, "DROP TABLE "+schema+"."+table) })

	if _, err := drv.Execute(ctx, "INSERT INTO "+schema+"."+table+" (id, name) VALUES (:1, :2)",
		dbcore.ValueOf(int64(1)), dbcore.ValueOf("alice")); err != nil {
		t.Fatalf("insert with :n binds: %v", err)
	}

	res, err := drv.BrowseRows(ctx, schema, table, &dbcore.TableRowsRequest{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %d", len(res.Rows))
	}
	if !strings.EqualFold(res.Rows[0][1].S, "alice") {
		t.Fatalf("row = %+v", res.Rows[0])
	}
	// The page shape must be ROWNUM-based: DM's Oracle mode has no LIMIT.
	if res.TotalFound && res.Total != 1 {
		t.Fatalf("total = %d, want 1", res.Total)
	}
}

// TestDMLiveObjectMetadata checks the parts of the catalog the sidebar uses: the
// column list, an index, and a foreign key. These read the same Oracle-compatible
// views as ListTables, but each has its own column order — which is exactly what a
// live instance is needed to confirm.
func TestDMLiveObjectMetadata(t *testing.T) {
	drv, ctx, schema := liveDM(t)
	const parent = "TPG_DM_PARENT"
	const child = "TPG_DM_CHILD"
	for _, name := range []string{child, parent} {
		_, _ = drv.Execute(ctx, "DROP TABLE "+schema+"."+name)
	}
	t.Cleanup(func() {
		_, _ = drv.Execute(ctx, "DROP TABLE "+schema+"."+child)
		_, _ = drv.Execute(ctx, "DROP TABLE "+schema+"."+parent)
	})

	for _, stmt := range []string{
		"CREATE TABLE " + schema + "." + parent + " (id INT PRIMARY KEY, label VARCHAR(50) NOT NULL)",
		"CREATE TABLE " + schema + "." + child + " (id INT PRIMARY KEY, parent_id INT, note VARCHAR(20), " +
			"CONSTRAINT fk_tpg_dm FOREIGN KEY (parent_id) REFERENCES " + schema + "." + parent + "(id))",
		"CREATE INDEX idx_tpg_dm_note ON " + schema + "." + child + "(note)",
	} {
		if _, err := drv.Execute(ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	cols, err := drv.ListColumns(ctx, schema, child)
	if err != nil {
		t.Fatalf("ListColumns: %v", err)
	}
	if len(cols) != 3 {
		t.Fatalf("columns = %d (%+v)", len(cols), cols)
	}
	byName := map[string]dbcore.ColumnInfo{}
	for _, c := range cols {
		byName[strings.ToLower(c.Name)] = c
	}
	id, ok := byName["id"]
	if !ok {
		t.Fatalf("the id column is missing: %+v", cols)
	}
	if !id.IsPrimaryKey {
		t.Fatalf("id should be the primary key: %+v", id)
	}
	if id.Nullable {
		t.Fatalf("a primary key must not be nullable: %+v", id)
	}
	if id.OrdinalPosition != 1 {
		t.Fatalf("ordinal position = %d, want 1", id.OrdinalPosition)
	}

	idx, err := drv.ListIndexes(ctx, schema, child)
	if err != nil {
		t.Fatalf("ListIndexes: %v", err)
	}
	var sawNote, sawPK bool
	for _, i := range idx {
		for _, c := range i.Columns {
			if strings.EqualFold(c, "note") {
				sawNote = true
			}
		}
		if i.Primary {
			sawPK = true
		}
	}
	if !sawNote {
		t.Fatalf("the note index is missing: %+v", idx)
	}
	if !sawPK {
		t.Fatalf("the primary-key index is missing: %+v", idx)
	}

	fks, err := drv.ListForeignKeys(ctx, schema, child)
	if err != nil {
		t.Fatalf("ListForeignKeys: %v", err)
	}
	if len(fks) != 1 {
		t.Fatalf("foreign keys = %d (%+v)", len(fks), fks)
	}
	fk := fks[0]
	if !strings.EqualFold(fk.Name, "fk_tpg_dm") {
		t.Fatalf("fk name = %q", fk.Name)
	}
	if !strings.EqualFold(fk.RefTable, parent) {
		t.Fatalf("fk ref table = %q, want %q", fk.RefTable, parent)
	}
	if len(fk.Columns) != 1 || !strings.EqualFold(fk.Columns[0], "parent_id") {
		t.Fatalf("fk columns = %+v", fk.Columns)
	}
	if len(fk.RefColumns) != 1 || !strings.EqualFold(fk.RefColumns[0], "id") {
		t.Fatalf("fk ref columns = %+v", fk.RefColumns)
	}
}

// TestDMLivePagingAndFilter exercises the ROWNUM page shape with more rows than one
// page, plus a filter, because that combination is where an Oracle-style paging
// implementation usually breaks.
func TestDMLivePagingAndFilter(t *testing.T) {
	drv, ctx, schema := liveDM(t)
	const table = "TPG_DM_PAGE"
	_, _ = drv.Execute(ctx, "DROP TABLE "+schema+"."+table)
	if _, err := drv.Execute(ctx, "CREATE TABLE "+schema+"."+table+" (ID INT PRIMARY KEY, BUCKET INT)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _, _ = drv.Execute(ctx, "DROP TABLE "+schema+"."+table) })
	for i := 1; i <= 25; i++ {
		if _, err := drv.Execute(ctx, "INSERT INTO "+schema+"."+table+" (ID, BUCKET) VALUES (:1, :2)",
			dbcore.ValueOf(int64(i)), dbcore.ValueOf(int64(i%2))); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	first, err := drv.BrowseRows(ctx, schema, table, &dbcore.TableRowsRequest{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(first.Rows) != 10 {
		t.Fatalf("page 1 rows = %d", len(first.Rows))
	}
	if !first.TotalFound || first.Total != 25 {
		t.Fatalf("total = %d found=%v, want 25", first.Total, first.TotalFound)
	}
	third, err := drv.BrowseRows(ctx, schema, table, &dbcore.TableRowsRequest{Page: 3, PageSize: 10})
	if err != nil {
		t.Fatalf("page 3: %v", err)
	}
	if len(third.Rows) != 5 {
		t.Fatalf("page 3 rows = %d, want the remaining 5", len(third.Rows))
	}

	// A filter narrows both the page and the count.
	filtered, err := drv.BrowseRows(ctx, schema, table, &dbcore.TableRowsRequest{
		Page: 1, PageSize: 50, Filter:     []dbcore.FilterExpr{{Column: "BUCKET", Op: dbcore.FilterEq, Value: dbcore.ValueOf(int64(0))}},
	})
	if err != nil {
		t.Fatalf("filtered: %v", err)
	}
	if !filtered.TotalFound || filtered.Total != 12 {
		t.Fatalf("filtered total = %d found=%v, want 12", filtered.Total, filtered.TotalFound)
	}
	if len(filtered.Rows) != 12 {
		t.Fatalf("filtered rows = %d", len(filtered.Rows))
	}

	// And Count alone must agree with BrowseRows' total.
	n, err := drv.BrowseRowsCount(ctx, schema, table, &dbcore.TableRowsRequest{Filter: []dbcore.FilterExpr{{Column: "BUCKET", Op: dbcore.FilterEq, Value: dbcore.ValueOf(int64(1))}}})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 13 {
		t.Fatalf("count = %d, want 13", n)
	}
}

// TestDMLiveCreateTableSQL checks the reconstruction path: DM has no
// DBMS_METADATA, so the script is always rebuilt from the catalog.
func TestDMLiveCreateTableSQL(t *testing.T) {
	drv, ctx, schema := liveDM(t)
	const table = "TPG_DM_DDL"
	_, _ = drv.Execute(ctx, "DROP TABLE "+schema+"."+table)
	if _, err := drv.Execute(ctx, "CREATE TABLE "+schema+"."+table+" (id INT PRIMARY KEY, label VARCHAR(30))"); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _, _ = drv.Execute(ctx, "DROP TABLE "+schema+"."+table) })

	ddl, err := drv.CreateTableSQL(ctx, schema, table)
	if err != nil {
		t.Fatalf("CreateTableSQL: %v", err)
	}
	upper := strings.ToUpper(ddl)
	if !strings.Contains(upper, "CREATE TABLE") {
		t.Fatalf("no CREATE TABLE in %q", ddl)
	}
	// Quoted identifier, the real column names, and the primary key restored.
	if !strings.Contains(ddl, `"ID"`) && !strings.Contains(ddl, `"id"`) {
		t.Fatalf("the id column is missing from %q", ddl)
	}
	if !strings.Contains(upper, "PRIMARY KEY") {
		t.Fatalf("the primary key is missing from %q", ddl)
	}
}
