package postgres

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/drivertest"
)

// TestPostgresContract runs the full contract suite against a live PostgreSQL.
// Set DBCORE_TEST_PG_DSN (e.g. "postgres://u:p@localhost:5432/db?sslmode=disable")
// to enable; otherwise the test is skipped (no server in CI-local runs).
func TestPostgresContract(t *testing.T) {
	dsn := os.Getenv("DBCORE_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set DBCORE_TEST_PG_DSN to run PostgreSQL contract tests")
	}
	h := drivertest.Harness{
		Make: func(ctx context.Context) (dbcore.SQLTxDriver, error) {
			return Open(dsn)
		},
		Schema:      "public",
		Placeholder: func(idx int) string { return fmt.Sprintf("$%d", idx) },
		Kind:        dbcore.KindPostgres,
		TimestampDDL: func(table string) []string {
			return []string{fmt.Sprintf("CREATE TABLE %s (id INTEGER, at TIMESTAMPTZ)", table)}
		},
	}
	h.Run(t)
}

func TestCatalogQueriesExposeMetadata(t *testing.T) {
	d := PostgresDialect{}
	cases := map[dbcore.CatalogKind][]string{
		dbcore.CatalogTables:      {"obj_description", "reltuples"},
		dbcore.CatalogColumns:     {"col_description", "pg_constraint", "nextval"},
		dbcore.CatalogIndexes:     {"indisprimary", "indisunique", "indoption", "pg_get_indexdef"},
		dbcore.CatalogForeignKeys: {"confdeltype", "confupdtype", "unnest"},
		dbcore.CatalogPrimaryKey:  {"contype = 'p'", "unnest"},
	}
	for kind, wants := range cases {
		q, _ := d.CatalogQuery(kind, "public", "users")
		for _, want := range wants {
			if !strings.Contains(q, want) {
				t.Errorf("kind %d missing %q: %s", kind, want, q)
			}
		}
	}
}

// BrowseRows tolerates a nil request (the plugin path normalizes one in), so
// the statement builders must too — a nil deref here panic'd the caller.
func TestBuildRowsQueryNilRequest(t *testing.T) {
	var d PostgresDialect
	q, _ := d.BuildRowsQuery("", "t", nil)
	if q == "" {
		t.Fatal("nil request built an empty query")
	}
	cq, _ := d.BuildCountQuery("", "t", nil)
	if cq == "" {
		t.Fatal("nil request built an empty count query")
	}
}

// A serial column's type already is the sequence mechanism: adding
// GENERATED ALWAYS AS IDENTITY on top is a syntax error, so the clause is
// reserved for real identity columns.
func TestRenderCreateColumnsSerialVsIdentity(t *testing.T) {
	quote := func(s string) string { return `"` + s + `"` }
	nextval := "nextval('users_id_seq'::regclass)"

	// Serial: nextval default, no IDENTITY clause.
	cols := []dbcore.ColumnInfo{{Name: "id", Type: "integer", Nullable: false,
		IsAutoIncrement: true, IdentityKind: "serial", Default: &nextval}}
	got := renderCreateColumns(cols, quote)
	if strings.Contains(got, "IDENTITY") {
		t.Errorf("a serial column must not get GENERATED ALWAYS AS IDENTITY:\n%s", got)
	}
	if !strings.Contains(got, "DEFAULT nextval('users_id_seq'::regclass)") {
		t.Errorf("a serial column keeps its nextval default:\n%s", got)
	}

	// Real identity: the clause is present and no nextval default.
	ident := []dbcore.ColumnInfo{{Name: "id", Type: "integer", Nullable: false,
		IsAutoIncrement: true, IdentityKind: "identity"}}
	got = renderCreateColumns(ident, quote)
	if !strings.Contains(got, "GENERATED ALWAYS AS IDENTITY") {
		t.Errorf("an identity column must get the clause:\n%s", got)
	}

	// Mixed: each column rendered by its own kind.
	mixed := []dbcore.ColumnInfo{
		{Name: "id", Type: "integer", Nullable: false, IsAutoIncrement: true, IdentityKind: "identity"},
		{Name: "code", Type: "text", Nullable: true},
	}
	got = renderCreateColumns(mixed, quote)
	if !strings.Contains(got, `"id" integer GENERATED ALWAYS AS IDENTITY NOT NULL`) {
		t.Errorf("identity column wrong:\n%s", got)
	}
	if !strings.Contains(got, `"code" text NULL`) {
		t.Errorf("plain column wrong:\n%s", got)
	}
}
