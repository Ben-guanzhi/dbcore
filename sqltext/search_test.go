package sqltext

import (
	"strings"
	"testing"
)

func TestSearchObjectsQueryBindsPatternWhereDriverSupportsIt(t *testing.T) {
	for _, dbType := range []string{MySQL, Postgres, SQLite} {
		q, args, err := SearchObjectsQuery(dbType, "app", "cust_%", 25)
		if err != nil {
			t.Fatalf("%s: %v", dbType, err)
		}
		placeholders := strings.Count(q, "?") + strings.Count(q, "$")
		if placeholders != len(args) {
			t.Errorf("%s: %d placeholders but %d args: %s", dbType, placeholders, len(args), q)
		}
		if len(args) == 0 || args[len(args)-1] != `%cust\_\%%` {
			t.Errorf("%s: pattern not escaped: %v", dbType, args)
		}
		if !strings.Contains(q, "25") {
			t.Errorf("%s: limit missing from query: %s", dbType, q)
		}
	}
}

// An empty database must not pin the search to one schema: on a server with
// many schemas that is exactly the case where a table cannot be found.
func TestSearchObjectsQueryWithoutDatabaseSearchesEverySchema(t *testing.T) {
	q, args, err := SearchObjectsQuery(MySQL, "", "order", 20)
	if err != nil {
		t.Fatalf("MySQL: %v", err)
	}
	if strings.Contains(q, "TABLE_SCHEMA = ?") {
		t.Fatalf("an unscoped search still filters by schema:\n%s", q)
	}
	if len(args) != 1 {
		t.Fatalf("args = %v, want just the pattern", args)
	}
	// Ordering by schema keeps a many-schema result readable.
	if !strings.Contains(q, "TABLE_SCHEMA, TABLE_NAME") {
		t.Errorf("the unscoped search is not ordered by schema: %s", q)
	}
	// A chosen schema still narrows it.
	q, args, err = SearchObjectsQuery(MySQL, "gs_prod", "order", 20)
	if err != nil {
		t.Fatalf("MySQL scoped: %v", err)
	}
	if !strings.Contains(q, "TABLE_SCHEMA = ?") || len(args) != 2 {
		t.Fatalf("a scoped search did not filter by schema: %s %v", q, args)
	}
}

func TestSearchObjectsQueryInlinesLiteralForMSSQLAndOracle(t *testing.T) {
	for _, dbType := range []string{SQLServer, Oracle} {
		q, args, err := SearchObjectsQuery(dbType, "app", "ord", 10)
		if err != nil {
			t.Fatalf("%s: %v", dbType, err)
		}
		if len(args) != 0 {
			t.Errorf("%s: expected inlined literal, got args %v", dbType, args)
		}
		if strings.Contains(q, "?") || strings.Contains(q, "$1") {
			t.Errorf("%s: bind marker leaked into %s", dbType, q)
		}
		if !strings.Contains(strings.ToUpper(q), "%ORD%") {
			t.Errorf("%s: pattern missing: %s", dbType, q)
		}
	}
}

func TestSearchObjectsQueryUpperCasesOraclePattern(t *testing.T) {
	q, _, err := SearchObjectsQuery(Oracle, "app", "cust", 5)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q, "%CUST%") {
		t.Fatalf("Oracle pattern should be upper-cased: %s", q)
	}
}

func TestSearchObjectsQueryRejectsBadInput(t *testing.T) {
	if _, _, err := SearchObjectsQuery(MySQL, "app", "x", 0); err == nil {
		t.Error("limit 0 must be rejected")
	}
	if _, _, err := SearchObjectsQuery("duckdb", "app", "x", 5); err == nil {
		t.Error("unknown engine must be rejected")
	}
}

// ---- data search (P2-10) ----------------------------------------------------

// Binds on the engines whose drivers rewrite them, inline literals on the
// engines whose drivers do not (the same split SearchObjectsQuery obeys).
func TestSearchDataQueryPerEngine(t *testing.T) {
	cases := []struct {
		dbType  string
		binds   int
		want    []string
		notWant string
	}{
		{"mysql", 2, []string{"SELECT * FROM `t` WHERE `a` LIKE ?", "`b` LIKE ?", "LIMIT 50"}, ""},
		{"sqlite", 2, []string{`SELECT * FROM "t" WHERE "a" LIKE ?`, "LIMIT 50"}, ""},
		{"postgresql", 2, []string{"ILIKE $1", "ILIKE $2", "LIMIT 50"}, "LIKE ?"},
		{"sqlserver", 0, []string{"SELECT TOP (50) *", "[a] LIKE N'%x%'"}, "LIKE ?"},
		{"oracle", 0, []string{`"a" LIKE '%x%'`, "ROWNUM <= 50"}, "FETCH"},
	}
	for _, c := range cases {
		table := quoted(t, c.dbType, "t")
		cols := []string{quoted(t, c.dbType, "a"), quoted(t, c.dbType, "b")}
		q, args, err := SearchDataQuery(c.dbType, table, cols, "x", 50)
		if err != nil {
			t.Fatalf("%s: %v", c.dbType, err)
		}
		if len(args) != c.binds {
			t.Errorf("%s: binds = %d (%v), want %d", c.dbType, len(args), args, c.binds)
		}
		for _, want := range c.want {
			if !strings.Contains(q, want) {
				t.Errorf("%s: query misses %q:\n%s", c.dbType, want, q)
			}
		}
		if c.notWant != "" && strings.Contains(q, c.notWant) {
			t.Errorf("%s: query contains %q:\n%s", c.dbType, c.notWant, q)
		}
	}
}

