package clickhouse

// clickhouse_test.go — the driver's pure logic: the DSN, the statement-kind
// classification, key-expression splitting, quoting and paging.

import (
	"strings"
	"testing"

	"github.com/Ben-guanzhi/dbcore"
)

func TestDSNCarriesTheDatabaseAndCredentials(t *testing.T) {
	spec := dbcore.ConnSpec{
		Kind: dbcore.KindClickHouse, Host: "ch.local", Port: 9000,
		User: "app", Password: "s3cr3t", Database: "analytics",
	}
	dsn := DSN(spec)
	for _, want := range []string{
		"clickhouse://app:s3cr3t@ch.local:9000",
		"/analytics",
	} {
		if !strings.Contains(dsn, want) {
			t.Errorf("DSN %q is missing %q", dsn, want)
		}
	}
	// A secure mode must set the driver's flag; the port is the caller's.
	secure := DSN(dbcore.ConnSpec{Kind: dbcore.KindClickHouse, Host: "h", Port: 9440, SSLMode: "require"})
	if !strings.Contains(secure, "secure=true") {
		t.Errorf("a TLS mode did not set secure: %q", secure)
	}
	// A plain mode must not: ClickHouse has no prefer/allow ladder.
	plain := DSN(dbcore.ConnSpec{Kind: dbcore.KindClickHouse, Host: "h", Port: 9000, SSLMode: "disable"})
	if strings.Contains(plain, "secure=true") {
		t.Errorf("a disabled TLS mode set secure: %q", plain)
	}
}

// TestLooksLikeCommand pins the classification that decides Exec vs Query.
//
// This is the check that stood between a working DROP and a silent no-op:
// ClickHouse's driver answers DDL through Query with an empty result and no
// error, so the path must be chosen from the statement, not from a failure.
func TestLooksLikeCommand(t *testing.T) {
	commands := []string{
		"DROP TABLE t", "drop table if exists t sync",
		"CREATE TABLE t (id UInt64) ENGINE = MergeTree() ORDER BY id",
		"ALTER TABLE t ADD COLUMN x Int8",
		"TRUNCATE TABLE t", "OPTIMIZE TABLE t FINAL",
		"INSERT INTO t VALUES (1)", "RENAME TABLE a TO b",
		"SET max_threads = 2", "SYSTEM RELOAD DICTIONARIES",
		"GRANT SELECT ON t TO u", "KILL QUERY WHERE query_id = 'x'",
	}
	for _, q := range commands {
		if !looksLikeCommand(q) {
			t.Errorf("%q should take the Exec path", q)
		}
	}
	queries := []string{
		"SELECT 1", "select count() from t", "WITH x AS (SELECT 1) SELECT * FROM x",
		"SHOW TABLES", "DESCRIBE TABLE t", "EXPLAIN SELECT 1", "EXISTS TABLE t",
		"  select 1", // leading space must not misclassify
	}
	for _, q := range queries {
		if looksLikeCommand(q) {
			t.Errorf("%q should take the Query path", q)
		}
	}
}

func TestSplitKeyExprs(t *testing.T) {
	cases := map[string][]string{
		"id":                {"id"},
		"a, b":              {"a", "b"},
		"tuple(a, b), c":    {"tuple(a, b)", "c"},
		"f(x, y)":           {"f(x, y)"},
		"a, tuple(b, c), d": {"a", "tuple(b, c)", "d"},
		"":                  nil,
		"  ":                nil,
	}
	for in, want := range cases {
		got := splitKeyExprs(in)
		if len(got) != len(want) {
			t.Errorf("splitKeyExprs(%q) = %v, want %v", in, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("splitKeyExprs(%q)[%d] = %q, want %q", in, i, got[i], want[i])
			}
		}
	}
}

func TestQuoteLiteral(t *testing.T) {
	if got := quoteLiteral("it's"); got != "'it''s'" {
		t.Fatalf("quote = %q", got)
	}
	// A backslash is escaped too: ClickHouse honours backslash escapes.
	if got := quoteLiteral(`a\b`); got != `'a\\b'` {
		t.Fatalf("backslash = %q", got)
	}
}

func TestDialectPagingAndFilters(t *testing.T) {
	d := ClickHouseDialect{}
	q, args := d.BuildRowsQuery("db", "t", &dbcore.TableRowsRequest{
		Page: 2, PageSize: 10,
		Sort:   []dbcore.SortKey{{Column: "id", Desc: true}},
		Filter: []dbcore.FilterExpr{{Column: "tag", Op: dbcore.FilterEq, Value: dbcore.Value{Kind: dbcore.ValText, S: "x"}}},
	})
	for _, want := range []string{`SELECT * FROM "db"."t"`, `WHERE "tag" = ?`, `ORDER BY "id" DESC`, "LIMIT 10 OFFSET 10"} {
		if !strings.Contains(q, want) {
			t.Errorf("query is missing %q:\n%s", want, q)
		}
	}
	// The filter value is BOUND, never spliced into the text.
	if len(args) != 1 || args[0].S != "x" {
		t.Fatalf("args = %+v", args)
	}
	if strings.Contains(q, "'x'") {
		t.Fatalf("the filter value was spliced into the query: %s", q)
	}

	cq, _ := d.BuildCountQuery("db", "t", &dbcore.TableRowsRequest{
		Filter: []dbcore.FilterExpr{{Column: "tag", Op: dbcore.FilterEq, Value: dbcore.Value{Kind: dbcore.ValText, S: "x"}}},
	})
	if !strings.Contains(cq, "count()") || !strings.Contains(cq, `WHERE "tag" = ?`) {
		t.Fatalf("count query = %q", cq)
	}
}

func TestDialectNullFilters(t *testing.T) {
	d := ClickHouseDialect{}
	q, args := d.BuildRowsQuery("db", "t", &dbcore.TableRowsRequest{
		Filter: []dbcore.FilterExpr{
			{Column: "a", Op: dbcore.FilterIsNull},
			{Column: "b", Op: dbcore.FilterIsNotNull},
		},
	})
	if !strings.Contains(q, `"a" IS NULL`) || !strings.Contains(q, `"b" IS NOT NULL`) {
		t.Fatalf("null filters = %q", q)
	}
	if len(args) != 0 {
		t.Fatalf("a null filter bound an argument: %+v", args)
	}
}
