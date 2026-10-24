// Package dbcore defines the engine-agnostic database driver abstraction shared by
// every client in the d:\tools\db workspace (see DB-CORE-CONTRACT.md).
//
// Design goals (contract §2):
//   - The value/metadata types live HERE, so dbcore has zero dependency on any
//     single app's protocol package (unlike polydb's older pkg/dbcore, which
//     returned protocol.* types).
//   - Capability layering: Driver base + optional SQLDriver / SQLTxDriver / KVDriver.
//   - Dialect is a first-class citizen (see dialect.go) so per-engine string
//     differences live in exactly one place.
package dbcore

import (
	"context"
	"database/sql"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// DatabaseKind is the single engine enum used by dialects and drivers to dispatch.
type DatabaseKind string

const (
	KindSQLite   DatabaseKind = "sqlite"
	KindPostgres DatabaseKind = "postgres"
	KindMySQL    DatabaseKind = "mysql"
	KindMSSQL    DatabaseKind = "mssql"
	KindOracle   DatabaseKind = "oracle"
	KindRedis    DatabaseKind = "redis"
)

// Error codes (contract §6: error codes must be unified across consumers).
const (
	ErrCodeNotSupported        = "ERR_NOT_SUPPORTED"
	ErrCodeTransactionNotFound = "ERR_TRANSACTION_NOT_FOUND"
	ErrCodeTransactionFailed   = "ERR_TRANSACTION_FAILED"
)

// Error is a typed error carrying a stable Code for cross-language matching.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Sentinel errors. Match with errors.Is.
var (
	ErrNotSupported        = &Error{ErrCodeNotSupported, "operation not supported for this engine"}
	ErrTransactionNotFound = &Error{ErrCodeTransactionNotFound, "transaction not found"}
	ErrTransactionFailed   = &Error{ErrCodeTransactionFailed, "transaction failed"}
)

// Driver is the base interface implemented by every engine driver.
type Driver interface {
	Kind() DatabaseKind
	Ping(ctx context.Context) error
	Close() error
	AsSQL() (SQLDriver, bool)
	AsKV() (KVDriver, bool)
}

// SQLDriver provides relational capabilities.
type SQLDriver interface {
	Driver
	Execute(ctx context.Context, query string, args ...Value) (*QueryResult, error)
	ListSchemas(ctx context.Context) ([]SchemaInfo, error)
	ListTables(ctx context.Context, schema string) ([]TableInfo, error)
	ListColumns(ctx context.Context, schema, table string) ([]ColumnInfo, error)
	ListIndexes(ctx context.Context, schema, table string) ([]IndexInfo, error)
	ListForeignKeys(ctx context.Context, schema, table string) ([]ForeignKeyInfo, error)
	CreateTableSQL(ctx context.Context, schema, table string) (string, error)
	BrowseRows(ctx context.Context, schema, table string, req *TableRowsRequest) (*TableRowsResult, error)
	BrowseRowsCount(ctx context.Context, schema, table string, req *TableRowsRequest) (uint64, error)
}

// SQLTx abstracts a statement executor; both *sql.DB and *sql.Tx satisfy it, so a
// driver's ExecuteIn can run on either uniformly.
type SQLTx interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// SQLTxDriver is the optional transaction extension of SQLDriver. Drivers that
// support transactions implement it so callers can compose Begin + ExecuteIn +
// Commit/Rollback.
type SQLTxDriver interface {
	SQLDriver
	Begin(ctx context.Context, mode TxMode) (*sql.Tx, error)
	ExecuteIn(exec SQLTx, ctx context.Context, query string, args ...Value) (*QueryResult, error)
}

// KVDriver provides Redis-style key/value capabilities.
type KVDriver interface {
	Driver
	SelectDB(ctx context.Context, idx int) error
	ScanKeys(ctx context.Context, cursor uint64, pattern string, count int) (*RedisScanPage, error)
	KeyType(ctx context.Context, key string) (RedisKeyType, error)
	GetValue(ctx context.Context, key string) (RedisValue, error)
	SetValue(ctx context.Context, key string, v RedisValue) error
	ExecCommand(ctx context.Context, args []string) (RedisReply, error)
}

// ElementWriter is optionally implemented by KV drivers that can overwrite a
// single element in place (Redis SET/HSET/LSET/ZADD). SetValue replaces the
// whole key and therefore drops its TTL; a targeted element write keeps the TTL
// and costs one command regardless of collection size.
type ElementWriter interface {
	SetElement(ctx context.Context, elem RedisElement) error
}

// ElementDeleter is optionally implemented by KV drivers that can remove one
// element (Redis HDEL/SREM/ZREM/XDEL). Lists are index-addressed and Redis only
// deletes them by value (LREM), so identity "index" is not covered, and a
// string's single element IS the key — use KeyAdmin.DeleteKeys for both.
type ElementDeleter interface {
	DeleteElement(ctx context.Context, elem RedisElement) error
}

// RowAppender is optionally implemented by KV drivers that can add rows to keys
// whose elements have no writable value: a set member (SADD) and a stream entry
// (XADD). AppendRow returns the identity the server assigned — the member, or
// the generated stream ID — so the caller can locate the new row. TTLs are left
// alone, like ElementWriter.
type RowAppender interface {
	AppendRow(ctx context.Context, row RedisRow) (string, error)
}

// KeyAdmin is optionally implemented by KV drivers that can inspect and manage
// key identities themselves. RenameKey must not overwrite an existing target
// (Redis RENAMENX): silently replacing another key's value is data loss.
// SetTTL with ttl <= 0 removes the expiry (PERSIST).
type KeyAdmin interface {
	KeyMeta(ctx context.Context, key string) (KeyMeta, error)
	DeleteKeys(ctx context.Context, keys ...string) error
	RenameKey(ctx context.Context, oldKey, newKey string) error
	SetTTL(ctx context.Context, key string, ttl time.Duration) error
}

// Connection wraps a concrete driver with type-safe capability downcast.
type Connection struct{ driver Driver }

func NewConnection(d Driver) Connection { return Connection{driver: d} }

func (c Connection) Kind() DatabaseKind { return c.driver.Kind() }
func (c Connection) Driver() Driver     { return c.driver }

func (c Connection) AsSQL() (SQLDriver, error) {
	if d, ok := c.driver.AsSQL(); ok {
		return d, nil
	}
	return nil, ErrNotSupported
}

func (c Connection) AsKV() (KVDriver, error) {
	if d, ok := c.driver.AsKV(); ok {
		return d, nil
	}
	return nil, ErrNotSupported
}

// AsSQLTx downcasts to the transaction-capable driver, if the concrete driver
// implements SQLTxDriver.
func (c Connection) AsSQLTx() (SQLTxDriver, error) {
	if d, ok := c.driver.(SQLTxDriver); ok {
		return d, nil
	}
	return nil, ErrNotSupported
}

// TxMode are Begin options. IsolationLevel is a string (read_uncommitted/
// read_committed/repeatable_read/serializable/""); the driver maps it to the
// engine dialect. Kept as string to keep dbcore free of external enum deps.
type TxMode struct{ IsolationLevel string }

// TxOptionsFromMode translates TxMode into *sql.TxOptions. Levels not mappable to
// the database/sql standard enum (e.g. MSSQL snapshot, Oracle serializable variants)
// degrade to LevelDefault; the driver may issue SET SESSION-style statements itself.
func TxOptionsFromMode(mode TxMode) *sql.TxOptions {
	opts := &sql.TxOptions{}
	switch mode.IsolationLevel {
	case "read_uncommitted":
		opts.Isolation = sql.LevelReadUncommitted
	case "read_committed":
		opts.Isolation = sql.LevelReadCommitted
	case "repeatable_read":
		opts.Isolation = sql.LevelRepeatableRead
	case "serializable":
		opts.Isolation = sql.LevelSerializable
	}
	return opts
}

// TransactionStatus mirrors a transaction's lifecycle in the registry.
type TransactionStatus string

const (
	TxnActive     TransactionStatus = "active"
	TxnCommitted  TransactionStatus = "committed"
	TxnRolledBack TransactionStatus = "rolled_back"
)

// TransactionInfo is returned to callers; the concrete *sql.Tx stays inside dbcore.
type TransactionInfo struct {
	ID     string
	Status TransactionStatus
}

// TxManager owns the transaction registry for one SQLTxDriver (one connection).
// It ports the polydb AppCore transaction semantics (contract §4.4): commit/rollback
// remove the entry; ExecuteInTx uses remove/reinsert to serialize; idle transactions
// are reclaimed by a sweeper (P0-2 residual fix).
type TxManager struct {
	driver SQLTxDriver

	mu       sync.Mutex
	txs      map[string]*txEntry
	done     chan struct{}
	closeOne sync.Once

	idleTTL    time.Duration
	maxEntries int

	counter atomic.Int64
}

type txEntry struct {
	id         string
	status     TransactionStatus
	lastActive time.Time
	driver     SQLTxDriver
	tx         *sql.Tx
}

// NewTxManager starts the idle sweeper. Call Close on shutdown.
func NewTxManager(d SQLTxDriver) *TxManager {
	m := &TxManager{
		driver:     d,
		txs:        make(map[string]*txEntry),
		done:       make(chan struct{}),
		idleTTL:    30 * time.Minute,
		maxEntries: 1024,
	}
	go m.sweeper()
	return m
}

func (m *TxManager) nextID() string {
	return "txn-" + time.Now().Format("20060102150405.000000000") + "-" + itoa(m.counter.Add(1))
}

// Begin opens a transaction and registers it.
func (m *TxManager) Begin(ctx context.Context, mode TxMode) (*TransactionInfo, error) {
	tx, err := m.driver.Begin(ctx, mode)
	if err != nil {
		return nil, err
	}
	e := &txEntry{
		id:         m.nextID(),
		status:     TxnActive,
		lastActive: time.Now(),
		driver:     m.driver,
		tx:         tx,
	}
	m.mu.Lock()
	m.txs[e.id] = e
	m.mu.Unlock()
	return &TransactionInfo{ID: e.id, Status: TxnActive}, nil
}

// Commit finalizes and removes the entry; returns ErrTransactionNotFound if absent.
// The DB call runs outside the registry lock so a slow commit cannot stall other
// transactions; the entry is gone before it starts, so a concurrent finalize sees
// NotFound instead of blocking.
func (m *TxManager) Commit(id string) error {
	m.mu.Lock()
	e, ok := m.txs[id]
	if !ok || e.status != TxnActive {
		m.mu.Unlock()
		return ErrTransactionNotFound
	}
	delete(m.txs, id)
	m.mu.Unlock()

	if err := e.tx.Commit(); err != nil {
		return &Error{ErrCodeTransactionFailed, err.Error()}
	}
	return nil
}

// Rollback finalizes and removes the entry (DB call outside the registry lock).
func (m *TxManager) Rollback(id string) error {
	m.mu.Lock()
	e, ok := m.txs[id]
	if !ok || e.status != TxnActive {
		m.mu.Unlock()
		return ErrTransactionNotFound
	}
	delete(m.txs, id)
	m.mu.Unlock()

	if err := e.tx.Rollback(); err != nil {
		return &Error{ErrCodeTransactionFailed, err.Error()}
	}
	return nil
}

// ExecuteInTx runs one statement inside the transaction. The entry is removed from
// the registry during execution (so a concurrent call sees NotFound) then reinserted,
// serializing access to the single *sql.Tx (spec §10).
func (m *TxManager) ExecuteInTx(ctx context.Context, id, query string, args ...Value) (*QueryResult, error) {
	m.mu.Lock()
	e, ok := m.txs[id]
	if !ok || e.status != TxnActive {
		m.mu.Unlock()
		return nil, ErrTransactionNotFound
	}
	delete(m.txs, id)
	m.mu.Unlock()

	res, err := m.driver.ExecuteIn(e.tx, ctx, query, args...)

	m.mu.Lock()
	if err == nil {
		e.lastActive = time.Now()
	}
	m.txs[id] = e
	m.mu.Unlock()
	return res, err
}

// Info returns the current status; ErrTransactionNotFound if not present.
func (m *TxManager) Info(id string) (*TransactionInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.txs[id]
	if !ok {
		return nil, ErrTransactionNotFound
	}
	return &TransactionInfo{ID: id, Status: e.status}, nil
}

// Close stops the sweeper (idempotent).
func (m *TxManager) Close() {
	m.closeOne.Do(func() { close(m.done) })
}

// SetIdleTTL overrides the idle threshold (primarily for tests / embedded tuning).
// It is safe to call while the sweeper runs; the new TTL applies on the next tick.
func (m *TxManager) SetIdleTTL(d time.Duration) {
	m.mu.Lock()
	m.idleTTL = d
	m.mu.Unlock()
}

// SetMaxEntries overrides the capacity cap.
func (m *TxManager) SetMaxEntries(n int) {
	m.mu.Lock()
	m.maxEntries = n
	m.mu.Unlock()
}

// SweepNow forces an immediate idle/capacity sweep (useful for tests and manual GC).
func (m *TxManager) SweepNow() { m.sweepIdleTx() }

// RollbackAll rolls back and removes every transaction (used when the owning
// connection is torn down). Entries are removed under the lock; the rollback
// I/O runs outside it, matching Commit/Rollback's discipline.
func (m *TxManager) RollbackAll() {
	m.mu.Lock()
	var txs []*sql.Tx
	for id, e := range m.txs {
		if e.tx != nil {
			txs = append(txs, e.tx)
		}
		delete(m.txs, id)
	}
	m.mu.Unlock()

	for _, tx := range txs {
		_ = tx.Rollback()
	}
}

func (m *TxManager) sweeper() {
	m.mu.Lock()
	interval := m.idleTTL / 6
	m.mu.Unlock()
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.sweepIdleTx()
		case <-m.done:
			return
		}
	}
}

