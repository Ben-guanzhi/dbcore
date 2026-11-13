package dbcore

// Tests for SessionCore's batch commit semantics (F11): the ride/own decision,
// the sentinel that keeps a racing BeginTx from splitting the session's write
// path across two transactions, and rollback on failure. A fake database/sql
// driver records statements and transaction fates —the kernel's root tests
// cannot import an engine subpackage (import cycle), and the workspace already
// registers in-package fake drivers for exactly this reason (multi_test.go).

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func init() {
	sql.Register("dbcore_sessioncore_test", sessionCoreTestDriver{})
}

var (
	scMu         sync.Mutex
	scStatements []string
	scTxs        []*sessionCoreTestTx
	scAffected   map[string]int64
)

func sessionCoreTestReset() {
	scMu.Lock()
	defer scMu.Unlock()
	scStatements = nil
	scTxs = nil
	scAffected = nil
}

// sessionCoreTestAffected pins a statement's affected-row count, keyed by
// statement substring (e.g. table name).
func sessionCoreTestAffected(substring string, n int64) {
	scMu.Lock()
	defer scMu.Unlock()
	if scAffected == nil {
		scAffected = map[string]int64{}
	}
	scAffected[substring] = n
}

func sessionCoreTestLastTx() *sessionCoreTestTx {
	scMu.Lock()
	defer scMu.Unlock()
	if len(scTxs) == 0 {
		return nil
	}
	return scTxs[len(scTxs)-1]
}

func sessionCoreTestStatementCount() int {
	scMu.Lock()
	defer scMu.Unlock()
	return len(scStatements)
}

type sessionCoreTestDriver struct{}

func (sessionCoreTestDriver) Open(string) (driver.Conn, error) { return sessionCoreTestConn{}, nil }

type sessionCoreTestConn struct{}

func (sessionCoreTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("sessionCoreTest: prepare unsupported")
}
func (sessionCoreTestConn) Close() error { return nil }
func (sessionCoreTestConn) Begin() (driver.Tx, error) {
	return nil, errors.New("sessionCoreTest: use BeginTx")
}

func (sessionCoreTestConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	tx := &sessionCoreTestTx{}
	scMu.Lock()
	scTxs = append(scTxs, tx)
	scMu.Unlock()
	return tx, nil
}

func (sessionCoreTestConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	scMu.Lock()
	defer scMu.Unlock()
	scStatements = append(scStatements, query)
	for sub, n := range scAffected {
		if strings.Contains(query, sub) {
			return sessionCoreTestResult{n}, nil
		}
	}
	return sessionCoreTestResult{0}, nil
}

func (sessionCoreTestConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return nil, errors.New("sessionCoreTest: query unsupported")
}

type sessionCoreTestResult struct{ n int64 }

func (r sessionCoreTestResult) LastInsertId() (int64, error) { return 0, nil }
func (r sessionCoreTestResult) RowsAffected() (int64, error) { return r.n, nil }

// sessionCoreTestTx records its fate; the accessors keep -race clean.
type sessionCoreTestTx struct {
	mu         sync.Mutex
	committed  bool
	rolledBack bool
}

func (t *sessionCoreTestTx) Commit() error {
	t.mu.Lock()
	t.committed = true
	t.mu.Unlock()
	return nil
}

func (t *sessionCoreTestTx) Rollback() error {
	t.mu.Lock()
	t.rolledBack = true
	t.mu.Unlock()
	return nil
}

func (t *sessionCoreTestTx) didCommit() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.committed
}

func (t *sessionCoreTestTx) didRollback() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.rolledBack
}

// sessionCoreTestTxDriver lifts the fake *sql.DB into a SQLTxDriver so
// TxManager tests run against the same fake without an engine subpackage.
// The catalog surface is stubbed ErrNotSupported — the TxManager never calls it.
type sessionCoreTestTxDriver struct{ db *sql.DB }

func (d sessionCoreTestTxDriver) Kind() DatabaseKind              { return DatabaseKind("test") }
func (d sessionCoreTestTxDriver) Ping(ctx context.Context) error  { return d.db.PingContext(ctx) }
func (d sessionCoreTestTxDriver) Close() error                    { return d.db.Close() }
func (d sessionCoreTestTxDriver) AsSQL() (SQLDriver, bool)        { return nil, false }
func (d sessionCoreTestTxDriver) AsKV() (KVDriver, bool)          { return nil, false }
func (d sessionCoreTestTxDriver) Execute(ctx context.Context, query string, args ...Value) (*QueryResult, error) {
	return RunQuery(ctx, d.db, query, args)
}
func (d sessionCoreTestTxDriver) Begin(ctx context.Context, _ TxMode) (*sql.Tx, error) {
	return d.db.BeginTx(ctx, nil)
}
func (d sessionCoreTestTxDriver) ExecuteIn(exec SQLTx, ctx context.Context, query string, args ...Value) (*QueryResult, error) {
	return RunQuery(ctx, exec, query, args)
}

