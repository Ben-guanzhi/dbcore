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
}

// IsNotFound reports whether err is the transaction-not-found sentinel.
func IsNotFound(err error) bool {
	e, ok := err.(*dbcore.Error)
	return ok && e.Code == dbcore.ErrCodeTransactionNotFound
}