// sweepIdleTx reclaims idle-timeout transactions and, when over capacity, the
// oldest. Candidates are removed from the registry under lock, then rolled back
// outside the lock so a blocking rollback can't stall the manager.
func (m *TxManager) sweepIdleTx() {
	m.mu.Lock()
	type cand struct {
		id string
		tx *sql.Tx
	}
	var cands []cand
	now := time.Now()

	for id, e := range m.txs {
		if e.status != TxnActive {
			continue
		}
		if now.Sub(e.lastActive) > m.idleTTL {
			cands = append(cands, cand{id, e.tx})
			delete(m.txs, id)
		}
	}

	if len(m.txs) > m.maxEntries {
		type kv struct {
			id         string
			tx         *sql.Tx
			lastActive time.Time
		}
		heap := make([]kv, 0, len(m.txs))
		for id, e := range m.txs {
			if e.status != TxnActive {
				continue
			}
			heap = append(heap, kv{id, e.tx, e.lastActive})
		}
		sort.Slice(heap, func(i, j int) bool { return heap[i].lastActive.Before(heap[j].lastActive) })
		excess := len(m.txs) - m.maxEntries
		for i := 0; i < excess && i < len(heap); i++ {
			cands = append(cands, cand{heap[i].id, heap[i].tx})
			delete(m.txs, heap[i].id)
		}
	}
	m.mu.Unlock()

	for _, c := range cands {
		if c.tx != nil {
			_ = c.tx.Rollback()
		}
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
