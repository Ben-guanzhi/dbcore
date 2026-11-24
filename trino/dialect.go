package trino

// dialect.go — the Trino SQL dialect: quoting, placeholders and the builders
// for the kernel's paging/count request.

import (
	"fmt"
	"strings"

	"github.com/Ben-guanzhi/dbcore"
)

// TrinoDialect implements dbcore.Dialect.
type TrinoDialect struct{}

// Kind implements dbcore.Dialect.
func (TrinoDialect) Kind() dbcore.DatabaseKind { return dbcore.KindTrino }

// QuoteIdent quotes identifiers with double quotes, joined by dots.
func (TrinoDialect) QuoteIdent(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, dbcore.QuoteDoubleIdent(p))
	}
	return strings.Join(out, ".")
}

// Placeholder renders the nth (1-based) bind placeholder; Trino's client binds
// positional question marks.
func (TrinoDialect) Placeholder(idx int) string { return "?" }

// TypeName reports the column's declared type verbatim.
func (TrinoDialect) TypeName(col dbcore.ColumnInfo) string { return col.Type }

// LimitOffset renders paging.
func (TrinoDialect) LimitOffset(limit, offset int) string {
	if limit <= 0 {
		return ""
	}
	if offset <= 0 {
		return fmt.Sprintf("LIMIT %d", limit)
	}
	return fmt.Sprintf("LIMIT %d OFFSET %d", limit, offset)
}

// BuildRowsQuery renders the page statement. spec is "catalog" or
// "catalog.schema"; tableRef resolves it to a fully qualified name.
func (l TrinoDialect) BuildRowsQuery(spec, table string, req *dbcore.TableRowsRequest) (string, []dbcore.Value) {
	var b strings.Builder
	b.WriteString("SELECT * FROM ")
	b.WriteString(tableRef(spec, table))
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
func (l TrinoDialect) BuildCountQuery(spec, table string, req *dbcore.TableRowsRequest) (string, []dbcore.Value) {
	var b strings.Builder
	b.WriteString("SELECT count(*) FROM ")
	b.WriteString(tableRef(spec, table))
	args := l.appendWhere(&b, req)
	return b.String(), args
}

// appendWhere renders the filters as bound predicates: a value never reaches
// the SQL text.
func (l TrinoDialect) appendWhere(b *strings.Builder, req *dbcore.TableRowsRequest) []dbcore.Value {
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

// appendOrder renders the sort keys.
func (l TrinoDialect) appendOrder(b *strings.Builder, req *dbcore.TableRowsRequest) {
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
