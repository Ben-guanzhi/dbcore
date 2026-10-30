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
	upper := strings.ToUpper(trimmed)
	if !strings.HasPrefix(upper, "SELECT") && !strings.HasPrefix(upper, "WITH") {
		return query
	}
	if queryOwnLimit(trimmed) {
		return query
	}
	return fmt.Sprintf("%s LIMIT %d", trimmed, limit)
}

func rowLimitSQLServer(query string, limit int) string {
	if limit <= 0 {
		return query
	}
	trimmed := strings.TrimRight(strings.TrimSpace(query), ";")
	upper := strings.ToUpper(trimmed)
	if !strings.HasPrefix(upper, "SELECT") && !strings.HasPrefix(upper, "WITH") {
		return query
	}
	if queryOwnLimit(trimmed) {
		return query
	}
	if !strings.HasPrefix(upper, "SELECT") {
		// A WITH statement: SQL Server accepts the CTE head inside a derived
		// table, so the TOP (n) cap applies exactly as to a SELECT — leaving
		// it untouched turned every unbounded CTE into a full-table scan.
		// Only read statements can be wrapped; data-modifying CTEs
		// (WITH … INSERT/UPDATE/DELETE) cannot and stay untouched.
		if cteOuterIsRead(trimmed) {
			return fmt.Sprintf("SELECT TOP (%d) * FROM (%s) AS limited", limit, trimmed)
		}
		return query
	}
	return fmt.Sprintf("SELECT TOP (%d)%s", limit, trimmed[len("SELECT"):])
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
		case c == '\\':
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
	upper := strings.ToUpper(trimmed)
	if !strings.HasPrefix(upper, "SELECT") && !strings.HasPrefix(upper, "WITH") {
		return query
	}
	if queryOwnLimit(trimmed) {
		return query
	}
	return fmt.Sprintf("SELECT * FROM (%s) WHERE ROWNUM <= %d", trimmed, limit)
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
func Normalize(dbType string) string {
	switch strings.ToLower(strings.TrimSpace(dbType)) {
	case "mariadb":
		return MySQL
	case "postgres", "pg":
		return Postgres
	case "sqlserver", "mssql", "sql server":
		return SQLServer
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
