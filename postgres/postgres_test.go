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
	}
	h.Run(t)
}

func TestCatalogQueriesExposeMetadata(t *testing.T) {
	d := PostgresDialect{}
	cases := map[dbcore.CatalogKind][]string{
		dbcore.CatalogTables:      {"obj_description", "reltuples"},
		dbcore.CatalogColumns:     {"col_description", "pg_constraint"},
		dbcore.CatalogIndexes:     {"indisprimary", "indisunique"},
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
