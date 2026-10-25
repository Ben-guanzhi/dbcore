// Package drivertest provides the shared contract test suite (DB-CORE-CONTRACT.md §6).
// Every engine driver MUST pass Harness.Run to be considered compliant. The suite is
// engine-agnostic: the test supplies an opener, a schema name, and the dialect's
// placeholder style.
package drivertest

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/dberror"
)

// Harness drives one engine driver through the full contract.
type Harness struct {
	// Make opens a fresh driver (the test owns lifecycle; it is closed by Run).
	Make func(ctx context.Context) (dbcore.SQLTxDriver, error)
	// Schema is the catalog schema used for ListTables/ListColumns (e.g. "main", "public").
	Schema string
	// Placeholder renders the idx-th bind parameter in this engine's style.
	Placeholder func(idx int) string
	// CreateTable, when non-nil, overrides the default portable DDL
	// "CREATE TABLE <name>(id INTEGER, name TEXT)". Engines without those
	// portable types (SQL Server has no comparable TEXT, Oracle has neither)
	// supply their own.
	CreateTable func(name string) string
	// DropTable, when non-nil, overrides "DROP TABLE IF EXISTS <name>" for
	// engines without IF EXISTS (Oracle).
	DropTable func(name string) string
	// TableName, when non-nil, maps the generated table name to the engine's
	// stored form. Oracle folds unquoted identifiers to upper case, so the
	// catalog check must compare against the folded name.
	TableName func(name string) string
	// ColumnFold, when non-nil, maps a column name to the engine's stored form
	// (Oracle folds to upper case); the wide-session subtests address rows by
	// column, so they must use the stored form.
	ColumnFold func(name string) string
	// Kind selects the engine-specific fixture DDL for the catalog and extras
	// subtests (indexes/FKs/views/checks/sequences/routines). When empty, the
	// portable SQLite-shaped fixtures are used and the subtests still run.
	Kind dbcore.DatabaseKind
}

