package mysql

import (
	"strings"
	"testing"

	"github.com/dbcore/dbcore"
)

// Single-key keyset continuation: the predicate joins an existing WHERE with
// AND, binds the cursor value, and AppendSortLimit suppresses OFFSET.
func TestAppendKeyset(t *testing.T) {
	l := MySQLDialect{}
	var b strings.Builder
	b.WriteString("SELECT * FROM ")
	b.WriteString(l.QuoteIdent("shop", "orders"))
	req := &dbcore.TableRowsRequest{
		Page: 5, PageSize: 10,
		Sort:  []dbcore.SortKey{{Column: "id"}},
		After: []dbcore.Value{{Kind: dbcore.ValInt, I: 42}},
	}
	args := dbcore.AppendKeyset(&b, l, req, 0)
	if len(args) != 1 || args[0].I != 42 {
		t.Fatalf("args = %v", args)
	}
	if !strings.Contains(b.String(), " WHERE `id` > ?") {
		t.Fatalf("no-filter keyset = %q", b.String())
	}

	// Filters first: the keyset joins with AND. MySQL's "?" repeats, so the
	// argument count is what matters here.
	req2 := &dbcore.TableRowsRequest{
		Sort:   []dbcore.SortKey{{Column: "id", Desc: true}},
		After:  []dbcore.Value{{Kind: dbcore.ValInt, I: 7}},
		Filter: []dbcore.FilterExpr{{Column: "tag", Value: dbcore.Value{Kind: dbcore.ValText, S: "x"}}},
	}
	var b2 strings.Builder
	b2.WriteString("SELECT * FROM ")
	b2.WriteString(l.QuoteIdent("shop", "orders"))
	if args := dbcore.AppendKeyset(&b2, l, req2, 1); len(args) != 1 {
		t.Fatalf("args = %v", args)
	}
	if !strings.Contains(b2.String(), " AND `id` < ?") {
		t.Fatalf("filtered keyset = %q", b2.String())
	}

	// Multi-key sorts degrade to OFFSET (no predicate), documented behavior.
	req3 := &dbcore.TableRowsRequest{
		Sort:  []dbcore.SortKey{{Column: "a"}, {Column: "b"}},
		After: []dbcore.Value{{Kind: dbcore.ValInt, I: 1}, {Kind: dbcore.ValInt, I: 2}},
	}
	var b3 strings.Builder
	if got := dbcore.AppendKeyset(&b3, l, req3, 0); got != nil {
		t.Fatalf("multi-key should degrade, got %v", got)
	}
}
