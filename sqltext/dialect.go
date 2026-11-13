package sqltext

import (
	"fmt"
	"strings"
)

// Dialect encapsulates per-engine SQL generation behavior.
// Adding a new engine means adding one entry to the dialects map.
type Dialect struct {
	QuoteIdent     func(name string) (string, error)
	QuoteLiteral   func(value string) string
	RowLimit       func(query string, limit int) string
	Truncate       func(quotedTable string) (string, error)
	LocalName      func(dbType string, schema, table string) (string, error)
	QualifiedTable func(dbType string, database, schema, table string) (string, error)
	CatalogSchema  func(database, schema string) string
	DefaultSchema  func(schema string) string
	KeysetEnabled  bool
}

var dialects = map[string]*Dialect{}

func init() {
	dialects[MySQL] = &Dialect{
		QuoteIdent:     quoteIdentMySQL,
		QuoteLiteral:   quoteLiteralMySQL,
		RowLimit:       rowLimitLimit,
		Truncate:       truncateTRUNCATE,
		LocalName:      localNameMySQL,
		QualifiedTable: qualifiedTableMySQL,
		CatalogSchema:  catalogSchemaMySQL,
		DefaultSchema:  defaultSchemaMySQL,
		KeysetEnabled:  true,
	}
	dialects[Postgres] = &Dialect{
		QuoteIdent:     quoteIdentDouble,
		QuoteLiteral:   quoteLiteralPlain,
		RowLimit:       rowLimitLimit,
		Truncate:       truncateTRUNCATE,
		LocalName:      localNameSchemaQualified,
		QualifiedTable: qualifiedTablePostgres,
		CatalogSchema:  catalogSchemaPostgres,
		DefaultSchema:  defaultSchemaPostgres,
		KeysetEnabled:  true,
	}
	dialects[SQLite] = &Dialect{
		QuoteIdent:     quoteIdentDouble,
		QuoteLiteral:   quoteLiteralPlain,
		RowLimit:       rowLimitLimit,
		Truncate:       truncateSQLite,
		LocalName:      localNameBare,
		QualifiedTable: qualifiedTableBare,
		CatalogSchema:  catalogSchemaIdentity,
		DefaultSchema:  defaultSchemaIdentity,
		KeysetEnabled:  true,
	}
	dialects[SQLServer] = &Dialect{
		QuoteIdent:     quoteIdentSQLServer,
		QuoteLiteral:   quoteLiteralSQLServer,
		RowLimit:       rowLimitSQLServer,
		Truncate:       truncateTRUNCATE,
		LocalName:      localNameSchemaQualified,
		QualifiedTable: qualifiedTableSQLServer,
		CatalogSchema:  catalogSchemaSQLServer,
		DefaultSchema:  defaultSchemaSQLServer,
		KeysetEnabled:  false,
	}
	dialects[Oracle] = &Dialect{
		QuoteIdent:     quoteIdentDouble,
		QuoteLiteral:   quoteLiteralPlain,
		RowLimit:       rowLimitOracle,
		Truncate:       truncateTRUNCATE,
		LocalName:      localNameBare,
		QualifiedTable: qualifiedTableBare,
		CatalogSchema:  catalogSchemaIdentity,
		DefaultSchema:  defaultSchemaIdentity,
		KeysetEnabled:  false,
	}

	// DM (达梦 DM8) in its default Oracle-compatibility mode takes the same
	// identifier quoting, ":n" binds and ROWNUM row limiting as Oracle, so the
	// DIALECT is shared. The data dictionary is NOT: DM's catalog objects are its
	// own, so the catalog recipes keep asking for Oracle only and a DM connection
	// never reaches them (it cannot be opened — see the engine's connect refusal in
	// the client, which names the missing driver).
	dialects[DM] = &Dialect{
		QuoteIdent:     quoteIdentDouble,
		QuoteLiteral:   quoteLiteralPlain,
		RowLimit:       rowLimitOracle,
		Truncate:       truncateTRUNCATE,
		LocalName:      localNameBare,
		QualifiedTable: qualifiedTableBare,
		CatalogSchema:  catalogSchemaIdentity,
		DefaultSchema:  defaultSchemaIdentity,
		KeysetEnabled:  false,
	}
}

// QuoteIdent helpers
func quoteIdentMySQL(name string) (string, error) {
	if name == "" || len(name) > 256 || strings.ContainsAny(name, "\x00\r\n") {
		return "", fmt.Errorf("invalid identifier: %q", name)
	}
	return "`" + strings.ReplaceAll(name, "`", "``") + "`", nil
}

