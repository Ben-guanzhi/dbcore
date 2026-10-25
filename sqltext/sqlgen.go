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
	"regexp"
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
)

// Engines lists every identifier the package knows how to generate for.
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

// ownLimitRe spots SQL that already caps its own rows. A false positive only
// means the statement runs untouched, which is the safer direction.
var ownLimitRe = regexp.MustCompile(`(?i)\b(top|limit|offset|fetch|rownum)\b`)

// queryOwnLimit reports whether the statement already caps its own rows. The
// regex runs on the statement with string literals, quoted identifiers,
// brackets and comments removed first: a column named `limit` or a literal
// like 'top n' must not suppress the row cap and turn a bounded page into a
// full-table read.
func queryOwnLimit(query string) bool {
	var sb strings.Builder
	i := 0
	for i < len(query) {
		c := query[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			i++
			for i < len(query) {
				if c == '\'' && query[i] == '\\' && i+1 < len(query) {
					i += 2
					continue
				}
				if query[i] == c {
					if i+1 < len(query) && query[i+1] == c {
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
			for i < len(query) && query[i] != ']' {
				i++
			}
			i++
		case c == '-' && i+1 < len(query) && query[i+1] == '-':
			for i < len(query) && query[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(query) && query[i+1] == '*':
			end := strings.Index(query[i+2:], "*/")
			if end < 0 {
				i = len(query)
			} else {
				i += 2 + end + 2
			}
		default:
			sb.WriteByte(c)
			i++
		}
	}
	return ownLimitRe.MatchString(sb.String())
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
	case Oracle:
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
	switch dbType {
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
	"serial": true, "bigserial": true, "smallserial": true,
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

// NewColumnType folds a raw declared type ("BIGINT UNSIGNED", "varchar(255)",
// "DOUBLE PRECISION") into its canonical base name ("bigint", "varchar",
// "double precision").
func NewColumnType(raw string) ColumnType {
	base := raw
	if i := strings.IndexByte(base, '('); i >= 0 {
		base = base[:i]
	}
	base = strings.ToLower(strings.TrimSpace(base))
	return ColumnType{raw: raw, base: base}
}

// Base returns the normalized type name.
func (t ColumnType) Base() string { return t.base }

// IsNumeric reports whether the column holds numbers. A trailing " unsigned"
// (MySQL's int family) is stripped before the lookup.
func (t ColumnType) IsNumeric() bool {
	return numericTypeNames[strings.TrimSuffix(t.base, " unsigned")]
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
// of folding the declared type again per client.
var seekableIntegerNames = map[string]bool{
	"int": true, "integer": true, "bigint": true, "smallint": true,
	"tinyint": true, "mediumint": true, "int2": true, "int4": true,
	"int8": true, "serial": true, "bigserial": true, "rowid": true,
	"unsigned big int": true,
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
	switch dbType {
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
