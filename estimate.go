package dbcore

import (
	"context"
	"strconv"
	"strings"
)

// Row estimates: "how big is this table", without counting it.
//
// A client that paged a ten-million-row table used to say "Page 1 / ?": the
// server had no total to give, and counting every row is exactly the scan a
// client must not start to draw a pager. What an engine can answer cheaply is an
// estimate from its catalog statistics — PostgreSQL's reltuples, SQL Server's
// row counts, Oracle's num_rows, MySQL's TABLE_ROWS — and "about 1.2M rows" is
// the answer a person actually wants.
//
// The estimate is an optional capability on purpose: an engine whose statistics
// are not there (never analysed, a view, no catalog) answers "unknown", and the
// pager keeps its "?", which is true, rather than a number that is not.
type RowEstimator interface {
	// EstimatedRows returns the catalog's estimate for one table, and whether it
	// is an estimate rather than an exact count. ok is false when the engine has
	// no estimate to give.
	EstimatedRows(ctx context.Context, schema, table string) (rows int64, approx bool, ok bool, err error)
}

// AsRowEstimator reports whether a driver can estimate a table's rows.
func AsRowEstimator(d Driver) (RowEstimator, bool) {
	e, ok := d.(RowEstimator)
	return e, ok
}

// EstimateLabel renders an estimate for a person: "~1.2M", "~340", "~4.5K".
// The tilde is not decoration — it says the number came from statistics and may
// lag behind the data.
func EstimateLabel(rows int64) string {
	switch {
	case rows < 0:
		return ""
	case rows < 1000:
		return "~" + itoa(rows)
	case rows < 1_000_000:
		return "~" + trimZero(float64(rows)/1000) + "K"
	case rows < 1_000_000_000:
		return "~" + trimZero(float64(rows)/1_000_000) + "M"
	default:
		return "~" + trimZero(float64(rows)/1_000_000_000) + "B"
	}
}

// trimZero formats a scaled count with one decimal, dropping a trailing ".0".
func trimZero(f float64) string {
	s := strconv.FormatFloat(f, 'f', 1, 64)
	s = strings.TrimSuffix(s, ".0")
	return s
}
