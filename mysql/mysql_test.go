package mysql

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/drivertest"
)

// TestMySQLContract runs the full contract suite against a live MySQL/MariaDB.
// Set DBCORE_TEST_MYSQL_DSN (e.g. "root:pass@tcp(127.0.0.1:3306)/dbcore_test?parseTime=true")
// to enable; otherwise the test is skipped (no server in CI-local runs).
func TestMySQLContract(t *testing.T) {
	dsn := os.Getenv("DBCORE_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set DBCORE_TEST_MYSQL_DSN to run MySQL contract tests")
	}
	cfg, err := gomysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	schema := cfg.DBName
	if schema == "" {
		schema = os.Getenv("DBCORE_TEST_MYSQL_SCHEMA")
	}
	if schema == "" {
		t.Fatal("DBCORE_TEST_MYSQL_DSN must name a database (or set DBCORE_TEST_MYSQL_SCHEMA)")
	}
	h := drivertest.Harness{
		Make: func(ctx context.Context) (dbcore.SQLTxDriver, error) {
			return Open(dsn)
		},
		Schema:      schema,
		Placeholder: func(int) string { return "?" },
		Kind:        dbcore.KindMySQL,
		TimestampDDL: func(table string) []string {
			return []string{fmt.Sprintf("CREATE TABLE %s (id INT, at DATETIME(6))", table)}
		},
	}
	h.Run(t)
}

func TestDialectBindsFilterValues(t *testing.T) {
	d := MySQLDialect{}
	req := &dbcore.TableRowsRequest{
		Page:     2,
		PageSize: 10,
		Sort:     []dbcore.SortKey{{Column: "name", Desc: true}},
		Filter: []dbcore.FilterExpr{{
			Column: "name",
			Op:     dbcore.FilterEq,
			Value:  dbcore.Value{Kind: dbcore.ValText, S: "x' OR 1=1 --"},
		}},
	}
	q, args := d.BuildRowsQuery("app", "users", req)

	if strings.Contains(q, "OR 1=1") || strings.Contains(q, "--") {
		t.Fatalf("filter value was interpolated into SQL: %s", q)
	}
	if len(args) != 1 {
		t.Fatalf("args = %d, want 1", len(args))
	}
	if !strings.Contains(q, "`app`.`users`") {
		t.Fatalf("table not quoted as MySQL path: %s", q)
	}
	if !strings.Contains(q, "ORDER BY `name` DESC") {
		t.Fatalf("sort clause missing: %s", q)
	}
	if !strings.Contains(q, "LIMIT 10 OFFSET 10") {
		t.Fatalf("page 2 should offset by 10: %s", q)
	}
	if got := strings.Count(q, "?"); got != 1 {
		t.Fatalf("placeholder count = %d, want 1: %s", got, q)
	}
}

func TestCatalogQueriesAreParameterized(t *testing.T) {
	d := MySQLDialect{}
	const schema, obj = "app", "users"
	cases := []dbcore.CatalogKind{
		dbcore.CatalogTables,
		dbcore.CatalogColumns,
		dbcore.CatalogIndexes,
		dbcore.CatalogForeignKeys,
	}
	for _, kind := range cases {
		q, args := d.CatalogQuery(kind, schema, obj)
		if len(args) == 0 {
			t.Errorf("catalog kind %d returned no bind args: %s", kind, q)
		}
		if strings.Contains(q, schema) || strings.Contains(q, obj) {
			t.Errorf("catalog kind %d interpolates names instead of binding: %s", kind, q)
		}
		for _, a := range args {
			if a.Kind != dbcore.ValText {
				t.Errorf("catalog kind %d has non-text arg %+v", kind, a)
			}
		}
	}
}

func TestQuotePlaceholderAndPaging(t *testing.T) {
	d := MySQLDialect{}
	if got := d.QuoteIdent("a", "b`c"); got != "`a`.`b``c`" {
		t.Fatalf("QuoteIdent = %q", got)
	}
	if got := d.Placeholder(3); got != "?" {
		t.Fatalf("Placeholder = %q", got)
	}
	if got := d.LimitOffset(0, 0); got != "" {
		t.Fatalf("LimitOffset(0,0) = %q", got)
	}
	if got := d.LimitOffset(5, 0); got != "LIMIT 5" {
		t.Fatalf("LimitOffset(5,0) = %q", got)
	}
	if got := d.LimitOffset(5, 20); got != "LIMIT 5 OFFSET 20" {
		t.Fatalf("LimitOffset(5,20) = %q", got)
	}
	if d.Kind() != dbcore.KindMySQL {
		t.Fatalf("Kind = %q", d.Kind())
	}
}

