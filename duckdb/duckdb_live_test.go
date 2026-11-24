package duckdb

// duckdb_live_test.go — a real DuckDB file. There is no server to skip
// without: the engine IS the file, so these tests run everywhere the package
// builds and always exercise the real engine.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ben-guanzhi/dbcore"
)

// liveDuck opens a fresh on-disk database for one test.
func liveDuck(t *testing.T) (*DuckDBDriver, context.Context) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "probe.duckdb")
	drv, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = drv.Close() })
	return drv, context.Background()
}

// TestDuckLiveCatalogAndBrowsing creates a parent/child pair with a primary
// key, a foreign key and a secondary index — the three things DuckDB has and
// ClickHouse does not — then reads each back through the catalog.
func TestDuckLiveCatalogAndBrowsing(t *testing.T) {
	drv, ctx := liveDuck(t)
	for _, stmt := range []string{
		`CREATE TABLE parent (id INTEGER PRIMARY KEY, name VARCHAR NOT NULL)`,
		`CREATE TABLE child (
			id INTEGER PRIMARY KEY,
			parent_id INTEGER REFERENCES parent(id),
			tag VARCHAR
		)`,
		`CREATE INDEX idx_child_tag ON child(tag)`,
		`INSERT INTO parent VALUES (1,'a'),(2,'b')`,
		`INSERT INTO child VALUES (1,1,'x'),(2,1,'y'),(3,2,'x')`,
	} {
		if _, err := drv.Execute(ctx, stmt); err != nil {
			t.Fatalf("%.50s: %v", stmt, err)
		}
	}

	schemas, err := drv.ListSchemas(ctx)
	if err != nil {
		t.Fatalf("ListSchemas: %v", err)
	}
	var sawMain bool
	for _, s := range schemas {
		if s.Name == "main" {
			sawMain = true
		}
	}
	if !sawMain {
		t.Fatalf("the main schema is missing from %+v", schemas)
	}

	tables, err := drv.ListTables(ctx, "main")
	if err != nil {
		t.Fatalf("ListTables: %v", err)
	}
	if len(tables) != 2 {
		t.Fatalf("tables = %d, want 2 (%+v)", len(tables), tables)
	}

	// The primary key must be flagged on the column.
	cols, err := drv.ListColumns(ctx, "main", "parent")
	if err != nil {
		t.Fatalf("ListColumns: %v", err)
	}
	var sawPK bool
	for _, c := range cols {
		if c.Name == "id" && c.IsPrimaryKey {
			sawPK = true
		}
	}
	if !sawPK {
		t.Fatalf("the primary key is not flagged: %+v", cols)
	}

	// The foreign key is a real answer here, not a refusal.
	fks, err := drv.ListForeignKeys(ctx, "main", "child")
	if err != nil {
		t.Fatalf("ListForeignKeys: %v", err)
	}
	if len(fks) != 1 {
		t.Fatalf("foreign keys = %d, want 1 (%+v)", len(fks), fks)
	}
	fk := fks[0]
	if fk.RefTable != "parent" || len(fk.Columns) != 1 || fk.Columns[0] != "parent_id" {
		t.Fatalf("foreign key = %+v", fk)
	}
	if len(fk.RefColumns) != 1 || fk.RefColumns[0] != "id" {
		t.Fatalf("referenced columns = %+v", fk.RefColumns)
	}

	// The index is reported with its columns lifted out of the DDL.
	idx, err := drv.ListIndexes(ctx, "main", "child")
	if err != nil {
		t.Fatalf("ListIndexes: %v", err)
	}
	var sawIdx bool
	for _, ix := range idx {
		if ix.Name == "idx_child_tag" && len(ix.Columns) == 1 && ix.Columns[0] == "tag" {
			sawIdx = true
		}
	}
	if !sawIdx {
		t.Fatalf("idx_child_tag is missing or has no columns: %+v", idx)
	}

	// Paging and the count agree.
	page, err := drv.BrowseRows(ctx, "main", "child", &dbcore.TableRowsRequest{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("BrowseRows: %v", err)
	}
	if len(page.Rows) != 2 {
		t.Fatalf("page rows = %d, want 2", len(page.Rows))
	}
	if !page.TotalFound || page.Total != 3 {
		t.Fatalf("total = %d (found=%v), want 3", page.Total, page.TotalFound)
	}
	n, err := drv.BrowseRowsCount(ctx, "main", "child", &dbcore.TableRowsRequest{})
	if err != nil || n != 3 {
		t.Fatalf("count = %d, %v", n, err)
	}

	// A filter narrows both.
	f := &dbcore.TableRowsRequest{Page: 1, PageSize: 10, Filter: []dbcore.FilterExpr{
		{Column: "tag", Op: dbcore.FilterEq, Value: dbcore.Value{Kind: dbcore.ValText, S: "x"}},
	}}
	fp, err := drv.BrowseRows(ctx, "main", "child", f)
	if err != nil {
		t.Fatalf("filtered browse: %v", err)
	}
	if len(fp.Rows) != 2 || fp.Total != 2 {
		t.Fatalf("filtered = %d rows, total %d, want 2/2", len(fp.Rows), fp.Total)
	}

	// The CREATE renders with the key clause.
	ddl, err := drv.CreateTableSQL(ctx, "main", "parent")
	if err != nil {
		t.Fatalf("CreateTableSQL: %v", err)
	}
	for _, want := range []string{"CREATE TABLE", `"parent"`, "PRIMARY KEY"} {
		if !strings.Contains(ddl, want) {
			t.Errorf("DDL is missing %q:\n%s", want, ddl)
		}
	}
}

// TestDuckLiveInMemory covers the in-memory form, which is how a scratch
// database is opened.
func TestDuckLiveInMemory(t *testing.T) {
	drv, err := Open("")
	if err != nil {
		t.Fatalf("open in-memory: %v", err)
	}
	defer drv.Close()
	ctx := context.Background()
	if _, err := drv.Execute(ctx, "CREATE TABLE t (a INTEGER)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := drv.Execute(ctx, "INSERT INTO t VALUES (42)"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	res, err := drv.Execute(ctx, "SELECT a FROM t")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %d", len(res.Rows))
	}
	if v, ok := dbcore.IntFrom(res.Rows[0][0]); !ok || v != 42 {
		t.Fatalf("value = %+v", res.Rows[0][0])
	}
}
