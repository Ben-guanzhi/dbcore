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
	for _, want := range []string{
		"http://analyst@trino.local:8080",
		"catalog=hive",
		"schema=default",
	} {
		if !strings.Contains(dsn, want) {
			t.Errorf("DSN %q is missing %q", dsn, want)
		}
	}
	// The catalog and schema must NOT ride the path: the client appends
	// /v1/statement to the base URL, so a path segment makes every query a
	// 404 at /tpch.tiny/v1/statement.
	if strings.Contains(strings.SplitN(dsn, "?", 2)[0], "/hive") {
		t.Fatalf("the catalog went into the path: %q", dsn)
	}
	// The scheme is the TRANSPORT protocol, not the engine name.
	if strings.HasPrefix(dsn, "trino://") {
		t.Fatalf("the scheme must be the transport, not the engine: %q", dsn)
	}
	if !strings.HasPrefix(dsn, "http://") {
		t.Fatalf("a plain mode should use http: %q", dsn)
	}
	// A TLS mode upgrades it.
	secure := DSN(dbcore.ConnSpec{Kind: dbcore.KindTrino, Host: "h", Port: 8443, SSLMode: "require"})
	if !strings.HasPrefix(secure, "https://") {
		t.Fatalf("a TLS mode should use https: %q", secure)
	}
}

// TestDSNSchemaFromParams pins the fallback: the client's connection dialog
// has no schema box, so the schema arrives through the generic parameter map.
func TestDSNSchemaFromParams(t *testing.T) {
	dsn := DSN(dbcore.ConnSpec{
		Kind: dbcore.KindTrino, Host: "h", Port: 8080,
		Database: "tpch", Params: map[string]string{"schema": "tiny"},
	})
	if !strings.Contains(dsn, "schema=tiny") || !strings.Contains(dsn, "catalog=tpch") {
		t.Fatalf("DSN = %q", dsn)
	}
	// An explicit Schema field wins over the parameter map.
	both := DSN(dbcore.ConnSpec{
		Kind: dbcore.KindTrino, Host: "h", Port: 8080,
		Database: "tpch", Schema: "sf1", Params: map[string]string{"schema": "tiny"},
	})
	if !strings.Contains(both, "schema=sf1") {
		t.Fatalf("the explicit schema did not win: %q", both)
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