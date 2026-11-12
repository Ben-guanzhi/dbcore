package dm

// The live half: everything that only a DM server can answer.
//
// Skipped unless DBCORE_TEST_DM_DSN is set, like every other engine's live suite.
// No DM instance was reachable while this driver was written, so what is here has
// never run against a server — that is stated in the package comment and in the
// plan document rather than papered over. When an instance appears, this file is
// the checklist: the catalog views, the ROWNUM page shape, and one write.
//
//	export DBCORE_TEST_DM_DSN='dm://SYSDBA:SYSDBA@localhost:5236?schema=SYSDBA'
//	go test ./dm/ -run TestDMLive -v

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
	tables, err := drv.ListTables(ctx, schema)
	if err != nil {
		t.Fatalf("ListTables: %v (does this instance expose ALL_TABLES?)", err)
	}
	_ = tables
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
