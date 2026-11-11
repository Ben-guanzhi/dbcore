// Package ddl assembles CREATE TABLE scripts from catalog rows a client has
// already fetched. It is the single home of the reconstruction algorithm: which
// clauses a table's DDL carries, how defaults are normalized per engine, and
// how identifiers are qualified. Clients map their own catalog shapes onto the
// input structs here; none of them re-derives DDL text (INTEGRATION-PLAN P1-7).
//
// Like sqltext it is dependency-free — structs in, strings out — so it imports
// no database drivers. The output is a reading aid rather than a migration
// tool: engine-level storage clauses are intentionally left out.
package ddl

import (
	"fmt"
	"strings"

	"github.com/dbcore/dbcore/sqltext"
)

// Column is one column of the table being described.
type Column struct {
	Name         string
	Type         string
	Nullable     bool
	DefaultValue string
}

// Index is one index on the table; composite keys keep their column order.
type Index struct {
	Name    string
	Unique  bool
	Primary bool
	Columns []string
}

// ForeignKey is one constraint pointing at another table.
type ForeignKey struct {
	Name       string
	Columns    []string
	RefSchema  string
	RefTable   string
	RefColumns []string
	OnDelete   string
	OnUpdate   string
}

// CreateTable renders the full script for a table described by the given
// catalog rows: the CREATE TABLE statement (columns plus primary-key
// constraints), one CREATE INDEX per secondary index, and one ALTER TABLE per
// foreign key.
//
// statements := one CREATE TABLE (columns plus primary-key constraint), one
// CREATE INDEX per secondary index, and one ALTER TABLE per foreign key.
//
// FullTypes optionally overrides a column's reported type with the fuller
// spelling a separate query produced (engines such as MSSQL and Oracle report
// bare names through their column list, which would build invalid DDL).
//
// The sibling file alter.go carries the matching verbs for a table that already
// exists (ADD/DROP COLUMN, ALTER COLUMN, index and constraint changes); use
// those when synchronizing and this one when reconstructing.
func CreateTable(dbType, schema, table string, columns []Column, fullTypes map[string]string, indexes []Index, foreignKeys []ForeignKey) string {
	object := tableName(dbType, schema, table)

	definitions := make([]string, 0, len(columns)+1)
	for _, column := range columns {
		if full, ok := fullTypes[column.Name]; ok && full != "" {
			column.Type = full
		}
		definitions = append(definitions, "  "+columnDef(dbType, column))
	}

	statements := []string{}
	for _, index := range indexes {
		if !index.Primary {
			continue
		}
		definitions = append(definitions,
			"  CONSTRAINT "+ident(dbType, index.Name)+" PRIMARY KEY ("+columnsList(dbType, index.Columns)+")")
	}
	statements = append(statements, "CREATE TABLE "+object+" (\n"+strings.Join(definitions, ",\n")+"\n);")

	for _, index := range indexes {
		if index.Primary || len(index.Columns) == 0 {
			continue
		}
		unique := ""
		if index.Unique {
			unique = "UNIQUE "
		}
		statements = append(statements, fmt.Sprintf("CREATE %sINDEX %s ON %s (%s);",
			unique, ident(dbType, index.Name), object, columnsList(dbType, index.Columns)))
	}

	for _, fk := range foreignKeys {
		statements = append(statements,
			"ALTER TABLE "+object+" "+foreignKeyClause(dbType, fk)+";")
	}

	return strings.Join(statements, "\n\n")
}

// NormalizeDefault unwraps the parentheses MSSQL wraps around every default and
// trims the padding Oracle adds to expression text. Other engines pass through
// unchanged.
func NormalizeDefault(dbType, value string) string {
	if dbType == sqltext.SQLServer {
		for strings.HasPrefix(value, "(") && strings.HasSuffix(value, ")") {
			inner := strings.TrimSpace(value[1 : len(value)-1])
			if inner == "" {
				break
			}
			value = inner
		}
	}
	return value
}

// tableName qualifies only when a schema exists, because SQLite has no schema
// argument in the tree and "main" would be noise in the DDL.
func tableName(dbType, schema, table string) string {
	if schema == "" || schema == "main" {
		return ident(dbType, table)
	}
	return ident(dbType, schema) + "." + ident(dbType, table)
}

// ident quotes one identifier; an engine that cannot quote the name (unknown
// type, invalid characters) yields the bare name rather than failing the whole
// reading aid. The reading aid (exported DDL scripts for humans) is exactly
// where this fallback belongs; a caller that EXECUTES the rendered statement
// (structure edit) must run ValidatedIdent instead, because an unquotable
// attacker-controlled name becomes raw SQL there.
func ident(dbType, part string) string {
	quoted, err := sqltext.QuoteIdent(dbType, part)
	if err != nil {
		return part
	}
	return quoted
}

// ValidatedIdent is ident for execution paths: a name the engine cannot quote
// fails the render instead of landing in the statement bare. The structure
// editor runs every statement it renders, so a newline inside a column name
// (QuoteIdent rejects it) must abort the operation, not smuggle a second
// statement through the fallback.
func ValidatedIdent(dbType, part string) (string, error) {
	quoted, err := sqltext.QuoteIdent(dbType, part)
	if err != nil {
		return "", fmt.Errorf("identifier %q cannot be quoted for %s: %w", part, dbType, err)
	}
	return quoted, nil
}

