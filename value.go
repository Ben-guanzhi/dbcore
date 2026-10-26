package dbcore

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// ValueKind discriminates the active field of a Value.
type ValueKind int

const (
	ValNull ValueKind = iota
	ValInt
	ValFloat
	ValText
	ValBlob
	ValBool
	ValTime
	// ValDecimal is an exact decimal number kept as its digit text (SQL
	// DECIMAL/NUMERIC columns). float64 silently rounds values a database
	// stores exactly, and int64 overflows past 18-19 digits, so the text IS
	// the value; helpers below convert to int64/float64 only when they lose
	// nothing. Appended last: consumers switch on names, and stored kind
	// numbers must not shift for old data.
	ValDecimal
)

// Value is the unified cell value crossing the driver boundary. Exactly one
// typed field is meaningful per Kind.
type Value struct {
	Kind ValueKind
	I    int64
	F    float64
	S    string
	B    []byte
	T    time.Time
	Bool bool
}

func (v Value) String() string {
	switch v.Kind {
	case ValNull:
		return "NULL"
	case ValInt:
		return itoa(v.I)
	case ValFloat:
		return fmt.Sprintf("%v", v.F)
	case ValText:
		return v.S
	case ValBlob:
		return fmt.Sprintf("<blob %d bytes>", len(v.B))
	case ValBool:
		if v.Bool {
			return "true"
		}
		return "false"
	case ValTime:
		return v.T.Format(time.RFC3339Nano)
	case ValDecimal:
		return v.S
	default:
		return "?"
	}
}

// TryGet* accessors return (value, ok). Use these instead of field access so
// callers get a clear type-mismatch signal (contract §6.6: value round-trip).
func TryGetInt(v Value) (int64, bool) {
	if v.Kind == ValInt {
		return v.I, true
	}
	return 0, false
}
func TryGetFloat(v Value) (float64, bool) {
	if v.Kind == ValFloat {
		return v.F, true
	}
	return 0, false
}
func TryGetString(v Value) (string, bool) {
	if v.Kind == ValText {
		return v.S, true
	}
	return "", false
}
func TryGetBlob(v Value) ([]byte, bool) {
	if v.Kind == ValBlob {
		return v.B, true
	}
	return nil, false
}
func TryGetBool(v Value) (bool, bool) {
	if v.Kind == ValBool {
		return v.Bool, true
	}
	return false, false
}
func TryGetTime(v Value) (time.Time, bool) {
	if v.Kind == ValTime {
		return v.T, true
	}
	return time.Time{}, false
}

// TryGetDecimal returns the exact digit text of a ValDecimal cell (an empty
// string with ok=true only for a genuinely empty decimal, which drivers never
// produce — a missing value is ValNull).
func TryGetDecimal(v Value) (string, bool) {
	if v.Kind == ValDecimal {
		return v.S, true
	}
	return "", false
}

// DecimalValue builds a ValDecimal from its exact text. The text must parse as
// a decimal number; anything else would be a data-quality bug upstream, so it
// is preserved as plain text rather than rejected.
func DecimalValue(s string) Value {
	if _, ok := new(big.Rat).SetString(strings.TrimSpace(s)); !ok {
		return Value{Kind: ValText, S: s}
	}
	return Value{Kind: ValDecimal, S: strings.TrimSpace(s)}
}

