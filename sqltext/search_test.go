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
		{"sqlserver", 0, []string{"SELECT TOP (50) *", "[a] LIKE N'%x%'", `ESCAPE '\'`}, "LIKE ?"},
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
	if !strings.Contains(q, `N'%100\%%'`) {
		t.Errorf("mssql pattern not escaped:\n%s", q)
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
