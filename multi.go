package dbcore

// Several result sets from one call.
//
// A statement can answer with more than one grid: SQL Server runs a batch of
// SELECTs, a stored procedure emits each SELECT it ran, MySQL's CALL returns the
// procedure's sets beside its own. `database/sql` shows them through
// Rows.NextResultSet, and until now nothing here walked that: Execute read the
// first set and left the rest on the wire.
//
// RunMulti is the traversal, so the walking rules live in one place: the sets
// that have columns are the results (a DML batch's empty set is not one), each
// set is bounded like a single result, and an error mid-walk returns what was
// read so far with the error, so a caller can show the sets before the failure.

import (
	"context"
	"database/sql"
)

// RunMulti executes a statement and reads every result set it answers with.
// maxRows bounds one set (0 means DriverMaxRows).
func RunMulti(ctx context.Context, exec SQLTx, query string, args []Value, maxRows int) ([]*QueryResult, error) {
	anyArgs := make([]any, len(args))
	for i, a := range args {
		anyArgs[i] = nativeFromValue(a)
	}
	rows, err := exec.QueryContext(ctx, query, anyArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return rowSetsOf(rows, maxRows)
}

// MultiResultReader is a driver that can answer one call with several result
// sets. A driver that does not implement it answers one set through Execute,
// and the client shows one grid — which is the truth for engines whose
// statements cannot carry more than one.
type MultiResultReader interface {
	// ExecuteMulti runs a statement and returns every result set it answers
	// with, in the order the engine emitted them.
	ExecuteMulti(ctx context.Context, query string, args ...Value) ([]*QueryResult, error)
}

// AsMultiResult reports whether a driver can answer with several result sets.
func AsMultiResult(d Driver) (MultiResultReader, bool) {
	m, ok := d.(MultiResultReader)
	return m, ok
}

// rowSetsOf is the sql.Rows adapter RunMulti uses, exposed for drivers that
// hold their own *sql.Rows (a session wrapper, say).
func rowSetsOf(rows *sql.Rows, maxRows int) ([]*QueryResult, error) {
	var out []*QueryResult
	for {
		cols, err := rows.Columns()
		if err != nil {
			return out, err
		}
		if len(cols) > 0 {
			res, err := rowsToResultBounded(rows, maxRows)
			if err != nil {
				return out, err
			}
			out = append(out, res)
		} else {
			for rows.Next() {
			}
			if err := rows.Err(); err != nil {
				return out, err
			}
		}
		if !rows.NextResultSet() {
			break
		}
	}
	return out, rows.Err()
}
