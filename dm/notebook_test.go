package dm

// Notebook expansion, checked on a real DM8.
//
// The kernel's expansion builds `WITH cell_1 AS (…) SELECT … FROM cell_1`. Whether
// DM accepts that shape is an engine question, and DM is the one most likely to
// disagree: it is Oracle-SHAPED, not Oracle. So it runs here rather than in
// sqltext, which cannot import this package without an import cycle.
//
// Skipped unless DBCORE_TEST_DM_DSN is set.

import (
	"context"
	"os"
	"testing"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/sqltext"
)

func TestNotebookExpansionRunsOnDM(t *testing.T) {
	dsn := os.Getenv("DBCORE_TEST_DM_DSN")
	if dsn == "" {
		t.Skip("set DBCORE_TEST_DM_DSN to run the DM notebook check")
	}
	ctx := context.Background()
	drv, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer drv.Close()

	const table = "TPG_NB_ORDERS"
	_, _ = drv.db.ExecContext(ctx, "DROP TABLE IF EXISTS "+table)
	if _, err := drv.db.ExecContext(ctx, "CREATE TABLE "+table+" (ID NUMBER PRIMARY KEY, AMOUNT NUMBER)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { _, _ = drv.db.ExecContext(ctx, "DROP TABLE IF EXISTS "+table) }()
	for _, v := range []string{"(1,50)", "(2,200)", "(3,300)"} {
		if _, err := drv.db.ExecContext(ctx, "INSERT INTO "+table+" (ID, AMOUNT) VALUES "+v); err != nil {
			t.Fatalf("insert %s: %v", v, err)
		}
	}

	cells := []sqltext.NotebookCell{
		{SQL: "SELECT ID, AMOUNT FROM " + table + " WHERE AMOUNT > 100;"},
		{SQL: "SELECT COUNT(*) AS N FROM {{cell_1}}"},
	}
	stmt, err := sqltext.ExpandNotebook(cells, 1)
	if err != nil {
		t.Fatal(err)
	}
	res, err := dbcore.RunQuery(ctx, drv.db, stmt, nil)
	if err != nil {
		t.Fatalf("DM rejected the expanded statement: %v\n  %s", err, stmt)
	}
	// DM hands COUNT back as NUMBER; read it kind-agnostically.
	got := intOf(res.Rows[0][0])
	if got != 2 {
		t.Fatalf("count = %+v, want 2", res.Rows[0][0])
	}

	// A three-deep chain: this is where CTE order matters.
	cells3 := []sqltext.NotebookCell{
		{SQL: "SELECT ID FROM " + table + " WHERE ID > 1"},
		{SQL: "SELECT ID * 10 AS ID10 FROM {{cell_1}}"},
		{SQL: "SELECT SUM(ID10) AS TOTAL FROM {{cell_2}}"},
	}
	stmt3, err := sqltext.ExpandNotebook(cells3, 2)
	if err != nil {
		t.Fatal(err)
	}
	res3, err := dbcore.RunQuery(ctx, drv.db, stmt3, nil)
	if err != nil {
		t.Fatalf("DM rejected the chained statement: %v\n  %s", err, stmt3)
	}
	if got := intOf(res3.Rows[0][0]); got != 50 {
		t.Fatalf("total = %+v, want 50", res3.Rows[0][0])
	}
}
