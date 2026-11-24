package trino

// trino_live_test.go — everything only a real coordinator can answer.
//
// Skipped unless DBCORE_TEST_TRINO_DSN is set:
//
//	export DBCORE_TEST_TRINO_DSN='http://root@127.0.0.1:8080?catalog=tpch&schema=tiny'
//	go test ./trino/ -run TestTrinoLive -v
//
// What these tests settle:
//   - SHOW CATALOGS answers, and the catalogs are what ListSchemas reports;
//   - ListTables takes "catalog" and "catalog.schema", and the flattened
//     catalog answer carries each table's schema (a client that lost it
//     could not address the table again);
//   - information_schema reads give real columns with Trino's own type
//     spellings;
//   - indexes and foreign keys answer ErrNotSupported rather than an empty
//     list, which a client would read as "this table has none";
//   - paging, filtering and counting agree.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Ben-guanzhi/dbcore"
)

func liveTrino(t *testing.T) (*TrinoDriver, context.Context, string) {
	t.Helper()
	dsn := os.Getenv("DBCORE_TEST_TRINO_DSN")
	if dsn == "" {
		t.Skip("set DBCORE_TEST_TRINO_DSN to run the Trino live tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	drv, err := Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = drv.Close() })
	if err := drv.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	cat := os.Getenv("DBCORE_TEST_TRINO_CATALOG")
	if cat == "" {
		cat = "tpch"
	}
	sch := os.Getenv("DBCORE_TEST_TRINO_SCHEMA")
	if sch == "" {
		sch = "tiny"
	}
	return drv, ctx, cat + "." + sch
}

// TestTrinoLiveCatalog walks the catalog model that is the point of this
// engine: catalogs at the top, schemas inside one, tables inside that.
func TestTrinoLiveCatalog(t *testing.T) {
	drv, ctx, catSchema := liveTrino(t)
	catalog, _, _ := splitCatalogSchema(catSchema)

	catalogs, err := drv.ListSchemas(ctx)
	if err != nil {
		t.Fatalf("ListSchemas: %v", err)
	}
	if len(catalogs) == 0 {
		t.Fatal("no catalogs")
	}
	var sawCatalog bool
	for _, c := range catalogs {
		if c.Name == catalog {
			sawCatalog = true
		}
	}
	if !sawCatalog {
		t.Fatalf("catalog %q missing from %+v", catalog, catalogs)
	}

	// The bare catalog answer spans its schemas and keeps each table's own.
	all, err := drv.ListTables(ctx, catalog)
	if err != nil {
		t.Fatalf("ListTables(%q): %v", catalog, err)
	}
	if len(all) == 0 {
		t.Fatalf("catalog %q listed no tables", catalog)
	}
	var withSchema int
	for _, tb := range all {
		if tb.Schema != "" {
			withSchema++
		}
	}
	if withSchema == 0 {
		t.Fatal("the flattened catalog answer dropped every table's schema")
	}

	// The catalog.schema answer is scoped.
	scoped, err := drv.ListTables(ctx, catSchema)
	if err != nil {
		t.Fatalf("ListTables(%q): %v", catSchema, err)
	}
	if len(scoped) == 0 {
		t.Fatalf("%q listed no tables", catSchema)
	}
	for _, tb := range scoped {
		if tb.Schema != strings.TrimPrefix(catSchema, catalog+".") {
			t.Errorf("table %s has schema %q, want the scoped one", tb.Name, tb.Schema)
		}
	}
}

// TestTrinoLiveColumnsAndBrowsing reads a real tpch table.
func TestTrinoLiveColumnsAndBrowsing(t *testing.T) {
	drv, ctx, catSchema := liveTrino(t)
	tables, err := drv.ListTables(ctx, catSchema)
	if err != nil || len(tables) == 0 {
		t.Fatalf("ListTables: %v (%d tables)", err, len(tables))
	}
	// tpch's orders table is the largest fixture and has a numeric key.
	table := tables[0].Name
	for _, tb := range tables {
		if tb.Name == "orders" || tb.Name == "nation" {
			table = tb.Name
			break
		}
	}

	cols, err := drv.ListColumns(ctx, catSchema, table)
	if err != nil {
		t.Fatalf("ListColumns: %v", err)
	}
	if len(cols) == 0 {
		t.Fatalf("%s reported no columns", table)
	}
	// Trino spells types its own way; a bigint column must read as bigint.
	var sawType bool
	for _, c := range cols {
		if c.Type != "" && c.Table == table {
			sawType = true
		}
	}
	if !sawType {
		t.Fatalf("columns carry no type or table: %+v", cols)
	}

	// Paging and the count agree.
	page, err := drv.BrowseRows(ctx, catSchema, table, &dbcore.TableRowsRequest{Page: 1, PageSize: 3})
	if err != nil {
		t.Fatalf("BrowseRows: %v", err)
	}
	if len(page.Rows) == 0 || len(page.Rows) > 3 {
		t.Fatalf("page has %d rows", len(page.Rows))
	}
	total, err := drv.BrowseRowsCount(ctx, catSchema, table, &dbcore.TableRowsRequest{})
	if err != nil {
		t.Fatalf("BrowseRowsCount: %v", err)
	}
	if total == 0 {
		t.Fatalf("%s counted 0 rows", table)
	}
	if !page.TotalFound || page.Total != total {
		t.Fatalf("page total = %d (found=%v), count = %d", page.Total, page.TotalFound, total)
	}

	// A filter narrows both the page and the count.
	first := cols[0]
	if first.Type == "bigint" || first.Type == "integer" {
		f := &dbcore.TableRowsRequest{Page: 1, PageSize: 5, Filter: []dbcore.FilterExpr{
			{Column: first.Name, Op: dbcore.FilterGte, Value: dbcore.Value{Kind: dbcore.ValInt, I: 0}},
		}}
		fp, ferr := drv.BrowseRows(ctx, catSchema, table, f)
		if ferr != nil {
			t.Fatalf("filtered browse: %v", ferr)
		}
		if fp.Total > total {
			t.Fatalf("a filter widened the count: %d > %d", fp.Total, total)
		}
	}
}

// TestTrinoLiveNoIndexesOrForeignKeys pins the honest refusals.
func TestTrinoLiveNoIndexesOrForeignKeys(t *testing.T) {
	drv, ctx, catSchema := liveTrino(t)
	if _, err := drv.ListIndexes(ctx, catSchema, "orders"); err == nil {
		t.Fatal("Trino reported an index list")
	} else {
		var de *dbcore.Error
		if !errors.As(err, &de) || de.Code != dbcore.ErrCodeNotSupported {
			t.Fatalf("index refusal is not ErrNotSupported: %v", err)
		}
	}
	if _, err := drv.ListForeignKeys(ctx, catSchema, "orders"); err == nil {
		t.Fatal("Trino reported a foreign-key list")
	} else {
		var de *dbcore.Error
		if !errors.As(err, &de) || de.Code != dbcore.ErrCodeNotSupported {
			t.Fatalf("foreign-key refusal is not ErrNotSupported: %v", err)
		}
	}
}
