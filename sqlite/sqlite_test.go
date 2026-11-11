package sqlite

import (
	"context"
	"database/sql"
	"fmt"
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

// TestListForeignKeysPropagatesQueryError pins the error contract: a failed
// catalog query must propagate, not be swallowed into an empty FK list. The
// old code returned (nil, nil) on any RunQuery error, which made a permission
// failure or closed connection look like "this table has no foreign keys" —
// the schemadiff then planned to DROP constraints that actually exist.
func TestListForeignKeysPropagatesQueryError(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	ctx := context.Background()
	if _, err := d.Execute(ctx, `CREATE TABLE parent(id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := d.Execute(ctx, `CREATE TABLE child(id INTEGER PRIMARY KEY, pid INTEGER REFERENCES parent(id))`); err != nil {
		t.Fatalf("create child: %v", err)
	}

	// Cancel the context before the call: QueryContext must return an error,
	// and ListForeignKeys must propagate it rather than returning (nil, nil).
	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = d.ListForeignKeys(cancelledCtx, "main", "child")
	if err == nil {
		t.Fatal("ListForeignKeys with cancelled context: expected error, got nil")
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
		Kind:        dbcore.KindSQLite,
		// SQLite has no timestamp type; the driver maps a declared TIMESTAMP
		// column onto ValTime by name. That mapping is exactly what the
		// value-kind contract checks, so the fixture declares the type.
		TimestampDDL: func(table string) []string {
			return []string{fmt.Sprintf("CREATE TABLE %s (id INTEGER, at TIMESTAMP)", table)}
		},
	}
	h.Run(t)
}

// BrowseRows tolerates a nil request (the plugin path normalizes one in), so
// the statement builders must too — a nil deref here panic'd the caller.
func TestBuildRowsQueryNilRequest(t *testing.T) {
	var d SQLiteDialect
	q, _ := d.BuildRowsQuery("", "t", nil)
	if q == "" {
		t.Fatal("nil request built an empty query")
	}
	cq, _ := d.BuildCountQuery("", "t", nil)
	if cq == "" {
		t.Fatal("nil request built an empty count query")
	}
}