func (d sessionCoreTestTxDriver) ListSchemas(context.Context) ([]SchemaInfo, error) {
	return nil, ErrNotSupported
}
func (d sessionCoreTestTxDriver) ListTables(context.Context, string) ([]TableInfo, error) {
	return nil, ErrNotSupported
}
func (d sessionCoreTestTxDriver) ListColumns(context.Context, string, string) ([]ColumnInfo, error) {
	return nil, ErrNotSupported
}
func (d sessionCoreTestTxDriver) ListIndexes(context.Context, string, string) ([]IndexInfo, error) {
	return nil, ErrNotSupported
}
func (d sessionCoreTestTxDriver) ListForeignKeys(context.Context, string, string) ([]ForeignKeyInfo, error) {
	return nil, ErrNotSupported
}
func (d sessionCoreTestTxDriver) CreateTableSQL(context.Context, string, string) (string, error) {
	return "", ErrNotSupported
}
func (d sessionCoreTestTxDriver) BrowseRows(context.Context, string, string, *TableRowsRequest) (*TableRowsResult, error) {
	return nil, ErrNotSupported
}
func (d sessionCoreTestTxDriver) BrowseRowsCount(context.Context, string, string, *TableRowsRequest) (uint64, error) {
	return 0, ErrNotSupported
}

// sessionCoreTestDialect satisfies the root Dialect contract; only quoting and
// placeholders are exercised by the session's statement builders.
type sessionCoreTestDialect struct{}

func (sessionCoreTestDialect) Kind() DatabaseKind { return DatabaseKind("test") }
func (sessionCoreTestDialect) QuoteIdent(parts ...string) string {
	return "`" + strings.Join(parts, "`.`") + "`"
}
func (sessionCoreTestDialect) Placeholder(idx int) string { return fmt.Sprintf("@p%d", idx) }
func (sessionCoreTestDialect) TypeName(col ColumnInfo) string {
	return col.Type
}
func (sessionCoreTestDialect) LimitOffset(limit, offset int) string { return "" }
func (sessionCoreTestDialect) BuildRowsQuery(string, string, *TableRowsRequest) (string, []Value) {
	return "", nil
}
func (sessionCoreTestDialect) BuildCountQuery(string, string, *TableRowsRequest) (string, []Value) {
	return "", nil
}
func (sessionCoreTestDialect) CatalogQuery(CatalogKind, string, string) (string, []Value) {
	return "", nil
}

func newSessionCoreTest(t *testing.T) *SessionCore {
	t.Helper()
	sessionCoreTestReset()
	db, err := sql.Open("dbcore_sessioncore_test", "")
	if err != nil {
		t.Fatalf("open fake: %v", err)
	}
	db.SetMaxOpenConns(4) // the pool must hold a batch tx and a manual Conn at once
	t.Cleanup(func() { _ = db.Close() })
	return NewSessionCore(db, sessionCoreTestDialect{})
}

func sessionCoreInsert(values map[string]Value) BatchRequest {
	return BatchRequest{Table: "t", Inserts: []map[string]Value{values}}
}

// With no manual transaction open, the batch runs on its own transaction and
// commits it; the session is left with no transaction and no sentinel.
func TestSessionCoreBatchOwnTransactionCommits(t *testing.T) {
	ctx := context.Background()
	s := newSessionCoreTest(t)

	if err := s.ApplyBatch(ctx, sessionCoreInsert(map[string]Value{"a": {Kind: ValText, S: "x"}})); err != nil {
		t.Fatalf("ApplyBatch: %v", err)
	}
	tx := sessionCoreTestLastTx()
	if tx == nil {
		t.Fatal("batch opened no transaction")
	}
	if !tx.didCommit() || tx.didRollback() {
		t.Error("an own-path batch must commit its transaction")
	}
	if s.InTransaction() {
		t.Error("the batch must not leave a manual transaction open")
	}
	if sessionCoreTestStatementCount() != 1 {
		t.Errorf("statements executed = %d, want 1", sessionCoreTestStatementCount())
	}
	// The sentinel must be gone: a manual Begin works afterwards.
	if err := s.BeginTx(ctx); err != nil {
		t.Errorf("BeginTx after a finished batch: %v", err)
	}
	_ = s.RollbackTx(ctx)
}

// With a manual transaction open, the batch rides it: the statements run
// inside the manual transaction, nothing is committed by the batch, and
// rolling the manual transaction back undoes the batch with it.
func TestSessionCoreBatchRidesManualTransaction(t *testing.T) {
	ctx := context.Background()
	s := newSessionCoreTest(t)

	if err := s.BeginTx(ctx); err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	manual := sessionCoreTestLastTx()
	if manual == nil {
		t.Fatal("manual transaction not recorded")
	}

	if err := s.ApplyBatch(ctx, sessionCoreInsert(map[string]Value{"a": {Kind: ValText, S: "x"}})); err != nil {
		t.Fatalf("ApplyBatch: %v", err)
	}
	if len(func() []*sessionCoreTestTx { scMu.Lock(); defer scMu.Unlock(); return append([]*sessionCoreTestTx(nil), scTxs...) }()) != 1 {
		t.Fatalf("batch opened its own transaction; it must ride the manual one")
	}
	if manual.didCommit() {
		t.Error("the batch must not commit the manual transaction it rides")
	}
	if !s.InTransaction() {
		t.Error("the manual transaction must stay open after the batch")
	}
	if err := s.RollbackTx(ctx); err != nil {
		t.Fatalf("RollbackTx: %v", err)
	}
	if !manual.didRollback() {
		t.Error("rolling back the manual transaction must undo the batch with it")
	}
}

