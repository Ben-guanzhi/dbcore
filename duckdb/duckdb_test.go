package duckdb

// duckdb_test.go — the driver's pure logic: the DSN, statement classification,
// list parsing and the dialect's paging.

import (
	"strings"
	"testing"

	"github.com/Ben-guanzhi/dbcore"
)

func TestDSNIsAFilePath(t *testing.T) {
	// An embedded engine has no host or port: the file path IS the address.
	if got := DSN(dbcore.ConnSpec{Kind: dbcore.KindDuckDB, File: "C:\\data\\app.duckdb"}); got != "C:\\data\\app.duckdb" {
		t.Fatalf("DSN = %q", got)
	}
	if got := DSN(dbcore.ConnSpec{Kind: dbcore.KindDuckDB}); got != "" {
		t.Fatalf("an empty file should mean in-memory, got %q", got)
	}
	if got := DSN(dbcore.ConnSpec{Kind: dbcore.KindDuckDB, File: "  db.duckdb  "}); got != "db.duckdb" {
		t.Fatalf("the path was not trimmed: %q", got)
	}
}

// TestLooksLikeCommand pins the Exec/Query split.
func TestLooksLikeCommand(t *testing.T) {
	for _, q := range []string{
		"INSERT INTO t VALUES (1)", "CREATE TABLE t (a INT)", "DROP TABLE t",
		"ALTER TABLE t ADD COLUMN b INT", "DELETE FROM t", "UPDATE t SET a=1",
		"COPY t FROM 'x.csv'", "INSTALL httpfs", "LOAD httpfs",
		"EXPORT DATABASE 'x'", "CHECKPOINT", "VACUUM", "SET threads=2",
	} {
		if !looksLikeCommand(q) {
			t.Errorf("%q should take the Exec path", q)
		}
	}
	for _, q := range []string{
		"SELECT 1", "select * from t", "WITH x AS (SELECT 1) SELECT * FROM x",
		"SHOW TABLES", "DESCRIBE t", "EXPLAIN SELECT 1", "PRAGMA version",
		"TABLE t", "VALUES (1)", "SUMMARIZE t", "  select 1",
	} {
		if looksLikeCommand(q) {
			t.Errorf("%q should take the Query path", q)
		}
	}
}

// TestListTextOf pins the parsing of DuckDB's list rendering, which is what
// the primary-key and foreign-key metadata is delivered as.
func TestListTextOf(t *testing.T) {
	cases := map[string][]string{
		"[id]":       {"id"},
		"[id, name]": {"id", "name"},
		"[]":         nil,
		"":           nil,
		`["weird"]`:  {"weird"},
		"plain":      {"plain"},
	}
	for in, want := range cases {
		got := listTextOf(in)
		if len(got) != len(want) {
			t.Errorf("listTextOf(%q) = %v, want %v", in, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("listTextOf(%q)[%d] = %q, want %q", in, i, got[i], want[i])
			}
		}
	}
}

// TestIndexColumnsOf lifts the column list out of a CREATE INDEX statement.
func TestIndexColumnsOf(t *testing.T) {
	if got := indexColumnsOf(`CREATE INDEX idx ON t (a, b)`); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("columns = %v", got)
	}
	if got := indexColumnsOf(`CREATE INDEX idx ON t ("x")`); len(got) != 1 || got[0] != "x" {
		t.Fatalf("quoted column = %v", got)
	}
	if got := indexColumnsOf("no parens"); got != nil {
		t.Fatalf("no parens = %v", got)
	}
}

func TestDialectPagingAndFilters(t *testing.T) {
	d := DuckDBDialect{}
	q, args := d.BuildRowsQuery("main", "t", &dbcore.TableRowsRequest{
		Page: 2, PageSize: 10,
		Sort:   []dbcore.SortKey{{Column: "id", Desc: true}},
		Filter: []dbcore.FilterExpr{{Column: "tag", Op: dbcore.FilterEq, Value: dbcore.Value{Kind: dbcore.ValText, S: "x"}}},
	})
	for _, want := range []string{
		`SELECT * FROM "main"."t"`, `WHERE "tag" = ?`,
		`ORDER BY "id" DESC`, "LIMIT 10 OFFSET 10",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query is missing %q:\n%s", want, q)
		}
	}
	if len(args) != 1 || args[0].S != "x" {
		t.Fatalf("args = %+v", args)
	}
	if strings.Contains(q, "'x'") {
		t.Fatalf("the filter value was spliced in: %s", q)
	}

	cq, _ := d.BuildCountQuery("main", "t", &dbcore.TableRowsRequest{
		Filter: []dbcore.FilterExpr{{Column: "a", Op: dbcore.FilterIsNotNull}},
	})
	if !strings.Contains(cq, "count(*)") || !strings.Contains(cq, `"a" IS NOT NULL`) {
		t.Fatalf("count query = %q", cq)
	}
}