func TestCatalogQueriesExposeMetadata(t *testing.T) {
	d := MySQLDialect{}
	cases := map[dbcore.CatalogKind][]string{
		dbcore.CatalogTables:      {"TABLE_COMMENT", "information_schema.TABLES"},
		dbcore.CatalogColumns:     {"COLUMN_KEY", "COLUMN_COMMENT"},
		dbcore.CatalogIndexes:     {"'primary'", "'unique'", "EXPRESSION", "INDEX_TYPE", "COLLATION"},
		dbcore.CatalogForeignKeys: {"REFERENTIAL_CONSTRAINTS", "DELETE_RULE", "UPDATE_RULE"},
	}
	for kind, wants := range cases {
		q, _ := d.CatalogQuery(kind, "app", "users")
		for _, want := range wants {
			if !strings.Contains(q, want) {
				t.Errorf("kind %d missing %q: %s", kind, want, q)
			}
		}
	}
}

// The table list must not select TABLE_ROWS: that column makes the server
// compute InnoDB statistics, which open every table of the schema and turned
// the catalog read into a 30s+ stall on large schemas (the client does not even
// use the estimate). Row counts are read per table by TableRows instead.
func TestCatalogTablesAvoidsRowEstimate(t *testing.T) {
	d := MySQLDialect{}
	q, _ := d.CatalogQuery(dbcore.CatalogTables, "gs_prod", "")
	if strings.Contains(q, "TABLE_ROWS") {
		t.Fatalf("CatalogTables selects TABLE_ROWS, which forces a stats dive:\n%s", q)
	}
}

// TestMysqlIndexPart covers the EXPRESSION column parsing in
// mysqlIndexPart: a functional key part has EXPRESSION text and an empty
// COLUMN_NAME; a plain column has the reverse. The DESC flag comes from
// STATISTICS.COLLATION = 'D' (column 8).
func TestMysqlIndexPart(t *testing.T) {
	// Functional index part: EXPRESSION column has text, COLUMN_NAME is empty.
	r := []dbcore.Value{
		{Kind: dbcore.ValText, S: "idx_lower"},       // r[0] INDEX_NAME
		{Kind: dbcore.ValInt, I: 0},                  // r[1] NON_UNIQUE
		{Kind: dbcore.ValText, S: ""},                // r[2] COLUMN_NAME (empty for expression)
		{Kind: dbcore.ValText, S: "index"},           // r[3] kind
		{Kind: dbcore.ValInt, I: 0},                  // r[4] primary flag
		{Kind: dbcore.ValText, S: "BTREE"},           // r[5] INDEX_TYPE
		{Kind: dbcore.ValText, S: "(LOWER(`name`))"}, // r[6] EXPRESSION
		{Kind: dbcore.ValInt, I: 0},                  // r[7] DESC flag (COLLATION != 'D')
	}
	part := mysqlIndexPart(r)
	if part.Expression != "(LOWER(`name`))" {
		t.Errorf("Expression = %q, want %q", part.Expression, "(LOWER(`name`))")
	}
	if part.Column != "" {
		t.Errorf("Column = %q, want empty for expression part", part.Column)
	}
	if part.Desc {
		t.Error("Desc = true, want false for ASC part")
	}

	// Plain column part: EXPRESSION is empty, COLUMN_NAME has text, DESC = true.
	r2 := []dbcore.Value{
		{Kind: dbcore.ValText, S: "idx_col"},
		{Kind: dbcore.ValInt, I: 1},
		{Kind: dbcore.ValText, S: "col1"},
		{Kind: dbcore.ValText, S: "index"},
		{Kind: dbcore.ValInt, I: 0},
		{Kind: dbcore.ValText, S: "BTREE"},
		{Kind: dbcore.ValText, S: ""}, // empty EXPRESSION
		{Kind: dbcore.ValInt, I: 1},   // DESC flag (COLLATION = 'D')
	}
	part2 := mysqlIndexPart(r2)
	if part2.Column != "col1" {
		t.Errorf("Column = %q, want %q", part2.Column, "col1")
	}
	if part2.Expression != "" {
		t.Errorf("Expression = %q, want empty for column part", part2.Expression)
	}
	if !part2.Desc {
		t.Error("Desc = false, want true for DESC part")
	}
}

// BrowseRows tolerates a nil request (the plugin path normalizes one in), so
// the statement builders must too — a nil deref here panic'd the caller.
func TestBuildRowsQueryNilRequest(t *testing.T) {
	var d MySQLDialect
	q, _ := d.BuildRowsQuery("", "t", nil)
	if q == "" {
		t.Fatal("nil request built an empty query")
	}
	cq, _ := d.BuildCountQuery("", "t", nil)
	if cq == "" {
		t.Fatal("nil request built an empty count query")
	}
}
