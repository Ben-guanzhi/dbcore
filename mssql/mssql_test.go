package mssql

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/drivertest"
)

// TestMSSQLContract runs the full contract suite against a live SQL Server.
// Set DBCORE_TEST_MSSQL_DSN (e.g.
// "sqlserver://sa:pass@localhost:1433?database=dbcore_test&encrypt=disable")
// to enable; otherwise it is skipped. DBCORE_TEST_MSSQL_SCHEMA defaults to dbo.
func TestMSSQLContract(t *testing.T) {
	dsn := os.Getenv("DBCORE_TEST_MSSQL_DSN")
	if dsn == "" {
		t.Skip("set DBCORE_TEST_MSSQL_DSN to run SQL Server contract tests")
	}
	schema := os.Getenv("DBCORE_TEST_MSSQL_SCHEMA")
	if schema == "" {
		schema = "dbo"
	}
	h := drivertest.Harness{
		Make: func(ctx context.Context) (dbcore.SQLTxDriver, error) {
			return Open(dsn)
		},
		Schema:      schema,
		Placeholder: func(idx int) string { return fmt.Sprintf("@p%d", idx) },
		// SQL Server's TEXT is deprecated and not comparable; use NVARCHAR.
		CreateTable: func(name string) string {
			return fmt.Sprintf("CREATE TABLE [%s] (id INT, name NVARCHAR(255))", name)
		},
	}
	h.Run(t)
}

func TestDialectBindsFilterValues(t *testing.T) {
	d := MSSQLDialect{}
	req := &dbcore.TableRowsRequest{
		Page:     2,
		PageSize: 10,
		Sort:     []dbcore.SortKey{{Column: "name", Desc: true}},
		Filter: []dbcore.FilterExpr{{
			Column: "app",
			Op:     dbcore.FilterEq,
			Value:  dbcore.Value{Kind: dbcore.ValText, S: "x' OR 1=1 --"},
		}},
	}
	q, args := d.BuildRowsQuery("app", "users", req)

	if strings.Contains(q, "x' OR 1=1 --") {
		t.Fatalf("filter value was inlined into SQL: %s", q)
	}
	if got := strings.Count(q, "@p1"); got != 1 {
		t.Fatalf("placeholder count = %d, want 1: %s", got, q)
	}
	for _, want := range []string{"[app].[users]", "[app] = @p1", "ORDER BY [name] DESC", "OFFSET 10 ROWS FETCH NEXT 10 ROWS ONLY"} {
		if !strings.Contains(q, want) {
			t.Errorf("SQL missing %q: %s", want, q)
		}
	}
	if len(args) != 1 || args[0].S != "x' OR 1=1 --" {
		t.Fatalf("args = %+v, want the bound filter value", args)
	}
}

func TestNotEqualMapsToTSQLOperator(t *testing.T) {
	d := MSSQLDialect{}
	q, _ := d.BuildCountQuery("dbo", "t", &dbcore.TableRowsRequest{
		Filter: []dbcore.FilterExpr{{Column: "id", Op: dbcore.FilterNeq, Value: dbcore.Value{Kind: dbcore.ValInt, I: 1}}},
	})
	if !strings.Contains(q, "[id] <> @p1") {
		t.Fatalf("!= should become <>: %s", q)
	}
}

func TestPagingAlwaysHasOrderBy(t *testing.T) {
	d := MSSQLDialect{}

	q, _ := d.BuildRowsQuery("dbo", "t", &dbcore.TableRowsRequest{Page: 1, PageSize: 5})
	if !strings.Contains(q, "ORDER BY (SELECT NULL) OFFSET 0 ROWS FETCH NEXT 5 ROWS ONLY") {
		t.Fatalf("unsorted paging must add a constant ORDER BY: %s", q)
	}

	q, _ = d.BuildCountQuery("dbo", "t", &dbcore.TableRowsRequest{})
	if strings.Contains(q, "ORDER BY") || strings.Contains(q, "OFFSET") {
		t.Fatalf("count query must not page: %s", q)
	}
}

func TestCatalogQueriesAreParameterized(t *testing.T) {
	d := MSSQLDialect{}
	for _, kind := range []dbcore.CatalogKind{
		dbcore.CatalogTables,
		dbcore.CatalogColumns,
		dbcore.CatalogIndexes,
		dbcore.CatalogForeignKeys,
		dbcore.CatalogPrimaryKey,
	} {
		q, args := d.CatalogQuery(kind, "dbo", "users")
		if len(args) == 0 {
			t.Errorf("catalog kind %d produced no bind args: %s", kind, q)
		}
		if !strings.Contains(q, "@p") {
			t.Errorf("catalog kind %d has no placeholder: %s", kind, q)
		}
		if strings.Contains(q, "'dbo'") || strings.Contains(q, "'users'") {
			t.Errorf("catalog kind %d inlines names: %s", kind, q)
		}
	}
}

func TestQuotePlaceholderAndPaging(t *testing.T) {
	d := MSSQLDialect{}
	if got := d.QuoteIdent("a]b", "c"); got != "[a]]b].[c]" {
		t.Errorf("QuoteIdent = %q", got)
	}
	if got := d.Placeholder(3); got != "@p3" {
		t.Errorf("Placeholder = %q", got)
	}
	if got := d.LimitOffset(10, 20); got != "OFFSET 20 ROWS FETCH NEXT 10 ROWS ONLY" {
		t.Errorf("LimitOffset = %q", got)
	}
	if got := d.LimitOffset(0, 0); got != "" {
		t.Errorf("LimitOffset(0,0) = %q, want empty", got)
	}
	if d.Kind() != dbcore.KindMSSQL {
		t.Errorf("Kind = %q", d.Kind())
	}
}

func TestCatalogQueriesExposeMetadata(t *testing.T) {
	d := MSSQLDialect{}
	cases := map[dbcore.CatalogKind][]string{
		dbcore.CatalogTables:      {"MS_Description", "sys.partitions"},
		dbcore.CatalogColumns:     {"PRIMARY KEY", "MS_Description"},
		dbcore.CatalogIndexes:     {"is_primary_key", "'primary'"},
		dbcore.CatalogForeignKeys: {"delete_referential_action_desc", "update_referential_action_desc"},
	}
	for kind, wants := range cases {
		q, _ := d.CatalogQuery(kind, "dbo", "users")
		for _, want := range wants {
			if !strings.Contains(q, want) {
				t.Errorf("kind %d missing %q: %s", kind, want, q)
			}
		}
	}
}
