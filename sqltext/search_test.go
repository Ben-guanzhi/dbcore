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
