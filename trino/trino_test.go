package trino

// trino_test.go — the driver's pure logic: the DSN, catalog handling, the
// statement classification and the dialect's paging.

import (
	"strings"
	"testing"

	"github.com/Ben-guanzhi/dbcore"
)

func TestDSNCarriesCatalogAndSchema(t *testing.T) {
	spec := dbcore.ConnSpec{
		Kind: dbcore.KindTrino, Host: "trino.local", Port: 8080,
		User: "analyst", Database: "hive", Schema: "default",
	}
	dsn := DSN(spec)
	for _, want := range []string{"trino://analyst@trino.local:8080", "/hive.default"} {
		if !strings.Contains(dsn, want) {
			t.Errorf("DSN %q is missing %q", dsn, want)
		}
	}
	// A catalog with no schema still rides the path.
	only := DSN(dbcore.ConnSpec{Kind: dbcore.KindTrino, Host: "h", Port: 8080, Database: "tpch"})
	if !strings.HasSuffix(only, "/tpch") {
		t.Errorf("catalog-only DSN = %q", only)
	}
	// Only a schema, no catalog: the schema takes the path.
	sch := DSN(dbcore.ConnSpec{Kind: dbcore.KindTrino, Host: "h", Port: 8080, Schema: "s1"})
	if !strings.HasSuffix(sch, "/s1") {
		t.Errorf("schema-only DSN = %q", sch)
	}
}

// TestSplitCatalogSchema pins the two accepted forms, since every catalog
// read depends on it.
func TestSplitCatalogSchema(t *testing.T) {
	if c, s, ok := splitCatalogSchema("tpch"); c != "tpch" || s != "" || ok {
		t.Errorf("bare catalog = %q,%q,%v", c, s, ok)
	}
	if c, s, ok := splitCatalogSchema("tpch.tiny"); c != "tpch" || s != "tiny" || !ok {
		t.Errorf("catalog.schema = %q,%q,%v", c, s, ok)
	}
	if c, s, ok := splitCatalogSchema("  tpch.tiny  "); c != "tpch" || s != "tiny" || !ok {
		t.Errorf("padded input = %q,%q,%v", c, s, ok)
	}
	if c, _, ok := splitCatalogSchema(""); c != "" || ok {
		t.Errorf("empty = %q,%v", c, ok)
	}
}

// TestTableRef pins the three-part naming: a bare catalog cannot address a
// table, and the ref must never silently drop a part.
func TestTableRef(t *testing.T) {
	if got := tableRef("tpch.tiny", "orders"); got != `"tpch"."tiny"."orders"` {
		t.Fatalf("full ref = %q", got)
	}
	if got := tableRef("tpch", "orders"); got != `"tpch"."orders"` {
		t.Fatalf("catalog-only ref = %q", got)
	}
}

// TestLooksLikeCommand pins the Exec/Query split. This is the check that
// stands between a working DROP and a silent no-op.
func TestLooksLikeCommand(t *testing.T) {
	for _, q := range []string{
		"INSERT INTO t VALUES (1)", "CREATE TABLE t (a bigint)",
		"DROP TABLE t", "ALTER TABLE t ADD COLUMN b bigint",
		"DELETE FROM t", "UPDATE t SET a = 1", "SET SESSION x = 1",
		"USE tpch.tiny", "GRANT SELECT ON t TO u", "CALL system.runtime.kill_query('x')",
	} {
		if !looksLikeCommand(q) {
			t.Errorf("%q should take the Exec path", q)
		}
	}
	for _, q := range []string{
		"SELECT 1", "select * from t", "WITH x AS (SELECT 1) SELECT * FROM x",
		"SHOW CATALOGS", "SHOW SCHEMAS", "DESCRIBE t", "EXPLAIN SELECT 1",
		"TABLE t", "VALUES (1)", "  select 1",
	} {
		if looksLikeCommand(q) {
			t.Errorf("%q should take the Query path", q)
		}
	}
}

func TestDialectPagingAndFilters(t *testing.T) {
	d := TrinoDialect{}
	q, args := d.BuildRowsQuery("tpch.tiny", "orders", &dbcore.TableRowsRequest{
		Page: 2, PageSize: 10,
		Sort:   []dbcore.SortKey{{Column: "orderkey"}},
		Filter: []dbcore.FilterExpr{{Column: "orderstatus", Op: dbcore.FilterEq, Value: dbcore.Value{Kind: dbcore.ValText, S: "F"}}},
	})
	for _, want := range []string{
		`SELECT * FROM "tpch"."tiny"."orders"`,
		`WHERE "orderstatus" = ?`,
		`ORDER BY "orderkey" ASC`,
		"LIMIT 10 OFFSET 10",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query is missing %q:\n%s", want, q)
		}
	}
	if len(args) != 1 || args[0].S != "F" {
		t.Fatalf("args = %+v", args)
	}
	// The value must never reach the SQL text.
	if strings.Contains(q, "'F'") {
		t.Fatalf("the filter value was spliced in: %s", q)
	}

	cq, _ := d.BuildCountQuery("tpch.tiny", "orders", &dbcore.TableRowsRequest{
		Filter: []dbcore.FilterExpr{{Column: "x", Op: dbcore.FilterIsNull}},
	})
	if !strings.Contains(cq, "count(*)") || !strings.Contains(cq, `"x" IS NULL`) {
		t.Fatalf("count query = %q", cq)
	}
}

func TestQuoteLiteral(t *testing.T) {
	if got := quoteLiteral("it's"); got != "'it''s'" {
		t.Fatalf("quote = %q", got)
	}
}
