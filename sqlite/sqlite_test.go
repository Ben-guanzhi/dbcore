package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/drivertest"
)

// TestSQLiteMetadataExtras pins the metadata the shared harness does not assert:
// primary-key flags, index kinds/columns and foreign-key actions.
func TestSQLiteMetadataExtras(t *testing.T) {
	ctx := context.Background()
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	for _, ddl := range []string{
		`CREATE TABLE parent(id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE child(id INTEGER PRIMARY KEY, pid INTEGER,
			FOREIGN KEY (pid) REFERENCES parent(id) ON DELETE CASCADE ON UPDATE SET NULL)`,
		`CREATE UNIQUE INDEX ux_child_pid ON child(pid)`,
		`CREATE INDEX ix_child_pid ON child(pid)`,
	} {
		if _, err := d.Execute(ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}

	cols, err := d.ListColumns(ctx, "main", "parent")
	if err != nil {
		t.Fatalf("ListColumns: %v", err)
	}
	if len(cols) != 2 || !cols[0].IsPrimaryKey || cols[1].IsPrimaryKey {
		t.Fatalf("columns = %+v, want id PK and name not PK", cols)
	}

	idx, err := d.ListIndexes(ctx, "main", "child")
	if err != nil {
		t.Fatalf("ListIndexes: %v", err)
	}
	byName := map[string]dbcore.IndexInfo{}
	for _, i := range idx {
		byName[i.Name] = i
	}
	if i, ok := byName["ix_child_pid"]; !ok || i.Kind != dbcore.IndexKindIndex || i.Unique {
		t.Errorf("plain index = %+v", i)
	}
	if i, ok := byName["ux_child_pid"]; !ok || i.Kind != dbcore.IndexKindUnique || !i.Unique {
		t.Errorf("unique index = %+v", i)
	}
	if got := byName["ux_child_pid"].Columns; len(got) != 1 || got[0] != "pid" {
		t.Errorf("unique index columns = %v", got)
	}

	fks, err := d.ListForeignKeys(ctx, "main", "child")
	if err != nil {
		t.Fatalf("ListForeignKeys: %v", err)
	}
	if len(fks) != 1 {
		t.Fatalf("fks = %+v", fks)
	}
	fk := fks[0]
	if fk.RefTable != "parent" || fk.Name != "fk_0" || len(fk.Columns) != 1 || fk.Columns[0] != "pid" ||
		len(fk.RefColumns) != 1 || fk.RefColumns[0] != "id" {
		t.Errorf("fk = %+v", fk)
	}
	if fk.OnDelete != dbcore.FKActionCascade || fk.OnUpdate != dbcore.FKActionSetNull {
		t.Errorf("fk actions = %q / %q", fk.OnDelete, fk.OnUpdate)
	}
}

// TestNewFromDB documents the borrowed-handle contract used by client pools:
// Close is a no-op and catalog reads still work while the caller owns the DB.
func TestNewFromDB(t *testing.T) {
	db, err := sql.Open(DriverName, ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // ":memory:" is per connection

	d := NewFromDB(db)
	defer d.Close()
	if _, err := d.Execute(context.Background(), `CREATE TABLE borrowed(id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("borrowed handle was closed: %v", err)
	}
	tables, err := d.ListTables(context.Background(), "main")
	if err != nil || len(tables) != 1 || tables[0].Name != "borrowed" {
		t.Fatalf("ListTables = %+v, %v", tables, err)
	}
}

// TestSQLiteContract runs the full contract suite against an in-memory SQLite DB.
func TestSQLiteContract(t *testing.T) {
	h := drivertest.Harness{
		Make: func(ctx context.Context) (dbcore.SQLTxDriver, error) {
			return Open(":memory:")
		},
		Schema:      "main",
		Placeholder: func(idx int) string { return "?" },
	}
	h.Run(t)
}
