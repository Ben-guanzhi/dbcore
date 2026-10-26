package sqltext

// Object search: one statement per engine that finds tables/views whose name
// contains a user-typed fragment. It lives in the kernel because the search box
// of every client asks exactly the same question, and because the per-engine
// differences (placeholder style, ILIKE vs LIKE, TOP vs ROWNUM) are the same
// differences already encoded in TablesQuery.

import (
	"fmt"
	"strings"
)

// SearchObjectsQuery returns a statement selecting (schema, name, type) for
// objects whose name matches fragment, capped at limit rows.
//
// The fragment is treated as literal text: LikePattern escapes %, _ and \ so a
// user typing "a_b" cannot match arbitrary characters. MySQL/PostgreSQL/SQLite
// bind the pattern; SQL Server and Oracle inline it as an escaped literal
// (NamePredicate's rationale: go-mssqldb and go-ora leave "?" unrewritten on
// this code path, so a bind would be sent literally).
func SearchObjectsQuery(dbType, database, fragment string, limit int) (string, []interface{}, error) {
	if limit <= 0 {
		return "", nil, fmt.Errorf("sqltext: search limit must be positive, got %d", limit)
	}
	pattern := LikePattern(fragment)
	switch Normalize(dbType) {
	case MySQL:
		return fmt.Sprintf(`SELECT TABLE_SCHEMA, TABLE_NAME, TABLE_TYPE
			FROM information_schema.TABLES
			WHERE TABLE_SCHEMA = ? AND TABLE_NAME LIKE ? ORDER BY TABLE_NAME LIMIT %d`, limit),
			[]interface{}{database, pattern}, nil
	case Postgres:
		return fmt.Sprintf(`SELECT n.nspname, c.relname, CASE WHEN c.relkind = 'v' THEN 'VIEW' ELSE 'TABLE' END
			FROM pg_class c
			INNER JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relkind IN ('r', 'v', 'm', 'p')
				AND n.nspname NOT IN ('pg_catalog', 'information_schema')
				AND c.relname ILIKE $1
			ORDER BY 1, 2 LIMIT %d`, limit),
			[]interface{}{pattern}, nil
	case SQLite:
		return fmt.Sprintf(`SELECT '', name, UPPER(type) FROM sqlite_master
			WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%%' AND name LIKE ?
			ORDER BY name LIMIT %d`, limit),
			[]interface{}{pattern}, nil
	case SQLServer:
		name, err := QuoteIdent(SQLServer, database)
		if err != nil {
			return "", nil, err
		}
		return fmt.Sprintf(`SELECT TOP (%d) TABLE_SCHEMA, TABLE_NAME, TABLE_TYPE
			FROM %s.INFORMATION_SCHEMA.TABLES
			WHERE TABLE_NAME LIKE %s ESCAPE '\' ORDER BY TABLE_NAME`,
			limit, name, QuotedLiteral(SQLServer, pattern)), nil, nil
	case Oracle:
		return fmt.Sprintf(`SELECT * FROM (
			SELECT OWNER, OBJECT_NAME, OBJECT_TYPE FROM ALL_OBJECTS
			WHERE OBJECT_TYPE IN ('TABLE', 'VIEW') AND UPPER(OBJECT_NAME) LIKE %s ESCAPE '\'
			ORDER BY OWNER, OBJECT_NAME)
			WHERE ROWNUM <= %d`, QuotedLiteral(Oracle, upperASCII(pattern)), limit), nil, nil
	default:
		return "", nil, fmt.Errorf("sqltext: unsupported engine %q for object search", dbType)
	}
}

// upperASCII upper-cases the ASCII letters of s without touching other bytes;
// Oracle compares OBJECT_NAME case-sensitively and stores it upper-cased.
func upperASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - ('a' - 'A')
		}
	}
	return string(b)
}

// SearchDataQuery returns a statement searching keyword through ONE row of a
// table: the columns listed (already qualified/quoted by the caller's
// QualifiedTable) OR'd with a LIKE test, capped at limit rows. The keyword is
// escaped as a literal fragment, never interpreted as a pattern.
//
// columns arrive as SQL fragments on purpose: which columns can hold readable
// text is a per-engine type question (ColumnType.IsDescriptive) the caller
// answers from the catalog, while HOW to spell the limit, the bind and the
// escape stays here. MySQL is case-insensitive by collation; PostgreSQL uses
// ILIKE so the search behaves like the object search next to it; the rest use
// LIKE with the engine's escape spelling.
func SearchDataQuery(dbType, table string, columns []string, keyword string, limit int) (string, []interface{}, error) {
	if limit <= 0 {
		return "", nil, fmt.Errorf("sqltext: search limit must be positive, got %d", limit)
	}
	if len(columns) == 0 {
		return "", nil, fmt.Errorf("sqltext: search needs at least one column")
	}
	pattern := LikePattern(keyword)
	conds := make([]string, len(columns))
	for i, col := range columns {
		conds[i] = col
	}
	switch Normalize(dbType) {
	case MySQL, SQLite:
		joined := ""
		var args []interface{}
		for i, col := range conds {
			if i > 0 {
				joined += " OR "
			}
			joined += col + " LIKE ?"
			args = append(args, pattern)
		}
		return fmt.Sprintf("SELECT * FROM %s WHERE %s LIMIT %d", table, joined, limit), args, nil
	case Postgres:
		var parts []string
		args := make([]interface{}, 0, len(conds)+1)
		for i, col := range conds {
			parts = append(parts, fmt.Sprintf("%s ILIKE $%d", col, i+1))
			args = append(args, pattern)
		}
		return fmt.Sprintf("SELECT * FROM %s WHERE %s LIMIT %d", table, strings.Join(parts, " OR "), limit), args, nil
	case SQLServer:
		// go-mssqldb leaves "?" unrewritten on literal paths (see
		// SearchObjectsQuery's note), so the pattern rides inline.
		for i, col := range conds {
			conds[i] = col + " LIKE " + QuotedLiteral(SQLServer, pattern) + ` ESCAPE '\'`
		}
		return fmt.Sprintf("SELECT TOP (%d) * FROM %s WHERE %s", limit, table, strings.Join(conds, " OR ")), nil, nil
	case Oracle:
		// ROWNUM caps the scan. The keyword is NOT upper-cased: identifiers
		// fold to upper in Oracle's catalog, but DATA is case-sensitive, and
		// silently searching "alice" as "ALICE" would miss rows.
		for i, col := range conds {
			conds[i] = col + " LIKE " + QuotedLiteral(Oracle, pattern) + ` ESCAPE '\'`
		}
		return fmt.Sprintf("SELECT * FROM %s WHERE (%s) AND ROWNUM <= %d",
			table, strings.Join(conds, " OR "), limit), nil, nil
	}
	return "", nil, fmt.Errorf("sqltext: unsupported engine %q for data search", dbType)
}
