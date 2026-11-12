package oracle

// W9: the object-health surface, exercised against a real instance.
//
// A live test is the only honest one here: whether an object is INVALID, and
// whether ALTER … COMPILE clears it, are facts of the server's compiler, not of
// anything this code can assert on its own. The test therefore CREATES a broken
// object, reads it back through the driver, recompiles it, and reads again.
//
// Skipped unless DBCORE_TEST_ORACLE_DSN is set, like the rest of the live tests.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/sqltext"
)

func liveObjectHealth(t *testing.T) (dbcore.SQLDriver, context.Context, string) {
	t.Helper()
	dsn := os.Getenv("DBCORE_TEST_ORACLE_DSN")
	if dsn == "" {
		t.Skip("set DBCORE_TEST_ORACLE_DSN to run the object-health tests")
	}
	schema := os.Getenv("DBCORE_TEST_ORACLE_SCHEMA")
	if schema == "" {
		schema = "SYSTEM"
	}
	ctx := context.Background()
	drv, err := Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = drv.Close() })
	sqlDrv, ok := drv.AsSQL()
	if !ok {
		t.Fatal("the oracle driver must speak SQL")
	}
	return sqlDrv, ctx, schema
}

// cleanObject drops a procedure if it exists, so a failed run does not poison the
// next one.
func cleanObject(t *testing.T, drv dbcore.SQLDriver, ctx context.Context, schema, name string) {
	t.Helper()
	if _, err := drv.Execute(ctx, "DROP PROCEDURE "+schema+"."+name); err != nil {
		// ORA-04043 (does not exist) is the good case.
		if !strings.Contains(err.Error(), "ORA-04043") && !strings.Contains(err.Error(), "does not exist") {
			t.Logf("cleanup %s: %v", name, err)
		}
	}
}

// compile is what the client does: build the statement through the kernel's
// recipe and run it through the normal (gated) path. The driver deliberately has
// no compile method of its own — one way to write is this workspace's rule.
func compile(t *testing.T, drv dbcore.SQLDriver, ctx context.Context, schema, objectType, name string) error {
	t.Helper()
	stmt, err := sqltext.CompileStatement("oracle", schema, objectType, name)
	if err != nil {
		return err
	}
	_, err = drv.Execute(ctx, stmt)
	return err
}

