package clickhouse

// clickhouse_live_test.go — everything only a real server can answer.
//
// Skipped unless DBCORE_TEST_CLICKHOUSE_DSN is set, like every other engine's
// live suite:
//
//	export DBCORE_TEST_CLICKHOUSE_DSN='clickhouse://default:@127.0.0.1:9000/default'
//	go test ./clickhouse/ -run TestClickHouseLive -v
//
// What these tests settle:
//   - the system.* catalog reads work (databases, tables, columns, keys);
//   - the primary key is reported as the sorting key, and a declared skip
//     index appears as a secondary structure;
//   - the engine answers ErrNotSupported for foreign keys rather than an
//     empty list, which a client would read as "this table has none";
//   - paging, filtering and counting agree with each other.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Ben-guanzhi/dbcore"
)

func liveCH(t *testing.T) (*ClickHouseDriver, context.Context, string) {
	t.Helper()
	dsn := os.Getenv("DBCORE_TEST_CLICKHOUSE_DSN")
	if dsn == "" {
		t.Skip("set DBCORE_TEST_CLICKHOUSE_DSN to run the ClickHouse live tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	drv, err := Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = drv.Close() })
	if err := drv.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	db := os.Getenv("DBCORE_TEST_CLICKHOUSE_DB")
	if db == "" {
		db = "default"
	}
	return drv, ctx, db
}

// TestClickHouseLiveCatalogAndBrowsing creates a table with a sorting key and
// a skip index, then reads it back through every catalog path.
func TestClickHouseLiveCatalogAndBrowsing(t *testing.T) {
	drv, ctx, db := liveCH(t)
	const tbl = "zz_ch_probe"

	// A MergeTree table: the sorting key is the physical order, and the skip
	// index is the engine's only secondary structure.
	for _, stmt := range []string{
		"DROP TABLE IF EXISTS " + db + "." + tbl + " SYNC",
		"CREATE TABLE " + db + "." + tbl + ` (
			id UInt64,
			name String,
			tag String,
			INDEX idx_tag tag TYPE set(16) GRANULARITY 1
		) ENGINE = MergeTree() ORDER BY id COMMENT 'probe table'`,
		"INSERT INTO " + db + "." + tbl + " VALUES (1,'a','x'),(2,'b','y'),(3,'c','x')",
	} {
		if _, err := drv.Execute(ctx, stmt); err != nil {
			t.Fatalf("%.60s: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		_, _ = drv.Execute(context.Background(), "DROP TABLE IF EXISTS "+db+"."+tbl+" SYNC")
	})

	// The database is listed.
	schemas, err := drv.ListSchemas(ctx)
	if err != nil {
		t.Fatalf("ListSchemas: %v", err)
	}
	var sawDB bool
	for _, s := range schemas {
		if s.Name == db {
			sawDB = true
		}
	}
	if !sawDB {
		t.Fatalf("database %q missing from %d schemas", db, len(schemas))
	}

	// The table is listed, with its comment.
	tables, err := drv.ListTables(ctx, db)
	if err != nil {
		t.Fatalf("ListTables: %v", err)
	}
	var found *dbcore.TableInfo
	for i := range tables {
		if tables[i].Name == tbl {
			found = &tables[i]
		}
	}
	if found == nil {
		t.Fatalf("%s missing from %d tables", tbl, len(tables))
	}
	if found.Comment != "probe table" {
		t.Fatalf("comment = %q", found.Comment)
	}

	// The columns keep their declared types verbatim.
	cols, err := drv.ListColumns(ctx, db, tbl)
	if err != nil {
		t.Fatalf("ListColumns: %v", err)
	}
	want := map[string]string{"id": "UInt64", "name": "String", "tag": "String"}
	for _, c := range cols {
		if exp, ok := want[c.Name]; ok && c.Type != exp {
			t.Errorf("column %s type = %q, want %q", c.Name, c.Type, exp)
		}
	}
	if len(cols) != 3 {
		t.Fatalf("columns = %d, want 3", len(cols))
	}

	// The sorting key reads as the primary index; the skip index follows.
	idx, err := drv.ListIndexes(ctx, db, tbl)
	if err != nil {
		t.Fatalf("ListIndexes: %v", err)
	}
	var primary, skip bool
	for _, ix := range idx {
		if ix.Primary && len(ix.Columns) == 1 && ix.Columns[0] == "id" {
			primary = true
		}
		if ix.Name == "idx_tag" {
			skip = true
		}
	}
	if !primary {
		t.Fatalf("the sorting key is not reported as the primary index: %+v", idx)
	}
	if !skip {
		t.Fatalf("the skip index is missing: %+v", idx)
	}

	// Browsing: three rows, and the count agrees.
	page, err := drv.BrowseRows(ctx, db, tbl, &dbcore.TableRowsRequest{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("BrowseRows: %v", err)
	}
	if len(page.Rows) != 2 {
		t.Fatalf("page has %d rows, want 2", len(page.Rows))
	}
	if !page.TotalFound || page.Total != 3 {
		t.Fatalf("total = %d (found=%v), want 3", page.Total, page.TotalFound)
	}
	n, err := drv.BrowseRowsCount(ctx, db, tbl, &dbcore.TableRowsRequest{})
	if err != nil || n != 3 {
		t.Fatalf("BrowseRowsCount = %d, %v", n, err)
	}

	// A filter narrows the page and the count together.
	filtered := &dbcore.TableRowsRequest{Page: 1, PageSize: 10, Filter: []dbcore.FilterExpr{
		{Column: "tag", Op: dbcore.FilterEq, Value: dbcore.Value{Kind: dbcore.ValText, S: "x"}},
	}}
	fp, err := drv.BrowseRows(ctx, db, tbl, filtered)
	if err != nil {
		t.Fatalf("filtered browse: %v", err)
	}
	if len(fp.Rows) != 2 || fp.Total != 2 {
		t.Fatalf("filtered page = %d rows, total %d, want 2/2", len(fp.Rows), fp.Total)
	}
}

// TestClickHouseLiveNoForeignKeys pins the honest refusal: an empty list
// would tell a client the table has no foreign keys, which is true but only
// because the engine has no such concept.
func TestClickHouseLiveNoForeignKeys(t *testing.T) {
	drv, ctx, db := liveCH(t)
	_, err := drv.ListForeignKeys(ctx, db, "nonexistent_table")
	if err == nil {
		t.Fatal("ClickHouse reported a foreign-key list")
	}
	var de *dbcore.Error
	if !errors.As(err, &de) || de.Code != dbcore.ErrCodeNotSupported {
		t.Fatalf("the refusal is not ErrNotSupported: %v", err)
	}
}

// TestClickHouseLiveCreateTableSQL renders a table's CREATE from the catalog.
func TestClickHouseLiveCreateTableSQL(t *testing.T) {
	drv, ctx, db := liveCH(t)
	const tbl = "zz_ch_ddl"
	for _, stmt := range []string{
		"DROP TABLE IF EXISTS " + db + "." + tbl + " SYNC",
		"CREATE TABLE " + db + "." + tbl + " (id UInt64, s String) ENGINE = MergeTree() ORDER BY id",
	} {
		if _, err := drv.Execute(ctx, stmt); err != nil {
			t.Fatalf("%.50s: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		_, _ = drv.Execute(context.Background(), "DROP TABLE IF EXISTS "+db+"."+tbl+" SYNC")
	})
	ddl, err := drv.CreateTableSQL(ctx, db, tbl)
	if err != nil {
		t.Fatalf("CreateTableSQL: %v", err)
	}
	for _, want := range []string{"CREATE TABLE", `"zz_ch_ddl"`, "MergeTree"} {
		if !strings.Contains(ddl, want) {
			t.Errorf("DDL is missing %q:\n%s", want, ddl)
		}
	}
}
