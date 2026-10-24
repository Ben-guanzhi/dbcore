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
	if ownLimitRe.MatchString(trimmed) {
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
	if ownLimitRe.MatchString(trimmed) {
		return query
	}
	if !strings.HasPrefix(upper, "SELECT") {
		return query
	}
	return fmt.Sprintf("SELECT TOP (%d)%s", limit, trimmed[len("SELECT"):])
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
	if ownLimitRe.MatchString(trimmed) {
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
