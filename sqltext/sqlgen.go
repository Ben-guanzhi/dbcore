// Package sqltext is the single home of the SQL *text* every database client in
// the workspace generates: identifier quoting, literals, row limiting, paging,
// table actions and catalog introspection for all five SQL engines. It is the
// dialect layer of dbcore; engine drivers and UI clients consume it instead of
// re-deriving per-engine strings.
//
// It is deliberately dependency-free — every function maps strings to
// strings — so it can be imported by clients without pulling in database
// drivers. Catalog *readers* (which scan rows into each client's own structs)
// stay with the clients.
//
// The engine identifier is the type tag; each function is the op side of the
// op-and-type table. Adding an engine means touching the switch arms below,
// not the callers.
package sqltext

import (
	"fmt"
	"strings"
)

// Engine identifiers, in the exact spelling both clients persist in their
// connection configs.
const (
	MySQL     = "mysql"
	Postgres  = "postgresql"
	SQLite    = "sqlite"
	SQLServer = "sqlserver"
	Oracle    = "oracle"
	// DM is 达梦 DM8. It is a KNOWN engine with a known dialect (Oracle's quoting,
	// ":n" binds and ROWNUM), but not a connectable one: DM speaks its own wire
	// protocol, which no vendored driver implements, so the client refuses to open
	// a connection and says why. Listing it here is what keeps `dm` from falling
	// into the "unknown engine" branch and producing a confusing error.
	DM = "dm"
)

// Engines lists every identifier the package knows how to generate for — the
// fully supported set. DM is deliberately absent: its DIALECT is known (quoting,
// ":n" binds, ROWNUM) but its DDL generators have never been checked against a
// server, and this list is a promise ("every listed engine answers every
// operation", enforced by TestEveryEngineAnswersEveryOperation). Listing DM would
// turn an unverified guess into a contract other packages rely on.
var Engines = []string{MySQL, Postgres, SQLite, SQLServer, Oracle}

// QuoteIdent quotes an identifier the way the engine parses it: MySQL wants
// backticks, SQL Server brackets, everything else the SQL standard's double
// quotes. Overlong or control-carrying names are rejected outright — they
// cannot be quoted safely and would only fail at the engine anyway.
func QuoteIdent(dbType, name string) (string, error) {
	d, ok := dialect(dbType)
	if !ok {
		return "", fmt.Errorf("unsupported type: %s", dbType)
	}
	return d.QuoteIdent(name)
}

// QuoteIdentLoose quotes an identifier with the dialect's native delimiters
// without QuoteIdent's validity guard: empty, overlong and control-carrying
// names are rejected by QuoteIdent, this renders them anyway.
//
// Reach for it only when the name is not the caller's to validate and a
// syntactically valid identifier must be produced regardless. The usual
// alternative — falling back to the ANSI double-quote spelling — is not merely
// invalid on some engines but changes the statement's meaning: MySQL parses
// `"x"` as a string literal, so `"t".col` is a type error at best and a
// silent mis-read at worst. For an unknown dialect it returns the ANSI form,
// matching QuoteIdent's failure shape.
func QuoteIdentLoose(dbType, name string) string {
	switch Normalize(dbType) {
	case MySQL:
		return quoteIdentLooseMySQL(name)
	case SQLServer:
		return quoteIdentLooseSQLServer(name)
	default: // PostgreSQL, SQLite, Oracle and anything unrecognised
		return quoteIdentLooseDouble(name)
	}
}

// QualifiedName quotes each part and joins them with the engine's qualifier.
func QualifiedName(dbType string, parts ...string) (string, error) {
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		q, err := QuoteIdent(dbType, part)
		if err != nil {
			return "", err
		}
		quoted = append(quoted, q)
	}
	return strings.Join(quoted, "."), nil
}

// LocalName spells a table the way the tab's own database resolves it:
// schema-qualified where the engine needs the schema to find the table,
// bare elsewhere. Same op-and-type table as QualifiedName, so no client
// hand-builds identifier shapes any more.
func LocalName(dbType, schema, table string) (string, error) {
	d, ok := dialect(dbType)
	if !ok {
		return "", fmt.Errorf("unsupported type: %s", dbType)
	}
	return d.LocalName(dbType, schema, table)
}