// ValidateColumnType checks that a client-supplied type name is a bare type
// word (letters, digits, spaces, and at most one parameterized length). The
// structure editor renders it straight into `ALTER TABLE … ADD COLUMN <name>
// <type>`, so a separator inside the type (`int; DROP TABLE users;--`) would
// run a second statement. Types that legitimately carry punctuation (the
// parameter list) pass through the parenthesized-tail check; anything with a
// statement separator, a comment, or a quote is refused.
func ValidateColumnType(dbType, dataType string) error {
	t := strings.TrimSpace(dataType)
	if t == "" {
		return fmt.Errorf("column type is empty")
	}
	if len(t) > 128 {
		return fmt.Errorf("column type %q is too long", dataType)
	}
	for i := 0; i < len(t); i++ {
		c := t[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			// type word characters
		case c == ' ', c == '\t':
			// word separator (double precision, timestamp with time zone)
		case c == '(' || c == ')':
			// parameterized length: decimal(10,2), varchar(255)
		case c == ',':
			// parameter separator
		case c == '-', c == '_':
			// hyphenated/underscored names (time-tz, user_defined)
		default:
			return fmt.Errorf("column type %q contains character %q, which a type name cannot",
				dataType, string(c))
		}
	}
	// A parenthesized tail must be balanced and trailing.
	depth := 0
	lastClose := -1
	for i := 0; i < len(t); i++ {
		switch t[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return fmt.Errorf("column type %q has unbalanced parentheses", dataType)
			}
			lastClose = i
		}
	}
	if depth != 0 {
		return fmt.Errorf("column type %q has unbalanced parentheses", dataType)
	}
	// `--` is a SQL line comment: the validator's own doc promises to refuse
	// comments, but `-` is a legitimate hyphen character in type names
	// (time-tz), so a naive character scan lets `int--foo` through. The `--`
	// would swallow the rest of the DDL line — including `NOT NULL` and
	// subsequent column definitions — when rendered into ALTER TABLE.
	if strings.Contains(t, "--") {
		return fmt.Errorf("column type %q contains a comment (--), which a type name cannot", dataType)
	}
	if lastClose >= 0 && strings.TrimSpace(t[lastClose+1:]) != "" {
		return fmt.Errorf("column type %q has content after the parameter list", dataType)
	}
	_ = dbType
	return nil
}

// ValidateDefault checks that a client-supplied DEFAULT value cannot smuggle a
// statement separator. Defaults are rendered verbatim (`DEFAULT <value>`) — a
// `1); DROP TABLE users;--` default would close the column and run a second
// statement. Literal text is allowed only as a quoted literal or a bare
// numeric/keyword token; anything carrying a separator, a comment, or an
// unbalanced quote is refused.
func ValidateDefault(value string) error {
	v := strings.TrimSpace(value)
	if v == "" {
		// No default is the common case: columnDef only renders DEFAULT when
		// the value is non-empty, so an empty field is valid, not an error.
		return nil
	}
	if len(v) > 256 {
		return fmt.Errorf("default value %q is too long", value)
	}
	// Count unescaped single quotes: an odd count means an unterminated
	// literal, which swallows the rest of the statement.
	quotes := strings.Count(v, "'")
	if quotes%2 != 0 {
		return fmt.Errorf("default value %q has an unterminated string literal", value)
	}
	// Strip quoted literals first, then the remainder must be free of
	// separators and comments.
	stripped := stripQuotedLiterals(v)
	for i := 0; i < len(stripped); i++ {
		c := stripped[i]
		if c == ';' {
			return fmt.Errorf("default value %q contains a statement separator", value)
		}
		if c == '-' && i+1 < len(stripped) && stripped[i+1] == '-' {
			return fmt.Errorf("default value %q contains a comment", value)
		}
		if c == '/' && i+1 < len(stripped) && stripped[i+1] == '*' {
			return fmt.Errorf("default value %q contains a comment", value)
		}
	}
	// A closing paren before any opening one closes the column definition and
	// turns the tail into raw SQL.
	depth := 0
	for i := 0; i < len(stripped); i++ {
		switch stripped[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return fmt.Errorf("default value %q closes a parenthesis it did not open", value)
			}
		}
	}
	return nil
}

// stripQuotedLiterals replaces the content of every single-quoted literal with
// a placeholder, so separator scans see only the code between them. Doubled
// quotes (”) inside a literal are consumed as one.
func stripQuotedLiterals(v string) string {
	var b strings.Builder
	b.Grow(len(v))
	inQuote := false
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c != '\'' {
			if !inQuote {
				b.WriteByte(c)
			}
			continue
		}
		// A quote: either it opens/closes a literal, or it is a doubled quote
		// inside one.
		if i+1 < len(v) && v[i+1] == '\'' {
			i++ // doubled quote: consume both
			continue
		}
		inQuote = !inQuote
		if !inQuote {
			b.WriteByte('\'') // keep the closing delimiter visible
		}
	}
	return b.String()
}

func columnsList(dbType string, names []string) string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, ident(dbType, name))
	}
	return strings.Join(quoted, ", ")
}
