package dbcore

import (
	"context"
	"database/sql"
	"strings"
)

// RunQuery executes a SELECT-style statement and converts the rows to QueryResult.
// Exported so drivers (and tests) reuse the shared scan logic.
func RunQuery(ctx context.Context, exec SQLTx, query string, args []Value) (*QueryResult, error) {
	anyArgs := make([]any, len(args))
	for i, a := range args {
		anyArgs[i] = nativeFromValue(a)
	}
	rows, err := exec.QueryContext(ctx, query, anyArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return rowsToResult(rows)
}

// RunExec executes a DML statement (INSERT/UPDATE/DELETE) and returns affected rows
// and last insert id.
func RunExec(ctx context.Context, exec SQLTx, query string, args []Value) (affected, lastID int64, err error) {
	anyArgs := make([]any, len(args))
	for i, a := range args {
		anyArgs[i] = nativeFromValue(a)
	}
	res, err := exec.ExecContext(ctx, query, anyArgs...)
	if err != nil {
		return 0, 0, err
	}
	affected, _ = res.RowsAffected()
	lastID, _ = res.LastInsertId()
	return affected, lastID, nil
}

// rowsToResult reads column metadata and all rows into a QueryResult.
func rowsToResult(rows *sql.Rows) (*QueryResult, error) {
	colNames, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}
	res := &QueryResult{
		Columns: make([]ColumnInfo, len(colNames)),
	}
	for i, ct := range colTypes {
		ci := ColumnInfo{Name: ct.Name(), Type: ct.DatabaseTypeName()}
		if nullable, ok := ct.Nullable(); ok {
			ci.Nullable = nullable
		}
		res.Columns[i] = ci
	}

	for rows.Next() {
		holders := make([]any, len(colNames))
		scan := make([]any, len(colNames))
		for i := range holders {
			holders[i] = &scan[i]
		}
		if err := rows.Scan(holders...); err != nil {
			return nil, err
		}
		row := make([]Value, len(colNames))
		for i, v := range scan {
			row[i] = valueForColumn(v, colTypes[i].DatabaseTypeName())
		}
		res.Rows = append(res.Rows, row)
	}
	return res, rows.Err()
}

// IsReadQuery reports whether a statement is expected to return rows. Exported so
// drivers can decide between QueryContext and ExecContext uniformly.
//
// Beyond the read-keyword prefixes, INSERT/UPDATE/DELETE ... RETURNING
// (PostgreSQL, SQLite >= 3.35, MariaDB) also produces a result set that a plain
// Exec would silently discard, so those route to the query path too.
func IsReadQuery(s string) bool {
	t := strings.TrimSpace(s)
	t = strings.TrimLeft(t, "(")
	upper := strings.ToUpper(t)
	for _, p := range []string{"SELECT", "WITH", "PRAGMA", "SHOW", "EXPLAIN", "VALUES", "TABLE", "DESC", "DESCRIBE"} {
		if strings.HasPrefix(upper, p) {
			return true
		}
	}
	return HasReturningClause(t)
}

// HasReturningClause reports whether the statement contains a RETURNING
// clause. Such a statement produces a result set that a plain Exec would
// silently discard.
//
// The scan skips string literals, quoted identifiers, and comments, so a value
// like UPDATE t SET note = 'returning' is not misrouted to the query path
// (which then fails on a statement that returns nothing). Only a whole word
// outside any quoted region counts as a clause.
func HasReturningClause(sql string) bool {
	var quote byte // active quote character; 0 outside a quoted region
	i := 0
	for i < len(sql) {
		c := sql[i]
		if quote != 0 {
			end := quote // brackets close on a different character
			if quote == '[' {
				end = ']'
			}
			switch {
			case c == end && i+1 < len(sql) && sql[i+1] == end:
				i += 2 // doubled quote escapes a single literal quote
				continue
			case c == end:
				quote = 0
				i++
			case quote == '\'' && c == '\\' && i+1 < len(sql):
				i += 2 // backslash escape, MySQL default
			default:
				i++
			}
			continue
		}
		switch c {
		case '\'', '"', '`', '[':
			quote = c
			i++
		case '-':
			if i+1 < len(sql) && sql[i+1] == '-' {
				if nl := strings.IndexByte(sql[i:], '\n'); nl >= 0 {
					i += nl + 1
				} else {
					i = len(sql)
				}
				continue
			}
			i++
		case '/':
			if i+1 < len(sql) && sql[i+1] == '*' {
				if end := strings.Index(sql[i:], "*/"); end >= 0 {
					i += end + 2
				} else {
					i = len(sql)
				}
				continue
			}
			i++
		default:
			if isIdentChar(c) {
				start := i
				for i < len(sql) && isIdentChar(sql[i]) {
					i++
				}
				if strings.EqualFold(sql[start:i], "returning") {
					return true
				}
			} else {
				i++
			}
		}
	}
	return false
}

// isIdentChar reports whether c may occur inside a SQL word. ASCII only: the
// RETURNING keyword is ASCII, so a non-ASCII run can never match and is stepped
// over one byte at a time.
func isIdentChar(c byte) bool {
	return c == '_' || c == '$' ||
		(c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

// AppendSortLimit appends ORDER BY / LIMIT / OFFSET clauses to b using the dialect.
// Shared by every driver so paging syntax lives in exactly one place (contract §4.5).
func AppendSortLimit(b *strings.Builder, d Dialect, req *TableRowsRequest) {
	if len(req.Sort) > 0 {
		b.WriteString(" ORDER BY ")
		for i, s := range req.Sort {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(d.QuoteIdent(s.Column))
			if s.Desc {
				b.WriteString(" DESC")
			}
		}
	}
	limit, offset := req.PageSize, 0
	if req.Page > 1 {
		offset = (req.Page - 1) * req.PageSize
	}
	if clause := d.LimitOffset(limit, offset); clause != "" {
		b.WriteString(" ")
		b.WriteString(clause)
	}
}