// QualifiedTable maps a reference to the fully-qualified spelling each engine
// wants: SQL Server wants database.schema.table, MySQL database.table,
// PostgreSQL schema.table, the rest just the table.
func QualifiedTable(dbType, database, schema, table string) (string, error) {
	d, ok := dialect(dbType)
	if !ok {
		return "", fmt.Errorf("unsupported type: %s", dbType)
	}
	return d.QualifiedTable(dbType, database, schema, table)
}

// QuotedLiteral renders a string as a single-quoted SQL literal. It serves
// every place the program inlines text — grid values, LIKE patterns, and
// string-argument positions such as sp_rename's parameters. Only SQL Server
// needs the national prefix to keep wide characters, and PostgreSQL rejects
// it, so the rest get a plain single-quoted literal.
func QuotedLiteral(dbType, value string) string {
	d, ok := dialect(dbType)
	if !ok {
		return "'" + strings.ReplaceAll(value, "'", "''") + "'"
	}
	return d.QuoteLiteral(value)
}

// skipLeadingTrivia strips whitespace, line/block comments and opening parens
// from the front of a statement so keyword classification sees the first real
// token. It mirrors dbcore.skipLeadingTrivia exactly: Oracle's optimizer hints
// make `/*+ ... */ SELECT` the everyday spelling of a read, and a `(SELECT …)`
// is a read too. Without the strip the row cap classifies those statements as
// "not a read" and silently no-ops, while IsReadQuery (which does strip) routes
// them into the capped branch — the two functions disagree on the same input.
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

// stripLeadingSeparators drops only the statement separators and whitespace at
// the front of a statement, keeping comments and parentheses. The appending
// dialects (LIMIT) can leave a leading `;` in place, but a wrapping dialect
// (ROWNUM, TOP ... FROM (...)) puts the text inside parentheses, where a
// stray separator is a syntax error: `SELECT * FROM (;SELECT ...)` never
// parses. Comments are not separators and must survive the wrap.
func stripLeadingSeparators(s string) string {
	return strings.TrimLeft(s, " \t\r\n;")
}

