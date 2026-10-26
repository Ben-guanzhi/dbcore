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
	if got := valueForColumn("123456789012345678901234567890", "NUMBER"); got.Kind != ValDecimal || got.S != "123456789012345678901234567890" {
		t.Errorf("out-of-range NUMBER = %+v, want decimal to preserve digits", got)
	}
	// go-ora hands NUMBER over as its decimal text; a fraction float64 cannot
	// reproduce keeps every digit as text instead of rounding it away.
	if got := valueForColumn("1.234567890123456789", "NUMBER"); got.Kind != ValDecimal || got.S != "1.234567890123456789" {
		t.Errorf("long fraction NUMBER = %+v, want decimal to preserve digits", got)
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

// MySQL's driver scans exact numerics (DECIMAL/NUMERIC) as []byte. P3-2 routes
// them to ValDecimal: the digit text stays the value (a float64 would move the
// point, an int64 overflows), the kind says "number" so grids sort and sum
// numerically, and helpers convert losslessly or not at all.
func TestValueForColumnDecimalIsExact(t *testing.T) {
	v := valueForColumn([]byte("123.45"), "DECIMAL")
	if v.Kind != ValDecimal || v.S != "123.45" {
		t.Fatalf("got %v, want decimal 123.45", v)
	}
	if v.String() != "123.45" {
		t.Errorf("String() = %q, want the digits verbatim", v.String())
	}
	if got, ok := DecimalToInt64(v); ok {
		t.Errorf("123.45 must not convert to int64 (got %d)", got)
	}
	// 123.45 is NOT representable as a float64, so the losslessness guard must
	// refuse — that refusal is the whole point of ValDecimal existing.
	if got, ok := DecimalToFloat64(v); ok {
		t.Errorf("DecimalToFloat64(123.45) = %v, want refused (a double rounds it)", got)
	}
	exact := DecimalValue("0.5") // 0.5 is exact in binary
	if got, ok := DecimalToFloat64(exact); !ok || got != 0.5 {
		t.Errorf("DecimalToFloat64(0.5) = %v, %v, want exact float", got, ok)
	}
	v = valueForColumn([]byte("10"), "NUMERIC")
	if v.Kind != ValDecimal || v.S != "10" {
		t.Fatalf("got %v, want decimal 10", v)
	}
	if n, ok := DecimalToInt64(v); !ok || n != 10 {
		t.Errorf("DecimalToInt64(10) = %d, %v, want 10", n, ok)
	}
	// A decimal past int64 stays lossless: no float, no int, only digits.
	big := valueForColumn([]byte("99999999999999999999.5"), "DECIMAL")
	if big.Kind != ValDecimal {
		t.Fatalf("big decimal = %+v, want ValDecimal", big)
	}
	if _, ok := DecimalToInt64(big); ok {
		t.Errorf("a 20-digit decimal must not convert to int64")
	}
	if _, ok := DecimalToFloat64(big); ok {
		t.Errorf("a 21-digit decimal must not convert to float64")
	}
	// A real blob stays a blob.
	v = valueForColumn([]byte{0x00, 0x01}, "BLOB")
	if v.Kind != ValBlob {
		t.Fatalf("blob degraded to %v", v.Kind)
	}
	// Garbage in a decimal column is not invented into a number.
	v = valueForColumn([]byte("--not-a-number--"), "DECIMAL")
	if v.Kind != ValText {
		t.Fatalf("unparseable decimal = %v, want text passthrough", v.Kind)
	}
}

// IntFrom accepts an integral decimal: contracts and keyset cursors that read
// integers through it keep working when a driver delivers DECIMAL(10,0)-style
// columns as exact decimals.
func TestIntFromAcceptsIntegralDecimal(t *testing.T) {
	v := valueForColumn([]byte("42"), "DECIMAL")
	if n, ok := IntFrom(v); !ok || n != 42 {
		t.Errorf("IntFrom(decimal 42) = %d, %v, want 42", n, ok)
	}
	if _, ok := IntFrom(valueForColumn([]byte("4.5"), "DECIMAL")); ok {
		t.Errorf("IntFrom(4.5) must refuse")
	}
}
