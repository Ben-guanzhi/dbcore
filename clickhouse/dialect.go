package clickhouse

// dialect.go — the ClickHouse SQL dialect: quoting, placeholders and the
// paging/count builders the kernel's BrowseRows request needs.

import (
	"fmt"
	"strings"

	"github.com/Ben-guanzhi/dbcore"
)

// ClickHouseDialect implements dbcore.Dialect.
type ClickHouseDialect struct{}

// Kind implements dbcore.Dialect.
func (ClickHouseDialect) Kind() dbcore.DatabaseKind { return dbcore.KindClickHouse }

// QuoteIdent quotes identifiers with double quotes, which ClickHouse honours
// (a backtick also works, but the ANSI form is what the rest of the kernel
// reads).
func (ClickHouseDialect) QuoteIdent(parts ...string) string {
	quoted := make([]string, 0, len(parts))
	for _, p := range parts {
		quoted = append(quoted, dbcore.QuoteDoubleIdent(p))
	}
	return strings.Join(quoted, ".")
}

// Placeholder renders the nth (1-based) bind placeholder. ClickHouse's
// database/sql driver binds positional question marks.
func (ClickHouseDialect) Placeholder(idx int) string { return "?" }

// TypeName reports the column's declared type unchanged: ClickHouse's type
// text (Nullable(Int64), Array(String)) IS the type, and converting it to a
// SQL name would drop the wrapper the value's behaviour depends on.
func (ClickHouseDialect) TypeName(col dbcore.ColumnInfo) string { return col.Type }

// LimitOffset renders paging. ClickHouse accepts LIMIT n OFFSET m.
func (ClickHouseDialect) LimitOffset(limit, offset int) string {
	if limit <= 0 {
		return ""
	}
	if offset <= 0 {
		return fmt.Sprintf("LIMIT %d", limit)
	}
	return fmt.Sprintf("LIMIT %d OFFSET %d", limit, offset)
}

// BuildRowsQuery renders the page statement for a browse request.
func (l ClickHouseDialect) BuildRowsQuery(schema, table string, req *dbcore.TableRowsRequest) (string, []dbcore.Value) {
	var b strings.Builder
	b.WriteString("SELECT * FROM ")
	b.WriteString(l.QuoteIdent(schema, table))
	args := l.appendWhere(&b, req)
	l.appendOrder(&b, req)
	limit := req.PageSize
	if limit <= 0 {
		limit = 200
	}
	offset := 0
	if req.Page > 1 {
		offset = (req.Page - 1) * limit
	}
	if s := l.LimitOffset(limit, offset); s != "" {
		b.WriteString(" ")
		b.WriteString(s)
	}
	return b.String(), args
}

// BuildCountQuery renders the count statement for the same filters.
func (l ClickHouseDialect) BuildCountQuery(schema, table string, req *dbcore.TableRowsRequest) (string, []dbcore.Value) {
	var b strings.Builder
	b.WriteString("SELECT count() FROM ")
	b.WriteString(l.QuoteIdent(schema, table))
	args := l.appendWhere(&b, req)
	return b.String(), args
}

// appendWhere renders the filter expressions as bound predicates. Every value
// travels as a bind parameter, never spliced into the text.
func (l ClickHouseDialect) appendWhere(b *strings.Builder, req *dbcore.TableRowsRequest) []dbcore.Value {
	if req == nil || len(req.Filter) == 0 {
		return nil
	}
	args := make([]dbcore.Value, 0, len(req.Filter))
	b.WriteString(" WHERE ")
	for i, f := range req.Filter {
		if i > 0 {
			b.WriteString(" AND ")
		}
		col := l.QuoteIdent(f.Column)
		switch f.Op {
		case dbcore.FilterIsNull:
			b.WriteString(col + " IS NULL")
		case dbcore.FilterIsNotNull:
			b.WriteString(col + " IS NOT NULL")
		case dbcore.FilterLike:
			// ClickHouse spells LIKE the ANSI way; the pattern travels bound.
			b.WriteString(col + " LIKE ?")
			args = append(args, dbcore.Value{Kind: dbcore.ValText, S: f.Value.S})
		default:
			op := string(f.Op)
			if op == "" {
				op = "="
			}
			b.WriteString(col + " " + op + " ?")
			args = append(args, f.Value)
		}
	}
	return args
}

// filterArg converts a kernel Value into a bind argument.
func filterArg(v dbcore.Value) any {
	switch v.Kind {
	case dbcore.ValNull:
		return nil
	case dbcore.ValInt:
		return v.I
	case dbcore.ValFloat:
		return v.F
	case dbcore.ValBool:
		return v.Bool
	case dbcore.ValBlob:
		return v.B
	default:
		return v.S
	}
}

// appendOrder renders the sort keys.
func (l ClickHouseDialect) appendOrder(b *strings.Builder, req *dbcore.TableRowsRequest) {
	if req == nil || len(req.Sort) == 0 {
		return
	}
	b.WriteString(" ORDER BY ")
	for i, s := range req.Sort {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(l.QuoteIdent(s.Column))
		if s.Desc {
			b.WriteString(" DESC")
		} else {
			b.WriteString(" ASC")
		}
	}
}