func quoteIdentDouble(name string) (string, error) {
	if name == "" || len(name) > 256 || strings.ContainsAny(name, "\x00\r\n") {
		return "", fmt.Errorf("invalid identifier: %q", name)
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`, nil
}

func quoteIdentSQLServer(name string) (string, error) {
	if name == "" || len(name) > 256 || strings.ContainsAny(name, "\x00\r\n") {
		return "", fmt.Errorf("invalid identifier: %q", name)
	}
	return "[" + strings.ReplaceAll(name, "]", "]]") + "]", nil
}

// Loose identifier quoting: the same escaping as above, minus the validity
// guard (empty / overlong / control-carrying). Callers reach for it when the
// name is not theirs to validate (a table or column name read out of a
// catalog) and they still have to emit a syntactically valid identifier. The
// guard's rationale is "this name would fail at the engine anyway", but the
// ANSI double-quote fallback a caller would otherwise write is worse than a
// failure: MySQL parses "x" as a string literal, so `"t".col` silently
// changes the statement instead of erroring.
func quoteIdentLooseMySQL(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

func quoteIdentLooseDouble(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func quoteIdentLooseSQLServer(name string) string {
	return "[" + strings.ReplaceAll(name, "]", "]]") + "]"
}

// QuoteLiteral helpers
func quoteLiteralMySQL(value string) string {
	escaped := strings.ReplaceAll(value, "'", "''")
	return "'" + strings.ReplaceAll(escaped, `\`, `\\`) + "'"
}

func quoteLiteralPlain(value string) string {
	escaped := strings.ReplaceAll(value, "'", "''")
	return "'" + escaped + "'"
}

func quoteLiteralSQLServer(value string) string {
	escaped := strings.ReplaceAll(value, "'", "''")
	return "N'" + escaped + "'"
}

// RowLimit helpers
func rowLimitLimit(query string, limit int) string {
	if limit <= 0 {
		return query
	}
	trimmed := strings.TrimRight(strings.TrimSpace(query), ";")
	// Leading trivia is stripped for classification only; the cap is appended
	// to the statement the user wrote, comments included.
	lead := skipLeadingTrivia(trimmed)
	upper := strings.ToUpper(lead)
	if !strings.HasPrefix(upper, "SELECT") && !strings.HasPrefix(upper, "WITH") {
		return query
	}
	if ownLimitKind(trimmed) != "" {
		return query
	}
	if !strings.HasPrefix(upper, "SELECT") {
		// WITH statement: only a read CTE takes a trailing LIMIT — appending
		// one to WITH ... INSERT/UPDATE/DELETE truncates the write (MySQL and
		// PostgreSQL accept the spelling). The SQL Server arm has the same
		// guard through cteOuterIsRead.
		//
		// The verdict runs on `lead`, the same trivia-stripped text the
		// classification above used. A statement wrapped in leading
		// parentheses keeps its outer keyword at paren depth 2 in the raw
		// text, where the scan reported "no statement at depth 0" and left
		// every paren-wrapped read uncapped. The appending dialects carry the
		// cap for those (verified against MySQL and PostgreSQL servers); the
		// wrap arms below keep the raw scan, because their shapes add parens
		// of their own and a paren-wrapped WITH has not been shown legal
		// inside them.
		if cteOuterIsRead(lead) {
			return fmt.Sprintf("%s LIMIT %d", trimmed, limit)
		}
		return query
	}
	return fmt.Sprintf("%s LIMIT %d", trimmed, limit)
}

func rowLimitSQLServer(query string, limit int) string {
	if limit <= 0 {
		return query
	}
	trimmed := strings.TrimRight(strings.TrimSpace(query), ";")
	lead := skipLeadingTrivia(trimmed)
	upper := strings.ToUpper(lead)
	if !strings.HasPrefix(upper, "SELECT") && !strings.HasPrefix(upper, "WITH") {
		return query
	}
	if ownLimitKind(trimmed) != "" {
		return query
	}
	if !strings.HasPrefix(upper, "SELECT") {
		// A WITH statement: SQL Server accepts the CTE head inside a derived
		// table, so the TOP (n) cap applies exactly as to a SELECT — leaving
		// it untouched turned every unbounded CTE into a full-table scan.
		// Only read statements can be wrapped; data-modifying CTEs
		// (WITH … INSERT/UPDATE/DELETE) cannot and stay untouched.
		if cteOuterIsRead(trimmed) {
			return fmt.Sprintf("SELECT TOP (%d) * FROM (%s) AS limited", limit, stripLeadingSeparators(trimmed))
		}
		return query
	}
	// Insert TOP immediately after the SELECT keyword the trivia scan found,
	// keeping whatever trivia preceded it. Slicing trimmed[len("SELECT"):]
	// assumed SELECT sits at offset 0, which corrupts any statement with a
	// leading comment, hint, parenthesis or semicolon — the very input the
	// other engines' arms handle (TestRowLimitIgnoresLeadingTrivia).
	prefix := trimmed[:len(trimmed)-len(lead)]
	return fmt.Sprintf("%sSELECT TOP (%d)%s", prefix, limit, lead[len("SELECT"):])
}

// CteOuterIsRead is the exported face of cteOuterIsRead: consumers with
// their own LIMIT dialects (fynequery's auto-limit) need the same
// outermost-statement verdict so a WITH ... INSERT never gets a cap.
func CteOuterIsRead(s string) bool {
	return cteOuterIsRead(s)
}

// cteOuterIsRead reports whether a WITH statement's outermost statement -
// the keyword at paren depth 0 after the CTE list - is a SELECT. The scan
// skips string literals, quoted identifiers and comments; the outer keyword
// decides the statement kind. A data-modifying CTE (WITH ... INSERT/UPDATE/
// DELETE) cannot be wrapped in a derived table, so only reads are wrapped.
func cteOuterIsRead(s string) bool {
	depth := 0
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\'':
			// String literal: skip to the closing quote (doubled '' is the
			// T-SQL escaped quote, \' is the MySQL backslash escape). The
			// previous scan only fired on a leading backslash, so plain
			// literals were stepped through byte by byte and their parens
			// moved the depth counter — '(' inside a literal lifted it and
			// the outer SELECT was never found at depth 0, silently
			// defeating the TOP wrap.
			i++
			for i < len(s) {
				if s[i] == '\\' && i+1 < len(s) {
					i += 2
					continue
				}
				if s[i] == '\'' {
					if i+1 < len(s) && s[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case c == '"' || c == '`':
			// Quoted identifier (PG/Oracle/SQLite double-quote, MySQL
			// backtick). A DML keyword inside the quotes — WITH "delete"
			// AS (...) — must not be matched at depth 0, otherwise the
			// scanner returns false and the row limit is silently
			// skipped. Same skipping logic as ownLimitKind.
			q := c
			i++
			for i < len(s) {
				if s[i] == q {
					if i+1 < len(s) && s[i+1] == q {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case c == '[':
			// Bracket-quoted identifier (SQL Server [delete]). A doubled ]]
			// is the escaped literal ] — quoteIdentSQLServer emits it that
			// way — so the scan consumes it as part of the name, the same
			// way the double-quote branch above consumes "". Stopping at the
			// first ] exposed a keyword spelled inside an identifier at
			// depth 0: the CTE name [a]]DELETE] (the name a]DELETE) read as
			// "non-read" and lost its cap, and [a]]SELECT] over a
			// data-modifying CTE read as a read and was wrapped into a
			// statement that cannot execute.
			i++
			for i < len(s) {
				if s[i] == ']' {
					if i+1 < len(s) && s[i+1] == ']' {
						i += 2
						continue
					}
					break
				}
				i++
			}
			i++
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			if end := strings.Index(s[i:], "*/"); end >= 0 {
				i += end + 2
			} else {
				return false
			}
		case c == '(':
			depth++
			i++
		case c == ')':
			depth -= 1
			i++
		default:
			if depth == 0 {
				start := i
				for i < len(s) && isWordByte(s[i]) {
					i++
				}
				switch strings.ToUpper(s[start:i]) {
				case "SELECT":
					return true
				case "INSERT", "UPDATE", "DELETE", "MERGE":
					return false
				}
				if i == start {
					// A non-word byte at depth 0 (space, operator, newline,
					// non-ASCII): step over it. Without this the scan stalls
					// on the first one and never terminates.
					i++
				}
			} else {
				i++
			}
		}
	}
	return false
}

// isWordByte reports whether c may occur inside a SQL word. ASCII only: a
// non-ASCII run is stepped over one byte at a time and can never form a
// keyword.
func isWordByte(c byte) bool {
	return c == '_' || c == '$' ||
		(c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func rowLimitOracle(query string, limit int) string {
	if limit <= 0 {
		return query
	}
	trimmed := strings.TrimRight(strings.TrimSpace(query), ";")
	lead := skipLeadingTrivia(trimmed)
	upper := strings.ToUpper(lead)
	if !strings.HasPrefix(upper, "SELECT") && !strings.HasPrefix(upper, "WITH") {
		return query
	}
	if ownLimitKind(trimmed) != "" {
		return query
	}
	// A WITH statement is wrapped only when its outermost statement is a
	// read: wrapping WITH ... INSERT into a ROWNUM subquery produces SQL
	// that cannot execute (and would truncate the write if it could).
	if !strings.HasPrefix(upper, "SELECT") && !cteOuterIsRead(trimmed) {
		return query
	}
	return fmt.Sprintf("SELECT * FROM (%s) WHERE ROWNUM <= %d", stripLeadingSeparators(trimmed), limit)
}

// Truncate helpers
func truncateMySQL(quotedTable string) (string, error) {
	return "TRUNCATE TABLE " + quotedTable, nil
}

func truncateTRUNCATE(quotedTable string) (string, error) {
	return "TRUNCATE TABLE " + quotedTable, nil
}

func truncateSQLite(quotedTable string) (string, error) {
	return "DELETE FROM " + quotedTable, nil
}

// LocalName helpers
func localNameMySQL(dbType, schema, table string) (string, error) {
	return QuoteIdent(dbType, table)
}

func localNamePostgres(dbType, schema, table string) (string, error) {
	return QualifiedName(dbType, schema, table)
}

func localNameBare(dbType, schema, table string) (string, error) {
	return QuoteIdent(dbType, table)
}

func localNameSchemaQualified(dbType, schema, table string) (string, error) {
	return QualifiedName(dbType, schema, table)
}

// QualifiedTable helpers
func qualifiedTableMySQL(dbType, database, schema, table string) (string, error) {
	return QualifiedName(dbType, database, table)
}

func qualifiedTablePostgres(dbType, database, schema, table string) (string, error) {
	return QualifiedName(dbType, schema, table)
}

func qualifiedTableBare(dbType, database, schema, table string) (string, error) {
	return QualifiedName(dbType, table)
}

func qualifiedTableSQLServer(dbType, database, schema, table string) (string, error) {
	return QualifiedName(dbType, database, schema, table)
}

// CatalogSchema helpers
func catalogSchemaMySQL(database, schema string) string {
	return database
}

func catalogSchemaPostgres(database, schema string) string {
	return "public"
}

func catalogSchemaSQLServer(database, schema string) string {
	return "dbo"
}

func catalogSchemaIdentity(database, schema string) string {
	return schema
}

// DefaultSchema helpers
func defaultSchemaMySQL(schema string) string {
	return schema
}

func defaultSchemaPostgres(schema string) string {
	if schema != "" {
		return schema
	}
	return "public"
}

func defaultSchemaSQLServer(schema string) string {
	if schema != "" {
		return schema
	}
	return "dbo"
}

func defaultSchemaIdentity(schema string) string {
	return schema
}

// Normalize maps a caller's engine spelling onto the canonical identifier used
// by this package. Clients persist several spellings ("postgres"/"postgresql",
// "mssql"/"sqlserver", "mariadb"/"mysql"), so every entry point normalizes
// instead of requiring each caller to agree on one.
//
// 协议兼容引擎也在这里归一: OceanBase MySQL 模式 / GreatSQL / StarRocks / Doris(FE)
// 走 MySQL 线协议, openGauss / GaussDB / 人大金仓 KingbaseES 走 PostgreSQL 线协议。
// 它们的 SQL 方言、目录查询与占位符与底层引擎一致, 因此复用对应 dialect 即可,
// 客户端不需要为新引擎实现第二套方言。
func Normalize(dbType string) string {
	switch strings.ToLower(strings.TrimSpace(dbType)) {
	case "mariadb", "greatsql", "starrocks", "doris", "oceanbase":
		return MySQL
	case "postgres", "pg", "opengauss", "gaussdb", "kingbase", "kingbasees":
		return Postgres
	case "sqlserver", "mssql", "sql server":
		return SQLServer
	case "dameng", "dm8", "dm7", "dm6":
		return DM
	}
	return strings.ToLower(strings.TrimSpace(dbType))
}

// Kind maps an engine spelling onto dbcore.DatabaseKind's spelling
// ("postgres", "mssql", ...), which differs from the client-persisted
// identifiers in this package.
func Kind(dbType string) string {
	switch Normalize(dbType) {
	case Postgres:
		return "postgres"
	case SQLServer:
		return "mssql"
	default:
		return Normalize(dbType)
	}
}

// DialectFor returns the dialect for the given engine type.
// Adding a new engine means adding one entry to the dialects map.
func DialectFor(dbType string) (*Dialect, bool) {
	d, ok := dialects[Normalize(dbType)]
	return d, ok
}

func dialect(dbType string) (*Dialect, bool) {
	d, ok := dialects[Normalize(dbType)]
	return d, ok
}
