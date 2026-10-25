package plugindriver_test

// The wide session on a bound connection: transactions, row edits, batches,
// metrics and streaming, driven through the kernel's typed interfaces (dbcore's
// SessionTx / RowWriter / BatchWriter / MetricsSource / StreamQuerier) against
// the real fynequery SQLite plugin.
//
// This is the property ⑤ pins down: those calls used to be spelled in one
// client's private vocabulary over the raw Call escape hatch, so a second
// client had to re-implement them. They are now reachable from a bound
// connection, and a deployed plugin answers them unchanged.

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/plugindriver"
)

// TestWideSessionOnABoundConnection drives every part of the wide surface
// through one bound connection.
func TestWideSessionOnABoundConnection(t *testing.T) {
	binary := fynequerySQLitePlugin(t)
	ctx := context.Background()

	host, err := plugindriver.StartConn(ctx, binary, nil)
	if err != nil {
		t.Fatalf("start the plugin: %v", err)
	}
	defer host.Close()

	connID, _, err := host.Open(ctx, plugindriver.ConnectParams{
		Name:     "wide",
		Database: filepath.Join(t.TempDir(), "wide.db"),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	wide := host.Wide(connID, "wide")
	// A client holding a bound connection can assert the whole wide surface
	// without another lookup.
	if _, ok := dbcore.AsWide(wide); !ok {
		t.Fatal("a bound connection is not a dbcore.WideSession")
	}

	if _, err := wide.Execute(ctx, "CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	// ---- transactions -----------------------------------------------------
	if wide.InTransaction() {
		t.Fatal("a fresh connection must not report a transaction")
	}
	if err := wide.BeginTx(ctx); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if !wide.InTransaction() {
		t.Fatal("after begin the mirror must report a transaction")
	}
	if err := wide.CommitTx(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if wide.InTransaction() {
		t.Fatal("after commit the mirror must be clear")
	}

	// A rollback discards what the transaction inserted.
	if err := wide.BeginTx(ctx); err != nil {
		t.Fatalf("begin for rollback: %v", err)
	}
	if err := wide.InsertRow(ctx, "t", map[string]dbcore.Value{
		"id":   intVal(1),
		"name": textVal("doomed"),
	}); err != nil {
		t.Fatalf("insert in tx: %v", err)
	}
	if err := wide.RollbackTx(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if n := countRows(t, wide, "t"); n != 0 {
		t.Fatalf("after rollback rows = %d, want 0", n)
	}

	// ---- row-level DML ----------------------------------------------------
	if err := wide.InsertRow(ctx, "t", map[string]dbcore.Value{
		"id": intVal(1), "name": textVal("one"),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if n := countRows(t, wide, "t"); n != 1 {
		t.Fatalf("after insert rows = %d, want 1", n)
	}
	if err := wide.UpdateRow(ctx, "t",
		map[string]dbcore.Value{"id": intVal(1)},
		map[string]dbcore.Value{"name": textVal("renamed")},
	); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := firstText(t, wide, "SELECT name FROM t WHERE id = 1"); got != "renamed" {
		t.Fatalf("after update name = %q, want %q", got, "renamed")
	}
	if err := wide.DeleteRow(ctx, "t", map[string]dbcore.Value{"id": intVal(1)}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n := countRows(t, wide, "t"); n != 0 {
		t.Fatalf("after delete rows = %d, want 0", n)
	}

	// ---- batch DML --------------------------------------------------------
	if err := wide.ApplyBatch(ctx, dbcore.BatchRequest{
		Table: "t",
		Inserts: []map[string]dbcore.Value{
			{"id": intVal(2), "name": textVal("two")},
			{"id": intVal(3), "name": textVal("three")},
		},
	}); err != nil {
		t.Fatalf("apply batch: %v", err)
	}
	if n := countRows(t, wide, "t"); n != 2 {
		t.Fatalf("after batch rows = %d, want 2", n)
	}
	// One update and one delete in the same commit.
	if err := wide.ApplyBatch(ctx, dbcore.BatchRequest{
		Table:   "t",
		Updates: []dbcore.RowChange{{PK: map[string]dbcore.Value{"id": intVal(2)}, Changes: map[string]dbcore.Value{"name": textVal("two!")}}},
		Deletes: []map[string]dbcore.Value{{"id": intVal(3)}},
	}); err != nil {
		t.Fatalf("apply batch (update+delete): %v", err)
	}
	if n := countRows(t, wide, "t"); n != 1 {
		t.Fatalf("after mixed batch rows = %d, want 1", n)
	}
	if got := firstText(t, wide, "SELECT name FROM t WHERE id = 2"); got != "two!" {
		t.Fatalf("after batch update name = %q, want %q", got, "two!")
	}

	// ---- metrics ----------------------------------------------------------
	// The set is engine-specific; what matters is that the call reaches the
	// plugin and comes back rather than being refused.
	if _, err := wide.ServerMetrics(ctx); err != nil {
		t.Fatalf("server metrics: %v", err)
	}

	// ---- streaming --------------------------------------------------------
	var batches, rows int
	var cols []dbcore.ColumnInfo
	result, err := wide.QueryStream(ctx, "SELECT id, name FROM t", 1,
		func(b dbcore.StreamBatch) (bool, error) {
			batches++
			rows += len(b.Rows)
			if len(b.Columns) > 0 {
				cols = b.Columns
			}
			return true, nil
		})
	if err != nil {
		t.Fatalf("query stream: %v", err)
	}
	if result == nil {
		t.Fatal("the stream returned no terminal result")
	}
	if rows != 1 {
		t.Fatalf("streamed rows = %d, want 1", rows)
	}
	if batches == 0 {
		t.Fatal("the stream emitted no batch")
	}
	if len(cols) != 2 {
		t.Fatalf("streamed columns = %+v, want 2", cols)
	}
}

// intVal and textVal build the kernel's values, which is what the wide session
// takes (a client holding nullable strings converts at its own boundary).
func intVal(n int64) dbcore.Value   { return dbcore.Value{Kind: dbcore.ValInt, I: n} }
func textVal(s string) dbcore.Value { return dbcore.Value{Kind: dbcore.ValText, S: s} }

// countRows reads a COUNT(*) through the narrow path, so the wide calls above
// are verified by something that does not go through them.
func countRows(t *testing.T, conn dbcore.SQLDriver, table string) int64 {
	t.Helper()
	return scalarInt(t, conn, "SELECT COUNT(*) FROM "+table)
}

func scalarInt(t *testing.T, conn dbcore.SQLDriver, query string) int64 {
	t.Helper()
	res, err := conn.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("execute %q: %v", query, err)
	}
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		t.Fatalf("%q returned no cell", query)
	}
	if n, ok := dbcore.TryGetInt(res.Rows[0][0]); ok {
		return n
	}
	// A count can come back as text depending on the engine's driver.
	n, err := strconv.ParseInt(res.Rows[0][0].String(), 10, 64)
	if err != nil {
		t.Fatalf("%q returned %q, not a count", query, res.Rows[0][0].String())
	}
	return n
}

func firstText(t *testing.T, conn dbcore.SQLDriver, query string) string {
	t.Helper()
	res, err := conn.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("execute %q: %v", query, err)
	}
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		t.Fatalf("%q returned no row", query)
	}
	return res.Rows[0][0].String()
}
