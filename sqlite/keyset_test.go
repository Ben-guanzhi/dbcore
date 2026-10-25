package sqlite

import (
	"context"
	"testing"

	"github.com/dbcore/dbcore"
)

// Deep paging through the BrowseRows path: a keyset walk over 100 rows must
// land every row exactly once and terminate, without OFFSET rescan.
func TestBrowseRowsKeysetWalk(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	ctx := context.Background()
	table := "dbcore_keyset_walk"
	if _, err := d.Execute(ctx, "DROP TABLE IF EXISTS "+table); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := d.Execute(ctx, "CREATE TABLE "+table+"(id INTEGER PRIMARY KEY, tag TEXT)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _, _ = d.Execute(context.Background(), "DROP TABLE IF EXISTS "+table) })
	for i := 0; i < 100; i++ {
		if _, err := d.Execute(ctx, "INSERT INTO "+table+"(id,tag) VALUES (?,?)",
			dbcore.Value{Kind: dbcore.ValInt, I: int64(i)},
			dbcore.Value{Kind: dbcore.ValText, S: "t"},
		); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	seen := map[int64]bool{}
	var after []dbcore.Value
	pages := 0
	for {
		req := &dbcore.TableRowsRequest{PageSize: 15, Sort: []dbcore.SortKey{{Column: "id"}}, After: after}
		res, err := d.BrowseRows(ctx, "main", table, req)
		if err != nil {
			t.Fatalf("browse: %v", err)
		}
		for _, row := range res.Rows {
			id := row[0].I
			if seen[id] {
				t.Fatalf("row %d visited twice", id)
			}
			seen[id] = true
		}
		if len(res.Rows) < 15 {
			break
		}
		last := res.Rows[len(res.Rows)-1]
		after = []dbcore.Value{last[0]}
		pages++
		if pages > 20 {
			t.Fatal("walk did not terminate")
		}
	}
	if len(seen) != 100 {
		t.Fatalf("visited %d rows, want 100", len(seen))
	}
}
