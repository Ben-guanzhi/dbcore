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

// DriverMaxRows is the materialization bound shared by every consumer that
// drains a cursor into memory: reading stops at this many rows and the result
// is marked Truncated. sqltui's AdHocRowLimit used to slice the tail off AFTER
// the kernel had allocated a row for every row in the table — the cap hid the
// rows from the UI without bounding the memory.
const DriverMaxRows = 10000

// RunQueryBounded is RunQuery with a materialization bound: reading stops at
// maxRows (<=0 means DriverMaxRows) and Truncated/TotalRows report the cut.
// A caller whose own cap is smaller passes it here instead of trimming after
// the fact.
func RunQueryBounded(ctx context.Context, exec SQLTx, query string, args []Value, maxRows int) (*QueryResult, error) {
	anyArgs := make([]any, len(args))
	for i, a := range args {
		anyArgs[i] = nativeFromValue(a)
	}
	rows, err := exec.QueryContext(ctx, query, anyArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return rowsToResultBounded(rows, maxRows)
}

// rowsToResult reads column metadata and all rows into a QueryResult.
func rowsToResult(rows *sql.Rows) (*QueryResult, error) {
	return rowsToResultBounded(rows, 0)
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

// rowsToResultBounded reads column metadata and up to maxRows rows into a
// QueryResult. maxRows <= 0 means DriverMaxRows.
func rowsToResultBounded(rows *sql.Rows, maxRows int) (*QueryResult, error) {
	if maxRows <= 0 {
		maxRows = DriverMaxRows
	}
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
	// rows.ColumnTypes() cannot tell two same-named projections apart; give the
	// duplicates distinct keys so the grid does not fold them together.
	DisambiguateColumnNames(res.Columns)

	for rows.Next() {
		if len(res.Rows) >= maxRows {
			total := len(res.Rows)
			res.Truncated = true
			res.TotalRows = &total
			break
		}
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
//
// Leading trivia (whitespace, comments, opening parens) is stripped before the
// prefix test: Oracle's optimizer hints make `/*+ ... */ SELECT` the everyday
// spelling of a read, and without the strip the statement misroutes to Exec —
// the result set is silently discarded and the user sees an empty grid.
func IsReadQuery(s string) bool {
	t := skipLeadingTrivia(s)
	upper := strings.ToUpper(t)
	for _, p := range []string{"SELECT", "WITH", "PRAGMA", "SHOW", "EXPLAIN", "VALUES", "TABLE", "DESC", "DESCRIBE"} {
		if strings.HasPrefix(upper, p) {
			return true
		}
	}
	return HasReturningClause(t)
}

// skipLeadingTrivia strips whitespace, line/block comments and opening parens
// from the front of a statement, so keyword classification sees the first real
// token. A statement that is nothing but an unterminated comment returns "".
func skipLeadingTrivia(s string) string {
	for {
		s = strings.TrimLeft(s, " \t\r\n;")
		switch {
		case strings.HasPrefix(s, "--"):
			if nl := strings.IndexByte(s, '\n'); nl >= 0 {
				s = s[nl+1:]
				continue
			}
			return ""
		case strings.HasPrefix(s, "/*"):
			if end := strings.Index(s, "*/"); end >= 0 {
				s = s[end+2:]
				continue
			}
			return ""
		case strings.HasPrefix(s, "("):
			s = s[1:]
		default:
			return s
		}
	}
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

// keysetApplicable reports whether the request's After cursor can drive a
// keyset predicate. Keyset continuation requires exactly one sort key and
// exactly one cursor value: the predicate is `sort_col > after_val`, which
// has no well-defined semantics for multi-column sorts or multi-value cursors.
// Both AppendKeyset and AppendSortLimit consult this so the two stay in sync.
func keysetApplicable(req *TableRowsRequest) bool {
	return len(req.After) == 1 && len(req.Sort) == 1
}

// AppendKeyset appends the keyset continuation predicate (single sort key:
// k > v with a dialect placeholder) and returns the bound args. Multi-key
// sorts or multi-value cursors degrade to OFFSET-based pagination (AppendSortLimit
// uses req.Page when keysetApplicable is false): the page is still correct,
// just not seek-fast. existingArgs is the number of parameters the statement
// already carries (the filter values): the continuation binds at the NEXT
// position. Passing it is not bookkeeping pedantry — positional dialects
// ($1… in PostgreSQL) mis-type a reused number ("integer > text" on a real
// instance), while the "?" dialects hide the bug entirely.
func AppendKeyset(b *strings.Builder, d Dialect, req *TableRowsRequest, existingArgs int) []Value {
	if !keysetApplicable(req) {
		return nil
	}
	if existingArgs > 0 {
		b.WriteString(" AND ")
	} else {
		b.WriteString(" WHERE ")
	}
	b.WriteString(d.QuoteIdent(req.Sort[0].Column))
	if req.Sort[0].Desc {
		b.WriteString(" < ")
	} else {
		b.WriteString(" > ")
	}
	b.WriteString(d.Placeholder(existingArgs + 1))
	return []Value{req.After[0]}
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
	// P3-1：keyset 不可用时（多键排序/多值 cursor）必须回退 OFFSET，否则
	// 既无 keyset 谓词又无 OFFSET——查询从头返回，静默重复第一页。
	// 可用 keyset 时抑制 OFFSET（seek 模式不需要偏移）。
	if req.Page > 1 && !keysetApplicable(req) {
		offset = (req.Page - 1) * req.PageSize
	}
	if clause := d.LimitOffset(limit, offset); clause != "" {
		b.WriteString(" ")
		b.WriteString(clause)
	}
}
