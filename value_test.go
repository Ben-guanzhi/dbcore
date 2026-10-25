package dbcore

import (
	"testing"
	"time"
)

func TestValueForColumnParsesOracleNumber(t *testing.T) {
	if got := valueForColumn("42", "NUMBER"); got.Kind != ValInt || got.I != 42 {
		t.Errorf(`valueForColumn("42", "NUMBER") = %+v, want ValInt 42`, got)
	}
	if got := valueForColumn("1.5", "number"); got.Kind != ValFloat || got.F != 1.5 {
		t.Errorf(`valueForColumn("1.5", "number") = %+v, want ValFloat 1.5`, got)
	}
	if got := valueForColumn("not-a-number", "NUMBER"); got.Kind != ValText || got.S != "not-a-number" {
		t.Errorf(`valueForColumn("not-a-number", "NUMBER") = %+v, want text passthrough`, got)
	}
	if got := valueForColumn("123456789012345678901234567890", "NUMBER"); got.Kind != ValText {
		t.Errorf("out-of-range NUMBER = %+v, want text to preserve digits", got)
	}
	// go-ora hands NUMBER over as its decimal text; a fraction float64 cannot
	// reproduce keeps every digit as text instead of rounding it away.
	if got := valueForColumn("1.234567890123456789", "NUMBER"); got.Kind != ValText || got.S != "1.234567890123456789" {
		t.Errorf("long fraction NUMBER = %+v, want text to preserve digits", got)
	}
	if got := valueForColumn("2.0", "NUMBER"); got.Kind != ValFloat || got.F != 2 {
		t.Errorf(`valueForColumn("2.0", "NUMBER") = %+v, want ValFloat 2`, got)
	}
	if got := valueForColumn("42", "TEXT"); got.Kind != ValText {
		t.Errorf(`valueForColumn("42", "TEXT") = %+v, want text (only NUMBER is parsed)`, got)
	}
}

func TestValueForColumnNormalizesText(t *testing.T) {
	cases := []struct {
		in   any
		typ  string
		kind ValueKind
		text string
	}{
		{[]byte("hello"), "VARCHAR", ValText, "hello"},
		{[]byte("hello"), "varchar", ValText, "hello"},
		{[]byte("hello"), "TEXT", ValText, "hello"},
		{[]byte("hello"), "LONGTEXT", ValText, "hello"},
		{[]byte(`{"a":1}`), "JSON", ValText, `{"a":1}`},
		{[]byte{0, 1, 2}, "BLOB", ValBlob, ""},
		{[]byte("x"), "", ValBlob, ""},
		{int64(7), "INT", ValInt, ""},
		{"already text", "TEXT", ValText, "already text"},
	}
	for _, c := range cases {
		got := valueForColumn(c.in, c.typ)
		if got.Kind != c.kind {
			t.Errorf("valueForColumn(%v, %q).Kind = %v, want %v", c.in, c.typ, got.Kind, c.kind)
		}
		if c.kind == ValText && got.S != c.text {
			t.Errorf("valueForColumn(%v, %q).S = %q, want %q", c.in, c.typ, got.S, c.text)
		}
	}
}

func TestValueOf(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	cases := []struct {
		in   any
		want Value
	}{
		{nil, Value{Kind: ValNull}},
		{int64(7), Value{Kind: ValInt, I: 7}},
		{float64(1.5), Value{Kind: ValFloat, F: 1.5}},
		{float32(2.5), Value{Kind: ValFloat, F: 2.5}},
		{"s", Value{Kind: ValText, S: "s"}},
		{[]byte("b"), Value{Kind: ValBlob, B: []byte("b")}},
		{true, Value{Kind: ValBool, Bool: true}},
		{now, Value{Kind: ValTime, T: now}},
		{int(3), Value{Kind: ValText, S: "3"}}, // non-scalar degrades to %v text
	}
	for _, c := range cases {
		got := ValueOf(c.in)
		if got.Kind != c.want.Kind || got.I != c.want.I || got.F != c.want.F ||
			got.S != c.want.S || got.Bool != c.want.Bool || !got.T.Equal(c.want.T) ||
			string(got.B) != string(c.want.B) {
			t.Errorf("ValueOf(%#v) = %+v, want %+v", c.in, got, c.want)
		}
	}
}
