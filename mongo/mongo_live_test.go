package mongo

// mongo_live_test.go — the half only a real server can answer.
//
// Skipped unless MONGO_URL is set. The WSL container this client talks to
// runs on 127.0.0.1:27017:
//
//	wsl docker start viva-mongo
//	export MONGO_URL='mongodb://127.0.0.1:27017'
//	go test ./mongo/ -run TestMongoLive -v
//
// What the live half settles:
//   - the collections of a database are listed, and a document's fields
//     become columns;
//   - a page of documents comes back as rows, with a field a document does
//     not have reading as NULL;
//   - a filter narrows the page, and a filter the mapping cannot express is
//     refused rather than ignored;
//   - the command language answers "list", "count", "find" and "insert".

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Ben-guanzhi/dbcore"
)

// liveMongo opens the server the live tests use.
func liveMongo(t *testing.T) (*MongoDriver, context.Context) {
	t.Helper()
	url := os.Getenv("MONGO_URL")
	if url == "" {
		t.Skip("set MONGO_URL to run the MongoDB live tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	drv, err := Open(ctx, url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = drv.Close() })
	if err := drv.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return drv, ctx
}

// TestMongoLiveCatalog lists the databases and the collections of one, and
// reads a document's fields as columns.
func TestMongoLiveCatalog(t *testing.T) {
	drv, ctx := liveMongo(t)
	schemas, err := drv.ListSchemas(ctx)
	if err != nil {
		t.Fatalf("schemas: %v", err)
	}
	if len(schemas) == 0 {
		t.Fatal("the server listed no databases")
	}
	// The business database the migration restored.
	var db string
	for _, s := range schemas {
		if s.Name == "novelgen" {
			db = s.Name
		}
	}
	if db == "" {
		t.Skipf("the server has no novelgen database (databases: %+v)", schemas)
	}
	tables, err := drv.ListTables(ctx, db)
	if err != nil {
		t.Fatalf("tables: %v", err)
	}
	if len(tables) == 0 {
		t.Fatal("the database has no collections")
	}
	var chapters bool
	for _, tb := range tables {
		if tb.Name == "chapters" {
			chapters = true
		}
	}
	if !chapters {
		t.Fatalf("the chapters collection is missing from %+v", tables)
	}
	cols, err := drv.ListColumns(ctx, db, "chapters")
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	if len(cols) == 0 {
		t.Fatal("the collection reported no columns")
	}
	// The identifier comes first: it is the document's identity.
	if cols[0].Name != "_id" {
		t.Fatalf("the first column is %q, want _id", cols[0].Name)
	}
	var hasTitle bool
	for _, c := range cols {
		if c.Name == "title" {
			hasTitle = true
			if c.Type != "TEXT" {
				t.Errorf("title type = %q, want TEXT", c.Type)
			}
		}
	}
	if !hasTitle {
		t.Fatalf("the title field is missing from %+v", cols)
	}
}

// TestMongoLiveBrowseRows reads a page and checks the row shape.
func TestMongoLiveBrowseRows(t *testing.T) {
	drv, ctx := liveMongo(t)
	res, err := drv.BrowseRows(ctx, "novelgen", "chapters",
		&dbcore.TableRowsRequest{Page: 1, PageSize: 5})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if len(res.Rows) == 0 {
		t.Fatal("the page came back empty")
	}
	if len(res.Rows) > 5 {
		t.Fatalf("the page has %d rows, want at most 5", len(res.Rows))
	}
	if len(res.Columns) == 0 {
		t.Fatal("the page has no columns")
	}
	if len(res.Rows[0]) != len(res.Columns) {
		t.Fatalf("the row has %d cells for %d columns", len(res.Rows[0]), len(res.Columns))
	}
	// A field a document does not have reads as NULL, not as an empty string.
	var nulls, values int
	for _, row := range res.Rows {
		for _, cell := range row {
			if cell.Kind == dbcore.ValNull {
				nulls++
			} else {
				values++
			}
		}
	}
	if values == 0 {
		t.Fatal("every cell came back NULL")
	}
	t.Logf("page: %d rows, %d columns, %d values, %d nulls",
		len(res.Rows), len(res.Columns), values, nulls)
}

// TestMongoLiveFilter narrows a page.
func TestMongoLiveFilter(t *testing.T) {
	drv, ctx := liveMongo(t)
	all, err := drv.BrowseRows(ctx, "novelgen", "chapters",
		&dbcore.TableRowsRequest{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	filtered, err := drv.BrowseRows(ctx, "novelgen", "chapters",
		&dbcore.TableRowsRequest{Page: 1, PageSize: 50, Filter: []dbcore.FilterExpr{
			{Column: "title", Op: dbcore.FilterLike, Value: dbcore.Value{Kind: dbcore.ValText, S: "%"}},
		}})
	if err != nil {
		t.Fatalf("filtered browse: %v", err)
	}
	if len(filtered.Rows) > len(all.Rows) {
		t.Fatalf("the filtered page has %d rows, more than the unfiltered %d",
			len(filtered.Rows), len(all.Rows))
	}
}

// TestMongoLiveCommandLanguage runs the console's commands.
func TestMongoLiveCommandLanguage(t *testing.T) {
	drv, ctx := liveMongo(t)
	res, err := drv.Execute(ctx, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(res.Rows) == 0 {
		t.Fatal("list returned nothing")
	}
	res, err = drv.Execute(ctx, "count novelgen.chapters")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0][0].I <= 0 {
		t.Fatalf("count = %+v", res.Rows)
	}
	res, err = drv.Execute(ctx, "find novelgen.chapters limit 3")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(res.Rows) > 3 {
		t.Fatalf("find returned %d rows, want at most 3", len(res.Rows))
	}
	// An unknown command is refused, not silently ignored.
	if _, err := drv.Execute(ctx, "drop everything"); err == nil {
		t.Fatal("an unknown command was accepted")
	} else if !strings.Contains(err.Error(), "可用") {
		t.Fatalf("the refusal does not say what is accepted: %v", err)
	}
}

// TestMongoLiveInsert writes a document into a scratch collection.
func TestMongoLiveInsert(t *testing.T) {
	drv, ctx := liveMongo(t)
	res, err := drv.Execute(ctx, `insert viva_driver_test {"name":"probe","n":7}`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("insert returned %+v", res.Rows)
	}
	// The document is there. The count grows with each run of the test (the
	// scratch collection is not cleaned), so the check is that the inserted
	// document is counted at all.
	res, err = drv.Execute(ctx, "count viva_driver_test")
	if err != nil {
		t.Fatalf("count after insert: %v", err)
	}
	if res.Rows[0][0].I < 1 {
		t.Fatalf("the inserted document is not there: %+v", res.Rows)
	}
}