// A statement that fails, or one that does not affect exactly the one row it
// addressed, rolls the batch's whole transaction back —nothing of the batch
// survives, and the session is left clean.
func TestSessionCoreBatchRollsBackOnFailure(t *testing.T) {
	ctx := context.Background()
	s := newSessionCoreTest(t)

	// The insert succeeds; the update addresses one row but affected zero —
	// the ensure-one guard must fail the whole batch.
	err := s.ApplyBatch(ctx, BatchRequest{
		Table:   "t",
		Inserts: []map[string]Value{{"a": {Kind: ValText, S: "x"}}},
		Updates: []RowChange{{
			PK:      map[string]Value{"k": {Kind: ValInt, I: 1}},
			Changes: map[string]Value{"v": {Kind: ValText, S: "y"}},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "expected 1") {
		t.Fatalf("want the affect-one failure, got %v", err)
	}
	tx := sessionCoreTestLastTx()
	if tx == nil || !tx.didRollback() || tx.didCommit() {
		t.Fatalf("a failed batch must roll back its transaction (tx %+v)", tx)
	}
	if s.InTransaction() {
		t.Error("a failed batch must leave no manual transaction open")
	}
	// Statement order is updates → deletes → inserts → DDL, so the failing
	// update is first and the insert never runs.
	if sessionCoreTestStatementCount() != 1 || !strings.Contains(scStatements[0], "UPDATE") {
		t.Errorf("statements executed = %v, want only the failing update", scStatements)
	}
	// The sentinel must be gone: a manual Begin works afterwards.
	if err := s.BeginTx(ctx); err != nil {
		t.Errorf("BeginTx after a failed batch: %v", err)
	}
	_ = s.RollbackTx(ctx)
}

// While a batch runs its own committed transaction, BeginTx refuses with the
// batch reason instead of opening a second transaction on the session —the
// racing pair would otherwise put writes on two transactions that cannot see
// each other.
func TestSessionCoreBeginTxRefusedWhileBatchInFlight(t *testing.T) {
	ctx := context.Background()
	s := newSessionCoreTest(t)

	s.mu.Lock()
	s.batchTx = true
	s.mu.Unlock()

	err := s.BeginTx(ctx)
	if err == nil || !strings.Contains(err.Error(), "batch") {
		t.Fatalf("BeginTx during a batch must refuse with the batch reason, got %v", err)
	}
	if s.InTransaction() {
		t.Error("the refused BeginTx must not have opened a transaction")
	}

	s.mu.Lock()
	s.batchTx = false
	s.mu.Unlock()
	if err := s.BeginTx(ctx); err != nil {
		t.Fatalf("BeginTx after the batch finished must work: %v", err)
	}
	if err := s.RollbackTx(ctx); err != nil {
		t.Fatalf("cleanup rollback: %v", err)
	}
}

// Unloading the manager must apply its accumulated inverse: every open
// transaction is rolled back and removed before the sweeper stops. The old
// Close only stopped the sweeper — open transactions were abandoned to
// whoever remembered to call RollbackAll first.
func TestTxManagerCloseReclaimsOpenTransactions(t *testing.T) {
	ctx := context.Background()
	sessionCoreTestReset()
	db, err := sql.Open("dbcore_sessioncore_test", "")
	if err != nil {
		t.Fatalf("open fake: %v", err)
	}
	defer func() { _ = db.Close() }()

	tm := NewTxManager(sessionCoreTestTxDriver{db: db})
	info1, err := tm.Begin(ctx, TxMode{})
	if err != nil {
		t.Fatalf("begin 1: %v", err)
	}
	info2, err := tm.Begin(ctx, TxMode{})
	if err != nil {
		t.Fatalf("begin 2: %v", err)
	}

	tm.Close()

	for _, info := range []*TransactionInfo{info1, info2} {
		if _, err := tm.Info(info.ID); !errors.Is(err, ErrTransactionNotFound) {
			t.Errorf("transaction %s survived Close: %v", info.ID, err)
		}
	}
	scMu.Lock()
	txs := append([]*sessionCoreTestTx(nil), scTxs...)
	scMu.Unlock()
	if len(txs) != 2 {
		t.Fatalf("transactions recorded = %d, want 2", len(txs))
	}
	for _, tx := range txs {
		if !tx.didRollback() || tx.didCommit() {
			t.Error("Close must roll back (not commit) every open transaction")
		}
	}
	// Idempotent: a second Close sees an empty registry and a closed channel.
	tm.Close()
}