// The keyword is literal text: % and _ survive as themselves, never wildcards.
func TestSearchDataQueryEscapesKeyword(t *testing.T) {
	q, args, err := SearchDataQuery("postgresql", `"t"`, []string{`"c"`}, "50%_off", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 1 {
		t.Fatalf("args = %v, want the escaped pattern", args)
	}
	if got, _ := args[0].(string); got != `%50\%\_off%` {
		t.Errorf("pattern = %q, want escaped 50%%_off", got)
	}
	if !strings.Contains(q, "LIMIT 10") {
		t.Errorf("pg query lost the cap:\n%s", q)
	}

	q, _, err = SearchDataQuery("sqlserver", "[t]", []string{"[c]"}, "100%", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q, `N'%100[%]%'`) {
		t.Errorf("mssql pattern not escaped to bracket classes:\n%s", q)
	}
	if strings.Contains(q, "ESCAPE") {
		t.Errorf("T-SQL has no ESCAPE clause:\n%s", q)
	}
}

// Oracle data is case-sensitive: upper-casing the keyword (as the object
// search does for identifiers) would silently miss lowercase rows.
func TestSearchDataQueryKeepsOracleCase(t *testing.T) {
	q, _, err := SearchDataQuery("oracle", `"T"`, []string{`"C"`}, "alice", 20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q, "'%alice%'") {
		t.Errorf("oracle keyword was upper-cased:\n%s", q)
	}
}

func TestSearchDataQueryGuards(t *testing.T) {
	if _, _, err := SearchDataQuery("mysql", "t", nil, "x", 10); err == nil {
		t.Error("no columns accepted")
	}
	if _, _, err := SearchDataQuery("mysql", "t", []string{"a"}, "x", 0); err == nil {
		t.Error("limit 0 accepted")
	}
	if _, _, err := SearchDataQuery("redis", "t", []string{"a"}, "x", 10); err == nil {
		t.Error("unknown engine accepted")
	}
}

func quoted(t *testing.T, dbType, name string) string {
	t.Helper()
	q, err := QuoteIdent(dbType, name)
	if err != nil {
		t.Fatalf("quote %s for %s: %v", name, dbType, err)
	}
	return q
}

// SQLite's LIKE has no default escape character (MySQL's is the backslash):
// without ESCAPE '\' the \%-escaped pattern matches literal backslashes and a
// fragment containing _ or % matches anything on the table.
func TestSQLiteSearchEscapesLike(t *testing.T) {
	q, _, err := SearchObjectsQuery(SQLite, "", "_users%", 10)
	if err != nil {
		t.Fatalf("objects: %v", err)
	}
	if !strings.Contains(q, `ESCAPE '\'`) {
		t.Fatalf("object search missing ESCAPE: %s", q)
	}
	q, _, err = SearchDataQuery(SQLite, "t", []string{"a", "b"}, "50%", 10)
	if err != nil {
		t.Fatalf("data: %v", err)
	}
	if !strings.Contains(q, `ESCAPE '\'`) {
		t.Fatalf("data search missing ESCAPE: %s", q)
	}
	// MySQL keeps the default escape — the clause must not be doubled up.
	q, _, err = SearchDataQuery(MySQL, "t", []string{"a"}, "50%", 10)
	if err != nil {
		t.Fatalf("mysql data: %v", err)
	}
	if strings.Contains(q, "ESCAPE") {
		t.Fatalf("mysql data search must rely on the default escape: %s", q)
	}
}

// T-SQL's LIKE has no ESCAPE clause and does not treat the backslash as an
// escape character. The kernel must therefore escape wildcards with bracket
// character classes and must never emit `ESCAPE '\'` on the SQL Server path —
// the old form was rejected by the server ("Incorrect syntax near 'ESCAPE'"),
// which made SQL Server object search and data search return nothing at all.
func TestMssqlLikePatternUsesBracketClasses(t *testing.T) {
	cases := map[string]string{
		"":         "%%",
		"abc":      "%abc%",
		"a_b":      "%a[_]b%",
		"50%":      "%50[%]%",
		"[x":       "%[[]x%",
		"a]b":      "%a]b%",
		`a\b`:      `%a\b%`, // backslash is literal in T-SQL, not an escape
		"100%_off": "%100[%][_]off%",
	}
	for in, want := range cases {
		if got := mssqlLikePattern(in); got != want {
			t.Errorf("mssqlLikePattern(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMssqlSearchNeverEmitsEscapeClause(t *testing.T) {
	q, args, err := SearchObjectsQuery(SQLServer, "app", "cust_%", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 0 {
		t.Fatalf("expected inlined literal, got args %v", args)
	}
	if strings.Contains(q, "ESCAPE") {
		t.Errorf("object search emits the invalid ESCAPE clause:\n%s", q)
	}
	for _, want := range []string{`N'%cust[_][%]%'`, "TOP (10)"} {
		if !strings.Contains(q, want) {
			t.Errorf("object search misses %q:\n%s", want, q)
		}
	}

	q, args, err = SearchDataQuery(SQLServer, "[t]", []string{"[c]"}, "cust_%", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 0 {
		t.Fatalf("expected inlined literal, got args %v", args)
	}
	if strings.Contains(q, "ESCAPE") {
		t.Errorf("data search emits the invalid ESCAPE clause:\n%s", q)
	}
	if !strings.Contains(q, `N'%cust[_][%]%'`) {
		t.Errorf("data search pattern not bracket-escaped:\n%s", q)
	}
}