// Run executes the contract assertions. It is safe to call from a *testing.T.
func (h Harness) Run(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	drv, err := h.Make(ctx)
	if err != nil {
		t.Fatalf("open driver: %v", err)
	}
	defer drv.Close()

	q := h.Placeholder
	colFold := h.ColumnFold
	if colFold == nil {
		colFold = func(s string) string { return s }
	}
	name := fmt.Sprintf("dbcore_ct_%d", rand.Int63())
	if h.TableName != nil {
		name = h.TableName(name)
	}

	// Provision a table to introspect and mutate.
	drop := "DROP TABLE IF EXISTS " + name
	if h.DropTable != nil {
		drop = h.DropTable(name)
	}
	if _, err := drv.Execute(ctx, drop); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	create := fmt.Sprintf("CREATE TABLE %s(id INTEGER, name TEXT)", name)
	if h.CreateTable != nil {
		create = h.CreateTable(name)
	}
	if _, err := drv.Execute(ctx, create); err != nil {
		t.Fatalf("create table: %v", err)
	}
	defer drv.Execute(ctx, drop)

	t.Run("connect_ping", func(t *testing.T) {
		if err := drv.Ping(ctx); err != nil {
			t.Fatalf("ping: %v", err)
		}
	})

	t.Run("catalog_nonempty", func(t *testing.T) {
		tables, err := drv.ListTables(ctx, h.Schema)
		if err != nil {
			t.Fatalf("ListTables: %v", err)
		}
		found := false
		for _, tb := range tables {
			if tb.Name == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("created table %q not found in ListTables: %+v", name, tables)
		}
		cols, err := drv.ListColumns(ctx, h.Schema, name)
		if err != nil {
			t.Fatalf("ListColumns: %v", err)
		}
		if len(cols) == 0 {
			t.Fatalf("ListColumns returned no columns for %q", name)
		}
		for _, c := range cols {
			if c.Name != "" && c.Type == "" {
				t.Errorf("column %q has empty Type (metadata gap)", c.Name)
			}
		}
	})

	t.Run("parameterized_insert_no_inline_literal", func(t *testing.T) {
		before, err := drv.BrowseRowsCount(ctx, h.Schema, name, &dbcore.TableRowsRequest{})
		if err != nil {
			t.Fatalf("count before: %v", err)
		}
		// Value bound as a parameter — never string-concatenated (contract §6.3 / P0-4).
		if _, err := drv.Execute(ctx,
			fmt.Sprintf("INSERT INTO %s(id,name) VALUES (%s,%s)", name, q(1), q(2)),
			dbcore.Value{Kind: dbcore.ValInt, I: 42},
			dbcore.Value{Kind: dbcore.ValText, S: "hello"},
		); err != nil {
			t.Fatalf("insert: %v", err)
		}
		after, err := drv.BrowseRowsCount(ctx, h.Schema, name, &dbcore.TableRowsRequest{})
		if err != nil {
			t.Fatalf("count after: %v", err)
		}
		if after != before+1 {
			t.Fatalf("row count %d -> %d, want +1", before, after)
		}
		// Read it back through a parameterized SELECT.
		res, err := drv.Execute(ctx,
			fmt.Sprintf("SELECT id, name FROM %s WHERE id = %s", name, q(1)),
			dbcore.Value{Kind: dbcore.ValInt, I: 42},
		)
		if err != nil {
			t.Fatalf("select: %v", err)
		}
		if len(res.Rows) != 1 {
			t.Fatalf("expected 1 row, got %d", len(res.Rows))
		}
		id, ok := dbcore.TryGetInt(res.Rows[0][0])
		if !ok || id != 42 {
			t.Fatalf("id round-trip = %v (%v), want 42", id, ok)
		}
		nm, ok := dbcore.TryGetString(res.Rows[0][1])
		if !ok || nm != "hello" {
			t.Fatalf("name round-trip = %q (%v), want hello", nm, ok)
		}
	})

	t.Run("error_unique_violation", func(t *testing.T) {
		// A driver error must be classifiable without string-matching the
		// dialect in the client: the second insert into the (unique) catalog
		// column has to surface as dberror.UniqueViolation. The main table
		// has no unique constraint, so this one carries its own.
		uni := fmt.Sprintf("dbcore_ct_err_%d", rand.Int63())
		if h.TableName != nil {
			uni = h.TableName(uni)
		}
		drop := "DROP TABLE IF EXISTS " + uni
		if h.DropTable != nil {
			drop = h.DropTable(uni)
		}
		if _, err := drv.Execute(ctx, drop); err != nil {
			t.Fatalf("drop: %v", err)
		}
		create := uniqueTableDDL(h.kind(), uni)
		if _, err := drv.Execute(ctx, create); err != nil {
			t.Fatalf("create: %v", err)
		}
		defer drv.Execute(ctx, drop)

		if _, err := drv.Execute(ctx,
			fmt.Sprintf("INSERT INTO %s(id,tag) VALUES (%s,%s)", uni, q(1), q(2)),
			dbcore.Value{Kind: dbcore.ValInt, I: 7},
			dbcore.Value{Kind: dbcore.ValText, S: "dup"},
		); err != nil {
			t.Fatalf("first insert: %v", err)
		}
		_, err := drv.Execute(ctx,
			fmt.Sprintf("INSERT INTO %s(id,tag) VALUES (%s,%s)", uni, q(1), q(2)),
			dbcore.Value{Kind: dbcore.ValInt, I: 8},
			dbcore.Value{Kind: dbcore.ValText, S: "dup"},
		)
		if err == nil {
			t.Fatal("second insert should violate the unique constraint")
		}
		if got := dberror.Classify(err); got != dberror.UniqueViolation {
			t.Fatalf("class = %s (%v), want unique_violation", got, err)
		}
	})

	t.Run("browse_paging", func(t *testing.T) {
		// Seed a few more rows for paging.
		for i := 0; i < 5; i++ {
			if _, err := drv.Execute(ctx,
				fmt.Sprintf("INSERT INTO %s(id,name) VALUES (%s,%s)", name, q(1), q(2)),
				dbcore.Value{Kind: dbcore.ValInt, I: 100 + int64(i)},
				dbcore.Value{Kind: dbcore.ValText, S: fmt.Sprintf("r%d", i)},
			); err != nil {
				t.Fatalf("seed insert: %v", err)
			}
		}
		res, err := drv.BrowseRows(ctx, h.Schema, name, &dbcore.TableRowsRequest{Page: 1, PageSize: 3})
		if err != nil {
			t.Fatalf("BrowseRows: %v", err)
		}
		if len(res.Rows) != 3 {
			t.Fatalf("paged rows = %d, want 3", len(res.Rows))
		}
		if res.Total < 6 {
			t.Fatalf("BrowseRows.Total = %d, want >= 6", res.Total)
		}
	})

	t.Run("tx_commit_removes_entry", func(t *testing.T) {
		tm := dbcore.NewTxManager(drv)
		defer tm.Close()
		info, err := tm.Begin(ctx, dbcore.TxMode{})
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if _, err := tm.ExecuteInTx(ctx, info.ID,
			fmt.Sprintf("INSERT INTO %s(id,name) VALUES (%s,%s)", name, q(1), q(2)),
			dbcore.Value{Kind: dbcore.ValInt, I: 7}, dbcore.Value{Kind: dbcore.ValText, S: "tx"},
		); err != nil {
			t.Fatalf("execute in tx: %v", err)
		}
		if err := tm.Commit(info.ID); err != nil {
			t.Fatalf("commit: %v", err)
		}
		// After commit, the entry must be gone (P0-2 fix).
		if _, err := tm.Info(info.ID); err == nil {
			t.Fatalf("committed tx still visible in registry")
		}
		if err := tm.Commit(info.ID); err == nil {
			t.Fatalf("second commit should fail with NotFound")
		} else if e, ok := err.(*dbcore.Error); !ok || e.Code != dbcore.ErrCodeTransactionNotFound {
			t.Fatalf("second commit err = %v, want ErrTransactionNotFound", err)
		}
	})

	t.Run("tx_serialize_same_handle", func(t *testing.T) {
		tm := dbcore.NewTxManager(drv)
		defer tm.Close()
		info, err := tm.Begin(ctx, dbcore.TxMode{})
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		const n = 8
		var wg sync.WaitGroup
		var okCount, borrowed int64
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := tm.ExecuteInTx(ctx, info.ID,
					fmt.Sprintf("INSERT INTO %s(id,name) VALUES (%s,%s)", name, q(1), q(2)),
					dbcore.Value{Kind: dbcore.ValInt, I: int64(rand.Int31())},
					dbcore.Value{Kind: dbcore.ValText, S: "x"},
				)
				switch {
				case err == nil:
					atomic.AddInt64(&okCount, 1)
				case IsNotFound(err):
					atomic.AddInt64(&borrowed, 1)
				default:
					t.Errorf("unexpected execute-in-tx error: %v", err)
				}
			}()
		}
		wg.Wait()
		if atomic.LoadInt64(&okCount) == 0 {
			t.Fatalf("all concurrent executes failed (ok=0)")
		}
		if got := atomic.LoadInt64(&okCount) + atomic.LoadInt64(&borrowed); got != n {
			t.Fatalf("classified %d != requests %d", got, n)
		}
		if err := tm.Commit(info.ID); err != nil {
			t.Fatalf("commit: %v", err)
		}
	})

	t.Run("tx_idle_sweep_reclaims", func(t *testing.T) {
		tm := dbcore.NewTxManager(drv)
		defer tm.Close()
		tm.SetIdleTTL(-time.Second) // any tx is instantly "idle"
		info, err := tm.Begin(ctx, dbcore.TxMode{})
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		tm.SweepNow()
		if _, err := tm.Info(info.ID); err == nil {
			t.Fatalf("idle tx not reclaimed by sweep")
		}
	})

	// ---- wide session (session.go) ----
	// Optional surface: a driver that does not implement it skips these
	// subtests; every built-in engine implements the whole aggregate.

	t.Run("wide_tx_rollback_discards", func(t *testing.T) {
		w, ok := dbcore.AsWide(drv)
		if !ok {
			t.Skip("driver does not implement the wide session")
		}
		if w.InTransaction() {
			t.Fatalf("fresh session reports an active transaction")
		}
		if err := w.BeginTx(ctx); err != nil {
			t.Fatalf("begin: %v", err)
		}
		if !w.InTransaction() {
			t.Fatalf("BeginTx did not mirror the state")
		}
		if err := w.BeginTx(ctx); err == nil {
			t.Fatalf("second BeginTx should fail")
		}
		if err := w.InsertRow(ctx, name, map[string]dbcore.Value{
			colFold("id"): {Kind: dbcore.ValInt, I: 900}, colFold("name"): {Kind: dbcore.ValText, S: "doomed"},
		}); err != nil {
			_ = w.RollbackTx(ctx) // do not leak the session tx to later subtests
			t.Fatalf("insert in tx: %v", err)
		}
		if err := w.RollbackTx(ctx); err != nil {
			t.Fatalf("rollback: %v", err)
		}
		if w.InTransaction() {
			t.Fatalf("RollbackTx did not clear the state")
		}
		res, err := drv.Execute(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE id = %s", name, q(1)),
			dbcore.Value{Kind: dbcore.ValInt, I: 900})
		if err != nil {
			t.Fatalf("count: %v", err)
		}
		if c := countRows(res); c != 0 {
			t.Fatalf("rolled-back row still visible: count = %d", c)
		}
	})

	t.Run("wide_row_writer_commit", func(t *testing.T) {
		w, ok := dbcore.AsWide(drv)
		if !ok {
			t.Skip("driver does not implement the wide session")
		}
		// Missing row: UpdateRow must fail rather than silently no-op.
		if err := w.UpdateRow(ctx, name,
			map[string]dbcore.Value{colFold("id"): {Kind: dbcore.ValInt, I: 901}},
			map[string]dbcore.Value{colFold("name"): {Kind: dbcore.ValText, S: "x"}}); err == nil {
			t.Fatalf("update of a missing row should fail (affects 0 rows)")
		}
		if err := w.BeginTx(ctx); err != nil {
			t.Fatalf("begin: %v", err)
		}
		if err := w.InsertRow(ctx, name, map[string]dbcore.Value{
			colFold("id"): {Kind: dbcore.ValInt, I: 901}, colFold("name"): {Kind: dbcore.ValText, S: "first"},
		}); err != nil {
			_ = w.RollbackTx(ctx)
			t.Fatalf("insert: %v", err)
		}
		if err := w.UpdateRow(ctx, name,
			map[string]dbcore.Value{colFold("id"): {Kind: dbcore.ValInt, I: 901}},
			map[string]dbcore.Value{colFold("name"): {Kind: dbcore.ValText, S: "second"}}); err != nil {
			_ = w.RollbackTx(ctx)
			t.Fatalf("update: %v", err)
		}
		if err := w.CommitTx(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		res, err := drv.Execute(ctx, fmt.Sprintf("SELECT name FROM %s WHERE id = %s", name, q(1)),
			dbcore.Value{Kind: dbcore.ValInt, I: 901})
		if err != nil {
			t.Fatalf("select: %v", err)
		}
		if len(res.Rows) != 1 {
			t.Fatalf("expected 1 committed row, got %d", len(res.Rows))
		}
		if nm, _ := dbcore.TryGetString(res.Rows[0][0]); nm != "second" {
			t.Fatalf("committed name = %q, want second", nm)
		}
		if err := w.DeleteRow(ctx, name,
			map[string]dbcore.Value{colFold("id"): {Kind: dbcore.ValInt, I: 901}}); err != nil {
			t.Fatalf("delete: %v", err)
		}
	})

	t.Run("wide_batch_atomic", func(t *testing.T) {
		w, ok := dbcore.AsWide(drv)
		if !ok {
			t.Skip("driver does not implement the wide session")
		}
		// Seed one row to update.
		if _, err := drv.Execute(ctx,
			fmt.Sprintf("INSERT INTO %s(id,name) VALUES (%s,%s)", name, q(1), q(2)),
			dbcore.Value{Kind: dbcore.ValInt, I: 910}, dbcore.Value{Kind: dbcore.ValText, S: "seed"}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		// A batch whose one statement affects zero rows must apply nothing.
		bad := dbcore.BatchRequest{Table: name,
			Inserts: []map[string]dbcore.Value{{colFold("id"): {Kind: dbcore.ValInt, I: 911}, colFold("name"): {Kind: dbcore.ValText, S: "n"}}},
			Updates: []dbcore.RowChange{{
				PK:      map[string]dbcore.Value{colFold("id"): {Kind: dbcore.ValInt, I: 999999}},
				Changes: map[string]dbcore.Value{colFold("name"): {Kind: dbcore.ValText, S: "x"}},
			}},
		}
		if err := w.ApplyBatch(ctx, bad); err == nil {
			t.Fatalf("batch with a zero-row update should fail")
		}
		res, err := drv.Execute(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE id = %s", name, q(1)),
			dbcore.Value{Kind: dbcore.ValInt, I: 911})
		if err != nil {
			t.Fatalf("count: %v", err)
		}
		if c := countRows(res); c != 0 {
			t.Fatalf("failed batch left the insert applied: count = %d", c)
		}
		// A good batch: update + insert + DDL, applied atomically.
		good := dbcore.BatchRequest{Table: name,
			Updates: []dbcore.RowChange{{
				PK:      map[string]dbcore.Value{colFold("id"): {Kind: dbcore.ValInt, I: 910}},
				Changes: map[string]dbcore.Value{colFold("name"): {Kind: dbcore.ValText, S: "batched"}},
			}},
			Inserts: []map[string]dbcore.Value{{colFold("id"): {Kind: dbcore.ValInt, I: 911}, colFold("name"): {Kind: dbcore.ValText, S: "n"}}},
		}
		if err := w.ApplyBatch(ctx, good); err != nil {
			t.Fatalf("good batch: %v", err)
		}
		res, err = drv.Execute(ctx, fmt.Sprintf("SELECT name FROM %s WHERE id = %s", name, q(1)),
			dbcore.Value{Kind: dbcore.ValInt, I: 910})
		if err != nil {
			t.Fatalf("select: %v", err)
		}
		if nm, _ := dbcore.TryGetString(res.Rows[0][0]); nm != "batched" {
			t.Fatalf("batched update = %q, want batched", nm)
		}
	})

	t.Run("wide_stream_batches", func(t *testing.T) {
		w, ok := dbcore.AsWide(drv)
		if !ok {
			t.Skip("driver does not implement the wide session")
		}
		for i := 0; i < 5; i++ {
			if _, err := w.Execute(ctx,
				fmt.Sprintf("INSERT INTO %s(id,name) VALUES (%s,%s)", name, q(1), q(2)),
				dbcore.Value{Kind: dbcore.ValInt, I: 920 + int64(i)},
				dbcore.Value{Kind: dbcore.ValText, S: "s"}); err != nil {
				t.Fatalf("seed: %v", err)
			}
		}
		batches, rows := 0, 0
		firstColumns := 0
		// QueryStream takes no bind args (matches the wire protocol); the
		// whole contract table is streamed and the row count asserted as a
		// lower bound, since earlier subtests inserted other rows too.
		res, err := w.QueryStream(ctx, fmt.Sprintf("SELECT id, name FROM %s", name), 2,
			func(b dbcore.StreamBatch) (bool, error) {
				batches++
				if batches == 1 {
					firstColumns = len(b.Columns)
				} else if len(b.Columns) != 0 {
					return false, fmt.Errorf("later batch re-sent columns")
				}
				rows += len(b.Rows)
				return true, nil
			})
		if err != nil {
			t.Fatalf("QueryStream: %v", err)
		}
		if rows < 5 || batches < 3 || firstColumns != 2 {
			t.Fatalf("stream: rows=%d batches=%d firstColumns=%d, want >=5/>=3/2", rows, batches, firstColumns)
		}
		if len(res.Columns) != 2 {
			t.Fatalf("terminal columns = %d, want 2", len(res.Columns))
		}
		// A write statement streams as affected-row counts, no batches.
		// QueryStream carries no bind args (matches the wire protocol), so
		// this fixed test constant is written inline.
		emitted := 0
		wres, err := w.QueryStream(ctx,
			fmt.Sprintf("INSERT INTO %s(id,name) VALUES (1, 'w')", name), 2,
			func(dbcore.StreamBatch) (bool, error) { emitted++; return true, nil })
		if err != nil {
			t.Fatalf("write stream: %v", err)
		}
		if emitted != 0 || wres.AffectedRows != 1 {
			t.Fatalf("write stream: emitted=%d affected=%d, want 0/1", emitted, wres.AffectedRows)
		}
	})

	t.Run("wide_metrics", func(t *testing.T) {
		w, ok := dbcore.AsWide(drv)
		if !ok {
			t.Skip("driver does not implement the wide session")
		}
		ms, err := w.ServerMetrics(ctx)
		if err != nil {
			t.Fatalf("ServerMetrics: %v", err)
		}
		// A metrics panel is useless with one row, an empty value or an
		// unlabelled group; the contract holds every engine to a filled row.
		if len(ms) < 2 {
			t.Fatalf("ServerMetrics returned %d metrics, want at least 2", len(ms))
		}
		seen := make(map[string]bool, len(ms))
		for _, m := range ms {
			if m.Label == "" || m.Value == "" || m.Group == "" {
				t.Errorf("incomplete metric %+v (label/value/group must all be set)", m)
			}
			if seen[m.Label] {
				t.Errorf("duplicate metric label %q", m.Label)
			}
			seen[m.Label] = true
		}
	})

	t.Run("tx_rollback_all_on_teardown", func(t *testing.T) {
		tm := dbcore.NewTxManager(drv)
		defer tm.Close()
		info, err := tm.Begin(ctx, dbcore.TxMode{})
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if _, err := tm.ExecuteInTx(ctx, info.ID,
			fmt.Sprintf("INSERT INTO %s(id,name) VALUES (%s,%s)", name, q(1), q(2)),
			dbcore.Value{Kind: dbcore.ValInt, I: 9}, dbcore.Value{Kind: dbcore.ValText, S: "doomed"},
		); err != nil {
			t.Fatalf("execute in tx: %v", err)
		}
		// A teardown (connection disconnect) rolls back every live transaction.
		tm.RollbackAll()
		if _, err := tm.Info(info.ID); err == nil {
			t.Fatalf("tx survived RollbackAll")
		}
		// The rolled-back insert must not be visible; a fresh statement uses a
		// new auto-commit path, proving the registry entry is really gone.
		res, err := drv.Execute(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE id = %s", name, q(1)),
			dbcore.Value{Kind: dbcore.ValInt, I: 9})
		if err != nil {
			t.Fatalf("count after rollback: %v", err)
		}
		if counted := countRows(res); counted != 0 {
			t.Fatalf("rolled-back row still visible: count = %d", counted)
		}
	})

	// ---- catalog detail + objects beyond tables (P3-8 / P3-4) ----
	// These provision their own fixtures (a child table with an FK and two
	// indexes, then a view/check table/sequence/routine), so they run after the
	// shared table's other assertions to keep row counts predictable.
	h.runCatalogDetailContract(t, ctx, drv, name)
	h.runExtraMetadataContract(t, ctx, drv, name)
}

// countRows extracts the first integer cell of a single-row result (COUNT(*)).
func countRows(res *dbcore.QueryResult) int64 {
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return -1
	}
	v := res.Rows[0][0]
	switch v.Kind {
	case dbcore.ValInt:
		return v.I
	case dbcore.ValFloat:
		return int64(v.F)
	case dbcore.ValText:
		n := 0
		for _, r := range v.S {
			if r < '0' || r > '9' {
				return -1
			}
			n = n*10 + int(r-'0')
		}
		return int64(n)
	}
	return -1
}

// IsNotFound reports whether err is the transaction-not-found sentinel.
func IsNotFound(err error) bool {
	e, ok := err.(*dbcore.Error)
	return ok && e.Code == dbcore.ErrCodeTransactionNotFound
}