// DecimalToInt64 returns the value as an int64 when the decimal is integral
// and fits — losslessness first, matching the int-preservation rule the
// Oracle NUMBER decoder uses.
func DecimalToInt64(v Value) (int64, bool) {
	if v.Kind != ValDecimal {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v.S), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// DecimalToFloat64 returns the value as a float64 only when the double carries
// every digit of the decimal; a lossy conversion would be exactly the bug
// ValDecimal exists to prevent, so callers must handle ok=false (typically by
// keeping the text).
func DecimalToFloat64(v Value) (float64, bool) {
	if v.Kind != ValDecimal {
		return 0, false
	}
	s := strings.TrimSpace(v.S)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || !floatHoldsExactly(f, s) {
		return 0, false
	}
	return f, true
}

// ValueOf converts a native Go scalar (nil / int64 / float64 / string / []byte /
// bool / time.Time) into a Value, the boundary helper for callers whose data
// starts as Go values rather than scanned rows. Anything else degrades to its
// %v text, mirroring what a text cast would store.
func ValueOf(v any) Value {
	return nativeToValue(v)
}

// nativeFromValue converts a Value to the driver-native argument type.
func nativeFromValue(v Value) any {
	switch v.Kind {
	case ValNull:
		return nil
	case ValInt:
		return v.I
	case ValFloat:
		return v.F
	case ValText:
		return v.S
	case ValBlob:
		return v.B
	case ValBool:
		return v.Bool
	case ValTime:
		return v.T
	case ValDecimal:
		// Drivers accept decimal text for exact-numeric parameters (and SQLite
		// has no exact-numeric type at all), so the digits bind as a string.
		return v.S
	default:
		return nil
	}
}

// nativeToValue maps a scanned database value to a Value.
func nativeToValue(v any) Value {
	switch x := v.(type) {
	case nil:
		return Value{Kind: ValNull}
	case int64:
		return Value{Kind: ValInt, I: x}
	case float64:
		return Value{Kind: ValFloat, F: x}
	case float32:
		return Value{Kind: ValFloat, F: float64(x)}
	case string:
		return Value{Kind: ValText, S: x}
	case []byte:
		return Value{Kind: ValBlob, B: x}
	case bool:
		return Value{Kind: ValBool, Bool: x}
	case time.Time:
		return Value{Kind: ValTime, T: x}
	default:
		return Value{Kind: ValText, S: fmt.Sprintf("%v", x)}
	}
}

// textColumnTypes lists catalog type names that hold text even though some
// drivers (notably go-sql-driver/mysql) scan them into []byte. Normalizing by
// column type keeps the contract's string round-trip (contract §6.6) working
// without turning real BLOB columns into text. Exact numerics are NOT here —
// they route to ValDecimal via exactDecimalTypes below.
var textColumnTypes = map[string]bool{
	"CHAR": true, "VARCHAR": true, "TINYTEXT": true, "TEXT": true,
	"MEDIUMTEXT": true, "LONGTEXT": true, "NCHAR": true, "NVARCHAR": true,
	"NTEXT": true, "ENUM": true, "SET": true, "JSON": true,
	"CITEXT": true, "NAME": true, "UUID": true,
	"VARCHAR2": true, "NVARCHAR2": true, "CLOB": true, "NCLOB": true, "LONG": true,
}

// exactDecimalTypes lists catalog type names whose values a database stores as
// EXACT decimals (fixed digits). They route to ValDecimal, not text: MySQL
// scans them as []byte and a float64 round-trip would silently move the point,
// so the digit text becomes the value with a numeric kind.
var exactDecimalTypes = map[string]bool{
	"DECIMAL": true, "NUMERIC": true, "DEC": true, "FIXED": true, // Sybase ASA "FIXED(18,4)"
	"NEWDECIMAL": true, // MariaDB binary protocol
	"MONEY":      true, "SMALLMONEY": true,
}

// IsExactDecimalType reports whether a catalog type name (DatabaseTypeName or
// information_schema DATA_TYPE, any case) denotes an EXACT numeric the driver
// may hand over as bytes/text. Consumers use it to mark a column's cells as
// numeric (sort/sum) even when the wire shape is the digit text — the same
// knowledge valueForColumn uses to produce ValDecimal, so the two can never
// disagree.
func IsExactDecimalType(typeName string) bool {
	base := typeName
	if i := strings.IndexByte(base, '('); i >= 0 {
		base = base[:i]
	}
	return exactDecimalTypes[strings.ToUpper(strings.TrimSpace(base))]
}

// valueForColumn converts a scanned value using the column's catalog type.
func valueForColumn(v any, typeName string) Value {
	val := nativeToValue(v)
	upper := strings.ToUpper(typeName)
	// Exact-decimal columns: bytes or text become ValDecimal when the digits
	// parse; anything else (a driver that hands float64) keeps its own kind.
	if exactDecimalTypes[upper] && (val.Kind == ValBlob || val.Kind == ValText) {
		text := val.S
		if val.Kind == ValBlob {
			text = string(val.B)
		}
		if _, ok := new(big.Rat).SetString(strings.TrimSpace(text)); ok {
			return Value{Kind: ValDecimal, S: strings.TrimSpace(text)}
		}
		return Value{Kind: ValText, S: text}
	}
	if val.Kind == ValBlob && textColumnTypes[upper] {
		return Value{Kind: ValText, S: string(val.B)}
	}
	// go-ora decodes every NUMBER as its decimal string, including COUNT(*)
	// and integral columns, so restore the numeric kind for integers/floats.
	if val.Kind == ValText && strings.EqualFold(typeName, "NUMBER") {
		return numberTextToValue(val.S)
	}
	return val
}

func numberTextToValue(s string) Value {
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return Value{Kind: ValInt, I: i}
	}
	// A number that is not an int64 still deserves its numeric kind: an
	// integer past int64 or a fraction float64 cannot reproduce (think
	// 1.234567890123456789) stays exact as ValDecimal rather than silently
	// losing digits through a float round-trip or degrading to text.
	if _, err := strconv.ParseFloat(s, 64); err != nil {
		return Value{Kind: ValText, S: s}
	}
	// Only fractional/scientific forms become floats.
	if strings.ContainsAny(s, ".eE") {
		if f, err := strconv.ParseFloat(s, 64); err == nil && floatHoldsExactly(f, s) {
			return Value{Kind: ValFloat, F: f}
		}
	}
	if _, ok := new(big.Rat).SetString(strings.TrimSpace(s)); ok {
		return Value{Kind: ValDecimal, S: s}
	}
	return Value{Kind: ValText, S: s}
}

// floatHoldsExactly reports whether f carries every digit of the decimal text.
func floatHoldsExactly(f float64, s string) bool {
	exact, ok := new(big.Rat).SetString(s)
	if !ok {
		return false
	}
	got := new(big.Rat).SetFloat64(f)
	return got != nil && got.Cmp(exact) == 0
}

// ValuesOf converts raw driver args (the []interface{} the sqltext recipes
// return) into bound values.
func ValuesOf(args []interface{}) []Value {
	out := make([]Value, 0, len(args))
	for _, a := range args {
		out = append(out, ValueOf(a))
	}
	return out
}

// IntFrom reads an integral from a scanned cell, accepting the text spellings
// some engines return for numeric columns (go-ora's NUMBER strings, MSSQL
// variants) and exact decimals whose digits happen to be integral. ok=false
// means the cell is not an integer at all.
func IntFrom(v Value) (int64, bool) {
	if n, ok := TryGetInt(v); ok {
		return n, true
	}
	if v.Kind == ValDecimal {
		return DecimalToInt64(v)
	}
	if v.Kind == ValFloat {
		return int64(v.F), true
	}
	if v.Kind == ValText {
		if n, err := strconv.ParseInt(strings.TrimSpace(v.S), 10, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}
