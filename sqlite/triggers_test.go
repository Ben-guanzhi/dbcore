package sqlite

// The TriggerLister contract on a real database: a trigger created on a table
// must be listed, and its definition must be the CREATE TRIGGER text SQLite
// keeps in sqlite_master.

import (
	"context"
	"strings"
	"testing"

	"github.com/dbcore/dbcore"
)

func TestTriggersListsAndDefines(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	ctx := context.Background()

	// The capability is present: a consumer asks for it, not for the concrete
	// driver type.
	var drv dbcore.Driver = d
	tl, ok := dbcore.AsTriggerLister(drv)
	if !ok {
		t.Fatal("the SQLite driver does not implement dbcore.TriggerLister")
	}

	for _, stmt := range []string{
		`CREATE TABLE employees (id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE audit_log (id INTEGER PRIMARY KEY, note TEXT)`,
		`CREATE TRIGGER trg_audit AFTER INSERT ON employees BEGIN
			INSERT INTO audit_log (note) VALUES ('row');
		END`,
	} {
		if _, err := d.Execute(ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	got, err := tl.Triggers(ctx, "", "employees")
	if err != nil {
		t.Fatalf("Triggers: %v", err)
	}
	if len(got) != 1 || got[0].Name != "trg_audit" {
		t.Fatalf("triggers = %+v, want one named trg_audit", got)
	}
	// SQLite records no timing/event column but does report whether the trigger
	// is enabled or was dropped.
	if got[0].Enabled == "" {
		t.Fatalf("trigger = %+v, want an enabled status", got[0])
	}

	def, err := tl.TriggerDefinition(ctx, "", "trg_audit")
	if err != nil {
		t.Fatalf("TriggerDefinition: %v", err)
	}
	upper := strings.ToUpper(def)
	if !strings.Contains(upper, "CREATE TRIGGER") || !strings.Contains(upper, "AUDIT_LOG") {
		t.Fatalf("definition = %q, want the CREATE TRIGGER text", def)
	}

	// A table without triggers answers an empty list, not an error: the concept
	// exists on this engine.
	none, err := tl.Triggers(ctx, "", "audit_log")
	if err != nil {
		t.Fatalf("Triggers on a plain table: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("triggers on audit_log = %+v, want none", none)
	}

	// A name that is not a trigger is a readable error, not an empty string.
	if _, err := tl.TriggerDefinition(ctx, "", "nope"); err == nil {
		t.Fatal("TriggerDefinition of an unknown trigger reported no error")
	}
}
