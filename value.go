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
// without turning real BLOB columns into text.
var textColumnTypes = map[string]bool{
	"CHAR": true, "VARCHAR": true, "TINYTEXT": true, "TEXT": true,
	"MEDIUMTEXT": true, "LONGTEXT": true, "NCHAR": true, "NVARCHAR": true,
	"NTEXT": true, "ENUM": true, "SET": true, "JSON": true,
	// Exact numerics scanned as bytes by go-sql-driver (contract §6.6 showed
	// DECIMAL arriving as []byte); text keeps the digits readable everywhere.
	"DECIMAL": true, "NUMERIC": true,
	"CITEXT": true, "NAME": true, "UUID": true,
	"VARCHAR2": true, "NVARCHAR2": true, "CLOB": true, "NCLOB": true, "LONG": true,
}

// valueForColumn converts a scanned value using the column's catalog type.
func valueForColumn(v any, typeName string) Value {
	val := nativeToValue(v)
	if val.Kind == ValBlob && textColumnTypes[strings.ToUpper(typeName)] {
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
	// Only fractional/scientific forms become floats; an integer outside int64
	// stays text rather than silently losing digits through a float round-trip.
	// The same fidelity rule applies to fractions: a database NUMBER keeps every
	// digit it was given, so a decimal float64 cannot reproduce (think
	// 1.234567890123456789) stays text instead of being rounded.
	if strings.ContainsAny(s, ".eE") {
		if f, err := strconv.ParseFloat(s, 64); err == nil && floatHoldsExactly(f, s) {
			return Value{Kind: ValFloat, F: f}
		}
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
