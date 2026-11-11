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

// P3-1 回归：keyset 不可用时（多键排序/多值 cursor）必须回退 OFFSET。
// 旧实现：AppendKeyset 返回 nil（无 keyset 谓词），但 AppendSortLimit 看到
// len(req.After) > 0 就抑制 OFFSET——结果既无 keyset 又无 OFFSET，查询从头
// 返回，静默重复第一页。
func TestBuildRowsQueryMultiKeySortFallsBackToOffset(t *testing.T) {
	l := MySQLDialect{}

	cases := []struct {
		name       string
		req        *dbcore.TableRowsRequest
		wantOffset bool // expect OFFSET clause
		wantKeyset bool // expect keyset predicate (col > ?)
	}{
		{
			name: "multi-key sort with after and page>1",
			req: &dbcore.TableRowsRequest{
				Page: 5, PageSize: 10,
				Sort:  []dbcore.SortKey{{Column: "a"}, {Column: "b"}},
				After: []dbcore.Value{{Kind: dbcore.ValInt, I: 1}, {Kind: dbcore.ValInt, I: 2}},
			},
			wantOffset: true,
			wantKeyset: false,
		},
		{
			name: "multi-value after with single-key sort and page>1",
			req: &dbcore.TableRowsRequest{
				Page: 5, PageSize: 10,
				Sort:  []dbcore.SortKey{{Column: "id"}},
				After: []dbcore.Value{{Kind: dbcore.ValInt, I: 1}, {Kind: dbcore.ValInt, I: 2}},
			},
			wantOffset: true,
			wantKeyset: false,
		},
		{
			name: "single-key sort with after and page>1 uses keyset",
			req: &dbcore.TableRowsRequest{
				Page: 5, PageSize: 10,
				Sort:  []dbcore.SortKey{{Column: "id"}},
				After: []dbcore.Value{{Kind: dbcore.ValInt, I: 42}},
			},
			wantOffset: false,
			wantKeyset: true,
		},
		{
			name: "no after with page>1 uses offset",
			req: &dbcore.TableRowsRequest{
				Page: 5, PageSize: 10,
				Sort: []dbcore.SortKey{{Column: "id"}},
			},
			wantOffset: true,
			wantKeyset: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, _ := l.BuildRowsQuery("shop", "orders", tc.req)

			hasOffset := strings.Contains(q, "OFFSET")
			if hasOffset != tc.wantOffset {
				t.Errorf("OFFSET present = %v, want %v:\n%s", hasOffset, tc.wantOffset, q)
			}

			// Check specifically for the keyset predicate pattern
			keysetPredicate := strings.Contains(q, "`id` >") || strings.Contains(q, "`id` <")
			if keysetPredicate != tc.wantKeyset {
				t.Errorf("keyset predicate = %v, want %v:\n%s", keysetPredicate, tc.wantKeyset, q)
			}
			// Sanity: ORDER BY must always appear when Sort is non-empty
			if len(tc.req.Sort) > 0 && !strings.Contains(q, "ORDER BY") {
				t.Errorf("missing ORDER BY:\n%s", q)
			}
		})
	}
}