func TestInvalidObjectsListsAndRecompiles(t *testing.T) {
	drv, ctx, schema := liveObjectHealth(t)
	health, ok := dbcore.AsObjectHealth(drv)
	if !ok {
		t.Fatal("the oracle driver must implement ObjectHealthReader")
	}

	const proc = "TPG_W9_BROKEN"
	cleanObject(t, drv, ctx, schema, proc)
	t.Cleanup(func() { cleanObject(t, drv, ctx, schema, proc) })

	// Create a procedure that is fine, then break it by dropping the table it
	// reads. CREATE OR REPLACE revalidates eagerly, so the way to get a genuinely
	// INVALID object is to remove a dependency afterwards.
	for _, stmt := range []string{
		"CREATE TABLE " + schema + ".TPG_W9_T (id NUMBER)",
		"CREATE OR REPLACE PROCEDURE " + schema + "." + proc + " AS n NUMBER; " +
			"BEGIN SELECT COUNT(*) INTO n FROM " + schema + ".TPG_W9_T; END;",
		"DROP TABLE " + schema + ".TPG_W9_T",
	} {
		if _, err := drv.Execute(ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	// The procedure is now INVALID.
	invalid, err := health.InvalidObjects(ctx, schema)
	if err != nil {
		t.Fatalf("invalid objects: %v", err)
	}
	var found *dbcore.InvalidObject
	for i := range invalid {
		if strings.EqualFold(invalid[i].Name, proc) {
			found = &invalid[i]
		}
	}
	if found == nil {
		t.Fatalf("the broken procedure is not reported as invalid: %+v", invalid)
	}
	if !strings.EqualFold(found.Type, "PROCEDURE") {
		t.Fatalf("type = %q", found.Type)
	}

	// Compiling while the table is missing: Oracle ACCEPTS the statement and
	// leaves the object INVALID, recording the reason in ALL_ERRORS. That split is
	// the whole reason CompileErrors exists — a UI that trusted the statement's
	// success would call this object repaired.
	if err := compile(t, drv, ctx, schema, found.Type, found.Name); err != nil {
		t.Fatalf("Oracle accepts the compile statement even with a missing dependency: %v", err)
	}
	invalid, err = health.InvalidObjects(ctx, schema)
	if err != nil {
		t.Fatal(err)
	}
	stillBroken := false
	for _, obj := range invalid {
		if strings.EqualFold(obj.Name, proc) {
			stillBroken = true
		}
	}
	if !stillBroken {
		t.Fatal("the object should still be invalid: the table it reads is gone")
	}
	errs, err := health.CompileErrors(ctx, schema, proc)
	if err != nil {
		t.Fatalf("compile errors: %v", err)
	}
	if len(errs) == 0 {
		t.Fatal("a failed compile must report why")
	}
	var mentionsTable bool
	for _, e := range errs {
		if strings.Contains(strings.ToUpper(e.Text), "TPG_W9_T") ||
			strings.Contains(strings.ToUpper(e.Text), "ORA-00942") {
			mentionsTable = true
		}
	}
	if !mentionsTable {
		t.Fatalf("the error should name the missing table: %+v", errs)
	}

	// Restore the dependency, compile, and the object is valid again.
	if _, err := drv.Execute(ctx, "CREATE TABLE "+schema+".TPG_W9_T (id NUMBER)"); err != nil {
		t.Fatalf("restore table: %v", err)
	}
	t.Cleanup(func() {
		_, _ = drv.Execute(ctx, "DROP TABLE "+schema+".TPG_W9_T")
	})
	if err := compile(t, drv, ctx, schema, found.Type, found.Name); err != nil {
		t.Fatalf("compile: %v", err)
	}
	invalid, err = health.InvalidObjects(ctx, schema)
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range invalid {
		if strings.EqualFold(obj.Name, proc) {
			t.Fatalf("the procedure is still invalid after a successful compile: %+v", obj)
		}
	}
	// And the errors are cleared.
	errs, err = health.CompileErrors(ctx, schema, proc)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Fatalf("a successful compile should leave no errors: %+v", errs)
	}
}

func TestObjectDependenciesBothDirections(t *testing.T) {
	drv, ctx, schema := liveObjectHealth(t)
	health, ok := dbcore.AsObjectHealth(drv)
	if !ok {
		t.Fatal("the oracle driver must implement ObjectHealthReader")
	}

	const proc = "TPG_W9_DEP"
	const table = "TPG_W9_DEP_T"
	cleanObject(t, drv, ctx, schema, proc)
	t.Cleanup(func() {
		cleanObject(t, drv, ctx, schema, proc)
		_, _ = drv.Execute(ctx, "DROP TABLE "+schema+"."+table)
	})
	for _, stmt := range []string{
		"CREATE TABLE " + schema + "." + table + " (id NUMBER)",
		"CREATE OR REPLACE PROCEDURE " + schema + "." + proc + " AS n NUMBER; " +
			"BEGIN SELECT COUNT(*) INTO n FROM " + schema + "." + table + "; END;",
	} {
		if _, err := drv.Execute(ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	deps, err := health.ObjectDependencies(ctx, schema, proc)
	if err != nil {
		t.Fatalf("dependencies: %v", err)
	}
	var out, in int
	for _, dep := range deps {
		// Out: the procedure depends on the table.
		if dep.Direction == dbcore.DependencyOut && strings.EqualFold(dep.Name, table) {
			out++
		}
		// In: nothing should depend on this fresh procedure.
		if dep.Direction == dbcore.DependencyIn && strings.EqualFold(dep.Name, proc) {
			in++
		}
	}
	if out == 0 {
		t.Fatalf("the procedure→table edge is missing: %+v", deps)
	}
	if in != 0 {
		t.Fatalf("an unrelated object appears to depend on the procedure: %+v", deps)
	}

	// The other direction: the TABLE has the procedure depending on it, which is
	// the "what breaks if I drop this" question.
	tableDeps, err := health.ObjectDependencies(ctx, schema, table)
	if err != nil {
		t.Fatal(err)
	}
	var dependents int
	for _, dep := range tableDeps {
		if dep.Direction == dbcore.DependencyIn && strings.EqualFold(dep.Name, proc) {
			dependents++
		}
	}
	if dependents == 0 {
		t.Fatalf("the table→procedure edge is missing: %+v", tableDeps)
	}
}

func TestCompileRefusesAMissingObjectType(t *testing.T) {
	// An unknown type must be refused before any statement is sent: the type is
	// spliced into ALTER … COMPILE where it cannot be quoted.
	if _, err := sqltext.CompileStatement("oracle", "SYSTEM", "SEQUENCE", "SEQ"); err == nil {
		t.Fatal("an unsupported object type must be refused")
	}
	// And a non-Oracle engine has no such statement at all.
	if _, err := sqltext.CompileStatement("postgresql", "public", "PROCEDURE", "p"); err == nil {
		t.Fatal("non-Oracle must refuse rather than emit an invalid statement")
	}
}
