package dm

// packages_live_test.go — the Oracle-family object families, verified on a
// real DM8. Skipped unless DBCORE_TEST_DM_DSN is set, like the rest of the
// live suite.
//
// What this settles on a live instance: the ALL_OBJECTS / ALL_SOURCE /
// ALL_SYNONYMS classic views exist with the shapes the recipes assume, and
// DBA_JOBS is the catalog DM8 actually keeps (Oracle's scheduler views are
// not there).

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Ben-guanzhi/dbcore"
)

// contextFor gives each test a bounded context: a hung catalog query must
// fail the test rather than the run.
func contextFor(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func liveDMCaps(t *testing.T) (dbcore.Driver, string) {
	t.Helper()
	dsn := os.Getenv("DBCORE_TEST_DM_DSN")
	if dsn == "" {
		t.Skip("set DBCORE_TEST_DM_DSN to run the DM live tests")
	}
	drv, err := Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = drv.Close() })
	schema := strings.ToUpper(strings.TrimSpace(os.Getenv("DBCORE_TEST_DM_SCHEMA")))
	if schema == "" {
		schema = "SYSDBA"
	}
	return drv, schema
}

// TestDMLivePackagesAndSynonyms creates a package and a synonym, then reads
// them back through the new capabilities.
func TestDMLivePackagesAndSynonyms(t *testing.T) {
	drv, schema := liveDMCaps(t)
	ctx := contextFor(t)

	pkgs, ok := dbcore.AsPackageReader(drv)
	if !ok {
		t.Fatal("the dm driver must expose packages")
	}
	syns, ok := dbcore.AsSynonymReader(drv)
	if !ok {
		t.Fatal("the dm driver must expose synonyms")
	}

	sqlDrv, _ := drv.AsSQL()
	// A table to point the synonym at, a package with both halves, and the
	// synonym itself. DROP first so a failed earlier run does not block.
	for _, stmt := range []string{
		`DROP SYNONYM ZZ_SYN_PROBE`,
		`DROP PACKAGE ZZ_PKG_PROBE`,
		`DROP TABLE ZZ_PKG_TABLE`,
	} {
		_, _ = sqlDrv.Execute(ctx, stmt)
	}
	for _, stmt := range []string{
		`CREATE TABLE ZZ_PKG_TABLE (id INT)`,
		`CREATE OR REPLACE PACKAGE ZZ_PKG_PROBE AS PROCEDURE p; END;`,
		`CREATE OR REPLACE PACKAGE BODY ZZ_PKG_PROBE AS PROCEDURE p IS BEGIN NULL; END; END;`,
		`CREATE SYNONYM ZZ_SYN_PROBE FOR ZZ_PKG_TABLE`,
	} {
		if _, err := sqlDrv.Execute(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		for _, stmt := range []string{
			`DROP SYNONYM ZZ_SYN_PROBE`,
			`DROP PACKAGE ZZ_PKG_PROBE`,
			`DROP TABLE ZZ_PKG_TABLE`,
		} {
			_, _ = sqlDrv.Execute(ctx, stmt)
		}
	})

	// The package appears with both halves.
	list, err := pkgs.Packages(ctx, schema)
	if err != nil {
		t.Fatalf("Packages: %v", err)
	}
	var found *dbcore.PackageInfo
	for i := range list {
		if strings.EqualFold(list[i].Name, "ZZ_PKG_PROBE") {
			found = &list[i]
		}
	}
	if found == nil {
		t.Fatalf("ZZ_PKG_PROBE is missing from %d packages", len(list))
	}
	if !found.HasSpec || !found.HasBody {
		t.Fatalf("both halves should exist: %+v", *found)
	}
	if found.Status == "" {
		t.Fatalf("no status reported: %+v", *found)
	}

	// Its definition renders as CREATE statements.
	def, err := pkgs.PackageDefinition(ctx, schema, "ZZ_PKG_PROBE")
	if err != nil {
		t.Fatalf("PackageDefinition: %v", err)
	}
	if !strings.Contains(strings.ToUpper(def), "CREATE OR REPLACE PACKAGE") {
		t.Fatalf("the spec is missing from:\n%s", def)
	}
	if !strings.Contains(strings.ToUpper(def), "PACKAGE BODY") {
		t.Fatalf("the body is missing from:\n%s", def)
	}

	// The synonym appears and resolves to the table.
	slist, err := syns.Synonyms(ctx, schema)
	if err != nil {
		t.Fatalf("Synonyms: %v", err)
	}
	var syn *dbcore.SynonymInfo
	for i := range slist {
		if strings.EqualFold(slist[i].Name, "ZZ_SYN_PROBE") {
			syn = &slist[i]
		}
	}
	if syn == nil {
		t.Fatalf("ZZ_SYN_PROBE is missing from %d synonyms", len(slist))
	}
	if !strings.EqualFold(syn.TargetName, "ZZ_PKG_TABLE") {
		t.Fatalf("target = %+v", *syn)
	}
	if syn.TargetType == "" {
		t.Fatalf("the target's object type should resolve: %+v", *syn)
	}
}

// TestDMLiveJobsDocumentsTheAbsence pins what a live DM8 actually offers.
//
// The first version of this recipe assumed DBA_JOBS; the server answered
// "invalid table or view", and every other candidate (ALL_JOBS, USER_JOBS and
// the three scheduler views) failed the same way — this build's ALL_OBJECTS
// has no JOB type at all. The driver therefore does not claim JobReader, and
// this test records the finding so nobody re-adds a recipe that cannot run.
func TestDMLiveJobsDocumentsTheAbsence(t *testing.T) {
	drv, _ := liveDMCaps(t)
	if jobs, ok := dbcore.AsJobReader(drv); ok {
		ctx := contextFor(t)
		list, err := jobs.Jobs(ctx, "SYSDBA")
		t.Fatalf("this DM8 build was measured to have no job catalog, but the driver claims one (Jobs returned %d rows, err=%v)", len(list), err)
	}
	// Which is the answer the client shows: unavailable, not empty.
	t.Log("DM8 exposes no job catalog on this build: JobReader is not implemented")
}
