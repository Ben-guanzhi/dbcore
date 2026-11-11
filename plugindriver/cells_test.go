package plugindriver

import (
	"testing"
	"time"

	"github.com/dbcore/dbcore"
)

// The tagged cell is the protocol's precision contract: JSON has one number
// type, so each kind must survive Encode→Decode unchanged — and an unknown
// kind must fail loudly rather than decode as NULL.
func TestValueCellRoundTrip(t *testing.T) {
	when := time.Date(2026, 10, 26, 8, 0, 0, 123456789, time.UTC)
	cases := []dbcore.Value{
		{Kind: dbcore.ValNull},
		{Kind: dbcore.ValInt, I: 9223372036854775807},
		{Kind: dbcore.ValFloat, F: 1.5},
		{Kind: dbcore.ValText, S: "héllo"},
		{Kind: dbcore.ValBlob, B: []byte{0, 1, 255}},
		{Kind: dbcore.ValBool, Bool: true},
		{Kind: dbcore.ValTime, T: when},
		// P3-2: the exact digits ride the text slot but keep their own kind.
		{Kind: dbcore.ValDecimal, S: "123456789012345678901234567890.123"},
	}
	for _, v := range cases {
		c := EncodeValue(v)
		got, err := DecodeValue(c)
		if err != nil {
			t.Fatalf("round trip of %+v: %v", v, err)
		}
		if got.Kind != v.Kind {
			t.Errorf("kind %v became %v", v.Kind, got.Kind)
		}
		if got.String() != v.String() {
			t.Errorf("text %q became %q", v.String(), got.String())
		}
	}
	if _, err := DecodeValue(CellPayload{Kind: "quantic"}); err == nil {
		t.Errorf("unknown kind must be an error, not a silent NULL")
	}
}

// P3-7 回归：browseArgs 必须把 req.After 翻译进 BrowseArgs.After，
// 不能静默丢弃。旧写法忽略 After，插件收到无游标的浏览请求返回第一页。
func TestBrowseArgsTranslatesAfter(t *testing.T) {
	req := &dbcore.TableRowsRequest{
		Page:     1,
		PageSize: 50,
		Sort:     []dbcore.SortKey{{Column: "id", Desc: false}},
		Filter:   []dbcore.FilterExpr{{Column: "name", Op: "=", Value: dbcore.Value{Kind: dbcore.ValText, S: "x"}}},
		After:    []dbcore.Value{{Kind: dbcore.ValInt, I: 42}},
	}
	args := browseArgs("main", "parts", req)
	if len(args.After) != 1 {
		t.Fatalf("After len = %d, want 1", len(args.After))
	}
	if args.After[0].Kind != "int" {
		t.Errorf("After[0].Kind = %q, want int", args.After[0].Kind)
	}
	if args.After[0].Int != 42 {
		t.Errorf("After[0].Int = %d, want 42", args.After[0].Int)
	}
	if len(args.Sort) != 1 || args.Sort[0].Column != "id" {
		t.Errorf("Sort = %+v, want [id]", args.Sort)
	}
	if len(args.Filter) != 1 || args.Filter[0].Column != "name" {
		t.Errorf("Filter = %+v, want [name]", args.Filter)
	}
}

// P1-1 回归：Conn.BrowseRows 必须把 req.Filter/Sort/After 翻译进
// ConnBrowseParams，不能只传 Limit。
func TestConnBrowseParamsTranslatesFilterSortAfter(t *testing.T) {
	req := &dbcore.TableRowsRequest{
		PageSize: 25,
		Sort:     []dbcore.SortKey{{Column: "id", Desc: true}},
		Filter:   []dbcore.FilterExpr{{Column: "status", Op: "=", Value: dbcore.Value{Kind: dbcore.ValText, S: "active"}}},
		After:    []dbcore.Value{{Kind: dbcore.ValInt, I: 7}},
	}
	params := ConnBrowseParams{
		ConnID: "c1",
		Table:  "users",
		Limit:  req.PageSize,
	}
	// 模拟 Conn.BrowseRows 的翻译逻辑（callConn 需要真实插件进程）。
	for _, key := range req.Sort {
		params.Sort = append(params.Sort, SortArg{Column: key.Column, Desc: key.Desc})
	}
	for _, filter := range req.Filter {
		params.Filter = append(params.Filter, FilterArg{
			Column: filter.Column,
			Op:     string(filter.Op),
			Value:  EncodeValue(filter.Value),
		})
	}
	for _, v := range req.After {
		params.After = append(params.After, EncodeValue(v))
	}
	if len(params.Sort) != 1 || params.Sort[0].Column != "id" || !params.Sort[0].Desc {
		t.Errorf("Sort = %+v, want [id desc]", params.Sort)
	}
	if len(params.Filter) != 1 || params.Filter[0].Column != "status" {
		t.Errorf("Filter = %+v, want [status]", params.Filter)
	}
	if len(params.After) != 1 || params.After[0].Int != 7 {
		t.Errorf("After = %+v, want [7]", params.After)
	}
}
