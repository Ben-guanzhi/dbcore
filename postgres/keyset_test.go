package postgres

import (
	"strings"
	"testing"

	"github.com/dbcore/dbcore"
)

// The positional dialect numbers every parameter, so a keyset continuation
// riding behind filter values has to bind at the NEXT position, not reuse $1:
// reusing it made PostgreSQL infer one type from the filter's value and another
// from the integer cursor and reject the page with "integer > text". "?"
// dialects (SQLite, MySQL) silently allow the reuse, so only the real server —
// or this shape test — can catch it. The contract's browse_keyset_with_filter
// now exercises it against a live instance too.
func TestBuildRowsQueryNumbersKeysetAfterFilters(t *testing.T) {
	l := PostgresDialect{}
	req := &dbcore.TableRowsRequest{
		PageSize: 10,
		Sort:     []dbcore.SortKey{{Column: "id"}},
		After:    []dbcore.Value{{Kind: dbcore.ValInt, I: 42}},
		Filter: []dbcore.FilterExpr{
			{Column: "grp", Op: dbcore.FilterEq, Value: dbcore.Value{Kind: dbcore.ValText, S: "a"}},
			{Column: "n", Op: dbcore.FilterGte, Value: dbcore.Value{Kind: dbcore.ValInt, I: 5}},
		},
	}
	q, args := l.BuildRowsQuery("public", "t", req)

	// Two filter params are $1/$2; the cursor is the third.
	if !strings.Contains(q, `"id" > $3`) {
		t.Fatalf("keyset did not bind after the filters:\n%s", q)
	}
	if len(args) != 3 {
		t.Fatalf("args = %d, want 2 filters + 1 cursor: %v", len(args), args)
	}
	if args[2].Kind != dbcore.ValInt || args[2].I != 42 {
		t.Errorf("third arg = %+v, want the cursor 42", args[2])
	}
	// OFFSET must not appear on a seek page (AppendSortLimit suppresses it).
	if strings.Contains(q, "OFFSET") {
		t.Errorf("seek page carried OFFSET:\n%s", q)
	}
}

// No filters: the cursor is $1 and there is no leading AND.
func TestBuildRowsQueryKeysetAlone(t *testing.T) {
	l := PostgresDialect{}
	req := &dbcore.TableRowsRequest{
		PageSize: 10,
		Sort:     []dbcore.SortKey{{Column: "id"}},
		After:    []dbcore.Value{{Kind: dbcore.ValInt, I: 7}},
	}
	q, args := l.BuildRowsQuery("public", "t", req)
	if !strings.Contains(q, `WHERE "id" > $1`) {
		t.Fatalf("no-filter keyset:\n%s", q)
	}
	if len(args) != 1 {
		t.Fatalf("args = %v, want the single cursor", args)
	}
}
