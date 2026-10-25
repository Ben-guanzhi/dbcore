package oracle

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/drivertest"
)

// TestOracleContract runs the full contract suite against a live Oracle.
// Set DBCORE_TEST_ORACLE_DSN (e.g. "oracle://user:pass@localhost:1521/FREEPDB1")
// to enable; otherwise it is skipped. With DBCORE_TEST_ORACLE_SCHEMA unset the
// driver uses the session's current schema.
func TestOracleContract(t *testing.T) {
	dsn := os.Getenv("DBCORE_TEST_ORACLE_DSN")
	if dsn == "" {
		t.Skip("set DBCORE_TEST_ORACLE_DSN to run Oracle contract tests")
	}
	h := drivertest.Harness{
		Make: func(ctx context.Context) (dbcore.SQLTxDriver, error) {
			return Open(dsn)
		},
		Schema:      os.Getenv("DBCORE_TEST_ORACLE_SCHEMA"),
		Placeholder: func(idx int) string { return fmt.Sprintf(":%d", idx) },
		Kind:        dbcore.KindOracle,
		// Oracle has no TEXT type; VARCHAR2 is the portable string column.
		CreateTable: func(name string) string {
			return fmt.Sprintf(`CREATE TABLE "%s" (id NUMBER, name VARCHAR2(255))`, name)
		},
		// DROP TABLE IF EXISTS does not exist before 23c.
		DropTable: func(name string) string {
			return fmt.Sprintf(`BEGIN EXECUTE IMMEDIATE 'DROP TABLE "%s"'; EXCEPTION WHEN OTHERS THEN NULL; END;`, name)
		},
		// Unquoted Oracle identifiers fold to upper case; keep every reference
		// in the harness on the stored name.
		TableName:  strings.ToUpper,
		ColumnFold: strings.ToUpper,
	}
	h.Run(t)
}

func TestDialectBindsFilterValues(t *testing.T) {
	d := OracleDialect{}
	req := &dbcore.TableRowsRequest{
		Page:     3,
		PageSize: 20,
		Sort:     []dbcore.SortKey{{Column: "name"}},
		Filter: []dbcore.FilterExpr{{
			Column: "app",
			Op:     dbcore.FilterEq,
			Value:  dbcore.Value{Kind: dbcore.ValText, S: "x' OR 1=1 --"},
		}},
	}
	q, args := d.BuildRowsQuery("APP", "users", req)

	if strings.Contains(q, "x' OR 1=1 --") {
		t.Fatalf("filter value was inlined into SQL: %s", q)
	}
	for _, want := range []string{`"APP"."users"`, `"app" = :1`, `ORDER BY "name"`, "OFFSET 40 ROWS FETCH NEXT 20 ROWS ONLY"} {
		if !strings.Contains(q, want) {
			t.Errorf("SQL missing %q: %s", want, q)
		}
	}
	if len(args) != 1 || args[0].S != "x' OR 1=1 --" {
		t.Fatalf("args = %+v, want the bound filter value", args)
	}
}

func TestCatalogQueriesAreParameterized(t *testing.T) {
	d := OracleDialect{}
	for _, kind := range []dbcore.CatalogKind{
		dbcore.CatalogTables,
		dbcore.CatalogColumns,
		dbcore.CatalogIndexes,
		dbcore.CatalogForeignKeys,
		dbcore.CatalogPrimaryKey,
	} {
		q, args := d.CatalogQuery(kind, "APP", "USERS")
		if len(args) == 0 {
			t.Errorf("catalog kind %d produced no bind args: %s", kind, q)
		}
		if !strings.Contains(q, ":1") {
			t.Errorf("catalog kind %d has no placeholder: %s", kind, q)
		}
		if strings.Contains(q, "'APP'") || strings.Contains(q, "'USERS'") {
			t.Errorf("catalog kind %d inlines names: %s", kind, q)
		}
	}
	if q, args := d.CatalogQuery(dbcore.CatalogSchemas, "", ""); len(args) != 0 || !strings.Contains(q, "all_users") {
		t.Errorf("schemas query = %q, args=%v", q, args)
	}
}

func TestTypeExprPreservesCharSemantics(t *testing.T) {
	// CHAR-semantics columns store the declared length in CHAR_LENGTH (DATA_LENGTH
	// is bytes), and the CHAR unit is only legal for VARCHAR2/CHAR — not for
	// NVARCHAR2/NCHAR.
	for _, want := range []string{"CHAR_LENGTH", "c.DATA_TYPE IN ('VARCHAR2','CHAR')"} {
		if !strings.Contains(sqlTypeExpr, want) {
			t.Errorf("sqlTypeExpr missing %q", want)
		}
	}
}

func TestQuotePlaceholderAndPaging(t *testing.T) {
	d := OracleDialect{}
	if got := d.QuoteIdent(`a"b`, "c"); got != `"a""b"."c"` {
		t.Errorf("QuoteIdent = %q", got)
	}
	if got := d.Placeholder(2); got != ":2" {
		t.Errorf("Placeholder = %q", got)
	}
	if got := d.LimitOffset(10, 5); got != "OFFSET 5 ROWS FETCH NEXT 10 ROWS ONLY" {
		t.Errorf("LimitOffset = %q", got)
	}
	if got := d.LimitOffset(0, 0); got != "" {
		t.Errorf("LimitOffset(0,0) = %q, want empty", got)
	}
	if d.Kind() != dbcore.KindOracle {
		t.Errorf("Kind = %q", d.Kind())
	}
}

func TestCatalogQueriesExposeMetadata(t *testing.T) {
	d := OracleDialect{}
	cases := map[dbcore.CatalogKind][]string{
		dbcore.CatalogTables:      {"ALL_TAB_COMMENTS", "COMMENTS", "NUM_ROWS"},
		dbcore.CatalogColumns:     {"ALL_COL_COMMENTS", "CONSTRAINT_TYPE = 'P'"},
		dbcore.CatalogIndexes:     {"'primary'", "'unique'", "ALL_IND_EXPRESSIONS", "DESCEND"},
		dbcore.CatalogForeignKeys: {"DELETE_RULE"},
	}
	for kind, wants := range cases {
		q, _ := d.CatalogQuery(kind, "APP", "USERS")
		for _, want := range wants {
			if !strings.Contains(q, want) {
				t.Errorf("kind %d missing %q: %s", kind, want, q)
			}
		}
	}
}
