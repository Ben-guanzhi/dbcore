package sqlite_test

import (
	"context"
	"testing"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/sqlite"
)

func TestDSNEmptyFileIsInMemory(t *testing.T) {
	if got := sqlite.DSN(dbcore.ConnSpec{Kind: dbcore.KindSQLite}); got != ":memory:" {
		t.Fatalf("DSN() = %q, want :memory:", got)
	}
	if got := sqlite.DSN(dbcore.ConnSpec{Kind: dbcore.KindSQLite, File: "C:/tmp/x.db"}); got != "C:/tmp/x.db" {
		t.Fatalf("DSN() = %q", got)
	}
}

func TestOpenSpecEndToEnd(t *testing.T) {
	ctx := context.Background()
	d, err := dbcore.Open(ctx, dbcore.ConnSpec{Kind: dbcore.KindSQLite, File: ":memory:"},
		dbcore.PoolConfig{MaxOpen: 2, MaxIdle: 1})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	drv, ok := d.AsSQL()
	if !ok {
		t.Fatal("SQLite driver must expose SQLDriver")
	}
	if _, err := drv.Execute(ctx, "CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := drv.Execute(ctx, "INSERT INTO t(id,name) VALUES (?,?)",
		dbcore.Value{Kind: dbcore.ValInt, I: 1}, dbcore.Value{Kind: dbcore.ValText, S: "a"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	res, err := drv.Execute(ctx, "SELECT id,name FROM t")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if len(res.Rows) != 1 || len(res.Rows[0]) != 2 {
		t.Fatalf("result = %+v", res)
	}
	if name, ok := dbcore.TryGetString(res.Rows[0][1]); !ok || name != "a" {
		t.Fatalf("name = %v (%v)", res.Rows[0][1], ok)
	}
}