// ownLimitKind reports whether the outermost statement carries a row limit of
// its own, and which keyword spells it ("LIMIT", "TOP", "FETCH" or "ROWNUM");
// it returns "" when the statement carries none.
//
// The scan skips string literals, quoted identifiers, bracketed identifiers and
// comments, and it only matches at paren depth 0. That is the whole point: a
// LIMIT buried in a subquery caps the subquery, not the rows the caller sees, so
// `SELECT * FROM big WHERE id IN (SELECT id FROM small LIMIT 5)` must still get
// the outer cap. OFFSET is deliberately absent from the keyword set — it does
// not cap anything, so `SELECT * FROM items OFFSET 10` returns every remaining
// row and needs the cap.
//
// This replaces a whole-statement substring match on (top|limit|offset|fetch|
// rownum), which matched a LIMIT in a subquery, an OFFSET with no LIMIT, and a
// column aliased `top`, and skipped the cap in all three cases.
func ownLimitKind(query string) string {
	depth := 0
	i, n := 0, len(query)
	for i < n {
		c := query[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			q := c
			i++
			for i < n {
				if q == '\'' && query[i] == '\\' && i+1 < n {
					i += 2
					continue
				}
				if query[i] == q {
					if i+1 < n && query[i+1] == q {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case c == '[':
			i++
			for i < n && query[i] != ']' {
				i++
			}
			i++
		case c == '-' && i+1 < n && query[i+1] == '-':
			for i < n && query[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && query[i+1] == '*':
			if end := strings.Index(query[i+2:], "*/"); end < 0 {
				return "" // unterminated block comment: be conservative
			} else {
				i += 2 + end + 2
			}
		case c == '(':
			depth++
			i++
		case c == ')':
			if depth == 0 {
				return "" // unbalanced: be conservative
			}
			depth--
			i++
		case depth == 0 && isWordByte(c):
			j := i
			for j < n && isWordByte(query[j]) {
				j++
			}
			word := strings.ToUpper(query[i:j])
			switch word {
			case "FETCH", "ROWNUM":
				return word
			case "LIMIT":
				// LIMIT must be followed by a count, a parenthesised count or a
				// bind parameter. A bare "limit" is an identifier (`SELECT *
				// FROM t AS limit`), and LIMIT ALL caps nothing.
				if k := skipSpaces(query, j); k < n {
					switch {
					case query[k] >= '0' && query[k] <= '9',
						query[k] == '(', query[k] == '?', query[k] == ':', query[k] == '$':
						return word
					}
				}
			case "TOP":
				// T-SQL TOP is followed by "(" or a count. A bare "top" is an
				// identifier (`SELECT * FROM t AS top`) and caps nothing.
				if k := skipSpaces(query, j); k < n &&
					(query[k] == '(' || query[k] >= '0' && query[k] <= '9') {
					return word
				}
			}
			i = j
		default:
			i++
		}
	}
	return ""
}

// skipSpaces advances past ASCII whitespace; it returns the index of the next
// non-space byte, or n when the statement ends in whitespace.
func skipSpaces(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}

// RowLimit caps a plain SELECT — or a WITH ... SELECT CTE — the way GUI
// clients do: TOP for SQL Server, LIMIT for MySQL/PostgreSQL/SQLite, a ROWNUM
// wrapper for Oracle. Statements with their own cap, and anything that is not
// a SELECT, run untouched.
func RowLimit(dbType, query string, limit int) string {
	d, ok := dialect(dbType)
	if !ok {
		return query
	}
	return d.RowLimit(query, limit)
}

// Paging is deliberately NOT shared here: the Fyne client pairs the window
// clause with a pageability guard (own-window refusal, page/size bounds) that
// is client-level policy, and the root client builds pages inline around its
// own ORDER BY handling. The raw window renderers stay with the clients.

// Placeholder renders the idx-th (1-based) bind parameter the way the engine's
// own driver expects: "?" for MySQL/SQLite, "$n" for PostgreSQL, "@pn" for
// SQL Server, ":n" for Oracle. Values must ride as bind arguments alongside
// the statement (contract P0-4); this spells only the marker. Unknown engines
// get "?", the database/sql default.
func Placeholder(dbType string, idx int) string {
	switch Normalize(dbType) {
	case Postgres:
		return fmt.Sprintf("$%d", idx)
	case SQLServer:
		return fmt.Sprintf("@p%d", idx)
	case Oracle, DM:
		// DM8 in Oracle-compatibility mode binds with ":n", like Oracle. This is a
		// DIALECT fact, not a catalog claim: it is the one DM spelling that has to
		// match here or every generated statement would be rejected.
		return fmt.Sprintf(":%d", idx)
	default:
		return "?"
	}
}

// TruncateStatement empties a table. SQLite has no TRUNCATE; deleting every
// row is what its own CLI does, and it keeps the sqlite_sequence counter
// alone. `quotedTable` must already carry the engine's quoting — the caller
// built it through QualifiedName/QuoteIdent with its own namespace rules.
func TruncateStatement(dbType, quotedTable string) (string, error) {
	d, ok := dialect(dbType)
	if !ok {
		return "", fmt.Errorf("unsupported type: %s", dbType)
	}
	return d.Truncate(quotedTable)
}

// RenameStatement renders a table rename. `current` and `next` are the fully
// qualified old and new names as the caller's namespace rules dictate (MySQL
// names both sides in full so the table cannot drift into another database on
// the way). Only statement shape lives here; name validation stays with the
// caller, which owns the user-facing error text.
func RenameStatement(dbType, current, next, schema, oldTable, newName string) (string, error) {
	// Normalize before switching: "postgresql" spelling must reach the
	// Postgres arm, not the unsupported error below.
	switch Normalize(dbType) {
	case MySQL:
		return fmt.Sprintf("RENAME TABLE %s TO %s", current, next), nil
	case SQLServer:
		// sp_rename takes text, not identifiers: brackets in the string mean
		// an escaped name rather than a quoted one.
		return fmt.Sprintf("EXEC sp_rename %s, %s",
			QuotedLiteral(dbType, DefaultTableSchema(dbType, schema)+"."+oldTable),
			QuotedLiteral(dbType, newName)), nil
	case Postgres, Oracle, SQLite:
		quotedNew, err := QuoteIdent(dbType, newName)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("ALTER TABLE %s RENAME TO %s", current, quotedNew), nil
	}
	return "", fmt.Errorf("unsupported type: %s", dbType)
}

// FKValuesQuery renders the DISTINCT candidate-keys query for an FK dropdown.
// Ordering on the key is what makes consecutive reads agree, and DISTINCT
// only allows it because the key is in the select list. `name` must already
// be qualified and quoted by the caller.
func FKValuesQuery(dbType, name, refColumn, labelColumn string, limit int) (string, error) {
	if limit <= 0 {
		return "", fmt.Errorf("invalid limit %d", limit)
	}
	key, err := QuoteIdent(dbType, refColumn)
	if err != nil {
		return "", err
	}
	selected := key
	if labelColumn != "" {
		label, err := QuoteIdent(dbType, labelColumn)
		if err != nil {
			return "", err
		}
		selected += ", " + label
	}
	switch dbType {
	case MySQL, Postgres, SQLite:
		return fmt.Sprintf("SELECT DISTINCT %s FROM %s ORDER BY %s LIMIT %d", selected, name, key, limit), nil
	case SQLServer, Oracle:
		return fmt.Sprintf(
			"SELECT DISTINCT %s FROM %s ORDER BY %s OFFSET 0 ROWS FETCH NEXT %d ROWS ONLY",
			selected, name, key, limit), nil
	}
	return "", fmt.Errorf("unsupported type: %s", dbType)
}

// FKRow is the flat, one-row-per-column shape the FK catalogs return for a
// composite key; the aggregated constraint lists are what the UI and the DDL
// want. Each client scans its engine's catalog rows into one FKRow per column
// pair, then folds the rows into its own constraint type.
type FKRow struct {
	Name      string
	Column    string
	RefSchema string
	RefTable  string
	RefColumn string
	OnDelete  string
	OnUpdate  string
}

// ReferenceAction renders a catalog reference action ("no_action", "CASCADE",
// "RESTRICT") the way it is written in DDL: upper case, whitespace trimmed,
// underscores as spaces.
func ReferenceAction(raw string) string {
	return strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(raw)), "_", " ")
}

// CatalogSchema names the namespace catalog queries run against inside one
// database. Engines without a schema layer answer with the database itself;
// callers only ask while the schema is still empty.
func CatalogSchema(dbType, database, schema string) string {
	d, ok := dialect(dbType)
	if !ok {
		return schema
	}
	return d.CatalogSchema(database, schema)
}

// DefaultTableSchema fills the engine's default table namespace for qualified
// names, independent of which database the session is in.
func DefaultTableSchema(dbType, schema string) string {
	d, ok := dialect(dbType)
	if !ok {
		return schema
	}
	return d.DefaultSchema(schema)
}

// LikePattern wraps the fragment for a LIKE test, escaping the wildcards the
// user typed so "a_b" cannot stand for any three characters.
func LikePattern(fragment string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(fragment)
	return "%" + escaped + "%"
}

// numericTypeNames is the closed set of declared column-type names that mean
// "this column holds a number". Every client that renders values against a
// declared type (grid edits, filters, exports) asks ColumnType.IsNumeric, so a
// new spelling (say, another engine's synonym for BIGINT) is added here once
// instead of per client.
var numericTypeNames = map[string]bool{
	"int": true, "integer": true, "int2": true, "int4": true, "int8": true,
	"tinyint": true, "smallint": true, "mediumint": true, "bigint": true,
	"big int": true,
	"serial":  true, "bigserial": true, "smallserial": true,
	"decimal": true, "dec": true, "numeric": true, "fixed": true,
	"float": true, "float4": true, "float8": true, "double": true,
	"double precision": true, "real": true, "money": true, "smallmoney": true,
	"number": true, "binary_float": true, "binary_double": true, "year": true,
}

// ColumnType is a declared column type reduced to the single normalized form
// every consumer keys off. The constructor does the fold once — drop any
// parenthesized precision, trim, lowercase — so callers never re-derive it.
// The predicate methods (IsNumeric, IsSeekable, IsInteger, IsDescriptive) are
// the only places engine type-name knowledge lives; a new spelling is added to
// the relevant set here, not per client.
type ColumnType struct {
	raw  string
	base string
}

// typeModifiers are the MySQL column-type modifiers that change a numeric
// type's range or display without changing what it is: `unsigned` widens the
// positive range, `zerofill` pads to the display width. Both are separate
// words and both orders occur in practice — `bigint unsigned` from
// information_schema.COLUMN_TYPE, `UNSIGNED BIGINT` from go-sql-driver's
// DatabaseTypeName — so the fold must drop them as words rather than stripping
// only a trailing suffix. Dropping them here (once, in the constructor) is what
// keeps IsNumeric, IsSeekable and IsInteger from answering differently about the
// same declared type.
var typeModifiers = map[string]bool{"unsigned": true, "zerofill": true}

// NewColumnType folds a raw declared type ("BIGINT UNSIGNED", "varchar(255)",
// "DOUBLE PRECISION") into its canonical base name ("bigint", "varchar",
// "double precision"). Modifier words are dropped in any position, so
// "unsigned big int", "bigint unsigned" and "UNSIGNED BIGINT" all fold to the
// same base.
func NewColumnType(raw string) ColumnType {
	base := raw
	if i := strings.IndexByte(base, '('); i >= 0 {
		base = base[:i]
	}
	base = strings.ToLower(strings.TrimSpace(base))
	words := strings.Fields(base)
	kept := words[:0]
	for _, w := range words {
		if typeModifiers[w] {
			continue
		}
		kept = append(kept, w)
	}
	return ColumnType{raw: raw, base: strings.Join(kept, " ")}
}

// Base returns the normalized type name.
func (t ColumnType) Base() string { return t.base }

// IsNumeric reports whether the column holds numbers. Modifier words such as
// "unsigned" are already dropped by NewColumnType, so the base is the canonical
// type name and this is a plain set lookup.
func (t ColumnType) IsNumeric() bool {
	return numericTypeNames[t.base]
}

// TypeIsNumeric answers IsNumeric for a raw declared type without building a
// ColumnType: the fold this needs (lowercase, drop modifier words) is
// allocation-free while the declared type is already lowercase and carries no
// modifier word — which catalog types in practice are. Hot loops that ask the
// same column's type once per row (a 10 000-row export asks it 10 000 times)
// call this through a per-column precomputation instead.
func TypeIsNumeric(raw string) bool {
	base := raw
	if i := strings.IndexByte(base, '('); i >= 0 {
		base = base[:i]
	}
	base = strings.ToLower(strings.TrimSpace(base))
	// A modifier word anywhere in the name changes the fold: drop "unsigned"
	// and "zerofill" wherever they appear (mirrors NewColumnType). The
	// fast path — the overwhelmingly common shape, a lowercase type without
	// modifier words — checks for the words without allocating.
	if !strings.Contains(base, "unsigned") && !strings.Contains(base, "zerofill") {
		return numericTypeNames[base]
	}
	words := strings.Fields(base)
	clean := make([]string, 0, len(words))
	for _, w := range words {
		if w == "unsigned" || w == "zerofill" {
			continue
		}
		clean = append(clean, w)
	}
	return numericTypeNames[strings.Join(clean, " ")]
}

// dateTypeNames is the closed set of declared column-type names that hold a
// date or a day-stamped time. The timestamp family is matched by prefix in
// IsDate because PostgreSQL spells the time-zone variants out, so it is not
// listed here. A bare "time" is deliberately absent: it has no day to sort on.
var dateTypeNames = map[string]bool{
	"date": true, "datetime": true, "datetime2": true, "smalldatetime": true,
}

// IsDate reports whether the column holds a date or a day-stamped time.
func (t ColumnType) IsDate() bool {
	if strings.HasPrefix(t.base, "timestamp") {
		return true
	}
	return dateTypeNames[t.base]
}

// seekableIntegerNames is the whole-number half of the seek gate: a keyset
// cursor binds integers as numbers and text as strings, so the caller needs
// the split as much as the gate itself. Both halves ask this one set instead
// of folding the declared type again per client. Modifier words (unsigned,
// zerofill) are already dropped by NewColumnType, so the set holds canonical
// names only — `unsigned big int` and `bigint unsigned` both fold to `bigint`.
var seekableIntegerNames = map[string]bool{
	"int": true, "integer": true, "bigint": true, "smallint": true,
	"tinyint": true, "mediumint": true, "int2": true, "int4": true,
	"int8": true, "serial": true, "bigserial": true, "rowid": true,
	"big int": true,
}

// IsSeekable reports whether the type can serve as a keyset cursor: whole
// numbers and text round-trip exactly; floats, decimals, dates and binaries
// do not, so they are refused rather than guessed at.
func (t ColumnType) IsSeekable() bool {
	if seekableIntegerNames[t.base] {
		return true
	}
	switch t.base {
	case "char", "varchar", "nchar", "nvarchar", "varchar2", "nvarchar2",
		"text", "tinytext", "mediumtext", "longtext", "clob",
		"character", "character varying":
		return true
	}
	return false
}

// IsInteger reports which of the two seekable halves the type belongs to: the
// whole numbers bind as numbers, the text half as strings, and the cursor
// encoding differs between them. Outside the gate there is no cursor at all,
// so this is only asked after IsSeekable said yes.
func (t ColumnType) IsInteger() bool {
	return seekableIntegerNames[t.base]
}

// IsDescriptive reports whether the column holds something a person reads.
// Numbers, dates and blobs describe a row only to whoever already knows it.
func (t ColumnType) IsDescriptive() bool {
	base := t.base
	if strings.HasSuffix(base, "text") || strings.HasSuffix(base, "char") {
		return true
	}
	switch base {
	case "varchar", "nvarchar", "character", "character varying", "citext", "string",
		"uuid", "uniqueidentifier", "name", "tinytext", "json", "jsonb":
		return true
	}
	return false
}

// CreateTableTemplate renders the starter CREATE TABLE for "新建表": the
// smallest table this engine accepts, so the user edits a name and a column
// instead of recalling each dialect's identity syntax. `schema` fills the
// engine's own default namespace.
func CreateTableTemplate(dbType, schema string) string {
	// Normalize before switching: callers persist their own spellings
	// ("postgresql", "mssql", "mariadb" — see Normalize), and a raw-spelling
	// switch silently fell through to "" for those, rendering an empty
	// starter DDL instead of the engine's template.
	switch Normalize(dbType) {
	case MySQL:
		// MySQL addresses tables by database, which the session already
		// carries, so the template names the table bare.
		return strings.Join([]string{
			"CREATE TABLE `new_table` (",
			"  `id` INT NOT NULL AUTO_INCREMENT,",
			"  `name` VARCHAR(255) NULL,",
			"  `created_at` DATETIME NULL,",
			"  PRIMARY KEY (`id`)",
			") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;",
		}, "\n")
	case Postgres:
		return strings.Join([]string{
			"CREATE TABLE " + mustQuote(dbType, DefaultTableSchema(dbType, schema)) + `."new_table" (`,
			"  id BIGSERIAL PRIMARY KEY,",
			"  name TEXT,",
			"  created_at TIMESTAMPTZ",
			");",
		}, "\n")
	case SQLServer:
		return strings.Join([]string{
			"CREATE TABLE " + mustQuote(dbType, DefaultTableSchema(dbType, schema)) + ".[new_table] (",
			"  [id] INT IDENTITY(1,1) NOT NULL PRIMARY KEY,",
			"  [name] NVARCHAR(255) NULL,",
			"  [created_at] DATETIME2 NULL",
			");",
		}, "\n")
	case Oracle:
		// Oracle's upper-case identifier convention, and the identity column
		// that makes a bare row insertable.
		return strings.Join([]string{
			`CREATE TABLE "NEW_TABLE" (`,
			`  "ID" NUMBER GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,`,
			`  "NAME" VARCHAR2(255),`,
			`  "CREATED_AT" TIMESTAMP`,
			");",
		}, "\n")
	case SQLite:
		return strings.Join([]string{
			`CREATE TABLE "new_table" (`,
			`  "id" INTEGER PRIMARY KEY AUTOINCREMENT,`,
			`  "name" TEXT,`,
			`  "created_at" TEXT`,
			");",
		}, "\n")
	}
	return ""
}

// mustQuote is QuoteIdent for template contexts: the schema comes from the
// catalog, and a name the quoter rejects would have failed long before the
// template is rendered. The empty string keeps the bare-table engines bare.
func mustQuote(dbType, name string) string {
	if name == "" {
		return ""
	}
	q, err := QuoteIdent(dbType, name)
	if err != nil {
		return name
	}
	return q
}
