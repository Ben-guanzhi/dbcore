package dbcore

// SessionCore is the built-in driver implementation of the wide-session surface
// (session.go): a manual transaction, row-level DML, whole-commit batching,
// server metrics, streamed reads and database creation, all on top of a
// *sql.DB plus the driver's Dialect.
//
// The wide surface used to have only one implementation — plugindriver.Conn,
// which drives a plugin process over the wire. A built-in driver wanting the
// same abilities had nothing to embed. Every SQL engine package here embeds one
// SessionCore (created by NewSessionCore with its own Dialect) instead of
// re-deriving five copies of the statement shapes; only the engine-specific
// parts (metrics query, SQLite's file-based CreateDatabase) are installed per
// driver via SetMetrics / SetMetricsQuery / SetCreateDatabase.
//
// Note the two notions of transaction (see session.go): SessionCore owns a
// SessionTx — one dedicated *sql.Conn held out of the pool with a manual
// *sql.Tx on it, so a multi-statement session stays on one connection — which
// is separate from the SQLTxDriver/TxManager registry transactions.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"
)

const defaultStreamBatch = 200

// SessionCore implements SessionTx, RowWriter, BatchWriter, MetricsSource,
// StreamQuerier and DatabaseCreator. It is embedded (as a pointer) by the
// built-in drivers, which promotes those methods onto the driver types.
type SessionCore struct {
	db      *sql.DB
	dialect Dialect

	metricsFn  func(context.Context) ([]Metric, error)
	createDBFn func(context.Context, string, map[string]string) error

	mu   sync.Mutex
	conn *sql.Conn
	tx   *sql.Tx
}

// NewSessionCore builds the wide-session core for one driver. The driver keeps
// it for its lifetime; it holds no resources of its own until BeginTx.
func NewSessionCore(db *sql.DB, dialect Dialect) *SessionCore {
	return &SessionCore{db: db, dialect: dialect}
}

// SetMetrics installs the engine's curated metrics reader. Without one,
// ServerMetrics reports ErrNotSupported.
func (s *SessionCore) SetMetrics(fn func(context.Context) ([]Metric, error)) {
	s.metricsFn = fn
}

// SetMetricsQuery is the common shape: one query returning (label, value)
// rows, shown under one group heading. Engines whose metrics need several
// statements (MySQL's SHOW GLOBAL STATUS) should use SetMetrics instead.
func (s *SessionCore) SetMetricsQuery(group, query string) {
	s.metricsFn = func(ctx context.Context) ([]Metric, error) {
		return kvMetrics(ctx, s.db, group, query)
	}
}

// SetCreateDatabase overrides the default CREATE DATABASE dispatch (used by
// SQLite, whose "database" is a file, not a server-side object).
func (s *SessionCore) SetCreateDatabase(fn func(context.Context, string, map[string]string) error) {
	s.createDBFn = fn
}

// ---- manual transactions ---------------------------------------------------

// BeginTx opens a manual transaction on a dedicated connection held out of the
// pool. A second Begin without Commit/Rollback fails.
func (s *SessionCore) BeginTx(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tx != nil {
		return fmt.Errorf("transaction already active")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		_ = conn.Close()
		return err
	}
	s.conn, s.tx = conn, tx
	return nil
}

// CommitTx commits the manual transaction and returns its connection to the pool.
func (s *SessionCore) CommitTx(ctx context.Context) error {
	tx, conn, ok := s.takeTx()
	if !ok {
		return fmt.Errorf("no active transaction")
	}
	if err := tx.Commit(); err != nil {
		_ = conn.Close()
		return err
	}
	return conn.Close()
}

// RollbackTx discards the manual transaction and returns its connection to the pool.
func (s *SessionCore) RollbackTx(ctx context.Context) error {
	tx, conn, ok := s.takeTx()
	if !ok {
		return fmt.Errorf("no active transaction")
	}
	if err := tx.Rollback(); err != nil {
		_ = conn.Close()
		return err
	}
	return conn.Close()
}

// InTransaction reports the mirrored state, no round trip.
func (s *SessionCore) InTransaction() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tx != nil
}

func (s *SessionCore) takeTx() (*sql.Tx, *sql.Conn, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, conn := s.tx, s.conn
	s.tx, s.conn = nil, nil
	return tx, conn, tx != nil
}

// execer returns where statements run: the manual transaction when one is open
// (own = false — the caller owns the lifecycle), the pool otherwise.
func (s *SessionCore) execer() (SQLTx, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tx != nil {
		return s.tx, false
	}
	return s.db, true
}

// ---- row-level DML ---------------------------------------------------------

func (s *SessionCore) UpdateRow(ctx context.Context, table string, pk, changes map[string]Value) error {
	if len(pk) == 0 {
		return fmt.Errorf("update needs a primary key")
	}
	if len(changes) == 0 {
		return fmt.Errorf("update needs at least one column to change")
	}
	stmt, args := buildUpdate(s.dialect, table, pk, changes)
	return execAffectOne(ctx, s, stmt, args)
}

func (s *SessionCore) InsertRow(ctx context.Context, table string, values map[string]Value) error {
	if len(values) == 0 {
		return fmt.Errorf("insert needs at least one column")
	}
	stmt, args := buildInsert(s.dialect, table, values)
	ex, _ := s.execer()
	_, _, err := RunExec(ctx, ex, stmt, args)
	return err
}

func (s *SessionCore) DeleteRow(ctx context.Context, table string, pk map[string]Value) error {
	if len(pk) == 0 {
		return fmt.Errorf("delete needs a primary key")
	}
	stmt, args := buildDelete(s.dialect, table, pk)
	return execAffectOne(ctx, s, stmt, args)
}

func execAffectOne(ctx context.Context, s *SessionCore, stmt string, args []Value) error {
	ex, _ := s.execer()
	affected, _, err := RunExec(ctx, ex, stmt, args)
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("statement affected %d rows (expected 1); the row may have been changed by another writer", affected)
	}
	return nil
}

// buildUpdate renders `UPDATE t SET col = ?... WHERE pk...` with the dialect's
// placeholders; values travel as bind args, never interpolated (P0-4). The
// WHERE placeholders continue the SET numbering — positional dialects ($1, :1,
// @p1) have one statement-wide sequence, not one per clause.
func buildUpdate(d Dialect, table string, pk, changes map[string]Value) (string, []Value) {
	var args []Value
	b := &strings.Builder{}
	b.WriteString("UPDATE ")
	b.WriteString(d.QuoteIdent(table))
	b.WriteString(" SET ")
	first := true
	for _, col := range sortedCols(changes) {
		if !first {
			b.WriteString(", ")
		}
		first = false
		b.WriteString(d.QuoteIdent(col))
		b.WriteString(" = ")
		args = appendArg(b, d, args, len(args), changes[col])
	}
	where, wargs := wherePK(d, pk, len(args))
	args = append(args, wargs...)
	b.WriteString(" WHERE ")
	b.WriteString(where)
	return b.String(), args
}

func buildInsert(d Dialect, table string, values map[string]Value) (string, []Value) {
	cols := sortedCols(values)
	quoted := make([]string, len(cols))
	holders := make([]string, len(cols))
	args := make([]Value, 0, len(cols))
	for i, col := range cols {
		quoted[i] = d.QuoteIdent(col)
		holders[i] = d.Placeholder(i + 1)
		args = append(args, values[col])
	}
	return "INSERT INTO " + d.QuoteIdent(table) + " (" + strings.Join(quoted, ", ") +
		") VALUES (" + strings.Join(holders, ", ") + ")", args
}

func buildDelete(d Dialect, table string, pk map[string]Value) (string, []Value) {
	where, args := wherePK(d, pk, 0)
	return "DELETE FROM " + d.QuoteIdent(table) + " WHERE " + where, args
}

// wherePK renders the primary-key predicate. A NULL key column is addressed
// with IS NULL (col = NULL is never true), matching what a grid shows. from is
// the 0-based count of placeholders already in the statement.
func wherePK(d Dialect, pk map[string]Value, from int) (string, []Value) {
	var b strings.Builder
	var args []Value
	first := true
	for _, col := range sortedCols(pk) {
		if !first {
			b.WriteString(" AND ")
		}
		first = false
		b.WriteString(d.QuoteIdent(col))
		if pk[col].Kind == ValNull {
			b.WriteString(" IS NULL")
			continue
		}
		b.WriteString(" = ")
		args = appendArg(&b, d, args, from+len(args), pk[col])
	}
	return b.String(), args
}

func appendArg(b *strings.Builder, d Dialect, args []Value, idx int, v Value) []Value {
	b.WriteString(d.Placeholder(idx + 1))
	return append(args, v)
}

func sortedCols(m map[string]Value) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ---- batch DML -------------------------------------------------------------

type batchStmt struct {
	sql       string
	args      []Value
	ensureOne bool
}

// ApplyBatch applies a whole commit. When a manual transaction is active the
// statements run inside it (no commit/rollback here — the caller owns the
// lifecycle); otherwise they run in a freshly opened transaction that is
// committed on success and rolled back on any failure. Every row-addressed
// statement must affect exactly one row; anything else is a lost update and
// the whole batch fails.
func (s *SessionCore) ApplyBatch(ctx context.Context, req BatchRequest) error {
	var stmts []batchStmt
	for _, u := range req.Updates {
		if len(u.PK) == 0 || len(u.Changes) == 0 {
			return fmt.Errorf("batch update needs a primary key and at least one change")
		}
		q, a := buildUpdate(s.dialect, req.Table, u.PK, u.Changes)
		stmts = append(stmts, batchStmt{q, a, true})
	}
	for _, pk := range req.Deletes {
		if len(pk) == 0 {
			return fmt.Errorf("batch delete needs a primary key")
		}
		q, a := buildDelete(s.dialect, req.Table, pk)
		stmts = append(stmts, batchStmt{q, a, true})
	}
	for _, vals := range req.Inserts {
		if len(vals) == 0 {
			return fmt.Errorf("batch insert needs at least one column")
		}
		q, a := buildInsert(s.dialect, req.Table, vals)
		stmts = append(stmts, batchStmt{q, a, false})
	}
	for _, ddl := range req.DDL {
		stmts = append(stmts, batchStmt{ddl, nil, false})
	}
	if len(stmts) == 0 {
		return fmt.Errorf("empty batch")
	}

	ex, own := s.execer()
	if !own {
		return runBatch(ctx, ex, stmts)
	}

	s.mu.Lock()
	conn := s.conn
	tx := s.tx
	s.mu.Unlock()
	if tx != nil { // raced with BeginTx after execer saw none; run on it anyway
		return runBatch(ctx, tx, stmts)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := runBatch(ctx, tx, stmts); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		return err
	}
	return nil
}

func runBatch(ctx context.Context, ex SQLTx, stmts []batchStmt) error {
	for _, st := range stmts {
		affected, _, err := RunExec(ctx, ex, st.sql, st.args)
		if err != nil {
			return err
		}
		if st.ensureOne && affected != 1 {
			return fmt.Errorf("statement affected %d rows (expected 1); the row may have been changed by another writer", affected)
		}
	}
	return nil
}

// ---- server metrics --------------------------------------------------------

// ServerMetrics reads the engine's curated metrics, reusing the open pool
// rather than opening a connection just to ask.
func (s *SessionCore) ServerMetrics(ctx context.Context) ([]Metric, error) {
	if s.metricsFn == nil {
		return nil, ErrNotSupported
	}
	return s.metricsFn(ctx)
}

// kvMetrics runs one (label, value) query and formats the rows as metrics
// under a group heading — the same shape the plugin toolkit uses.
func kvMetrics(ctx context.Context, db *sql.DB, group, query string) ([]Metric, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Metric{}
	for rows.Next() {
		var l, v any
		if err := rows.Scan(&l, &v); err != nil {
			return nil, err
		}
		out = append(out, Metric{Label: nativeToValue(l).String(), Value: nativeToValue(v).String(), Group: group})
	}
	return out, rows.Err()
}

// ---- streaming -------------------------------------------------------------

// QueryStream reads a statement's rows in batches, handing each to emit as it
// arrives so a large result is never held whole. Read statements stream; write
// statements execute and come back as affected-row counts without batches.
// emit's false return (or an error) stops the stream early; the result then
// carries the counts gathered so far.
func (s *SessionCore) QueryStream(
	ctx context.Context,
	query string,
	batch int,
	emit func(StreamBatch) (bool, error),
) (*StreamResult, error) {
	start := time.Now()
	ex, _ := s.execer()
	if !IsReadQuery(query) {
		affected, _, err := RunExec(ctx, ex, query, nil)
		if err != nil {
			return nil, err
		}
		return &StreamResult{AffectedRows: affected, DurationMS: time.Since(start).Milliseconds()}, nil
	}
	if batch <= 0 {
		batch = defaultStreamBatch
	}

	rows, err := ex.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}
	columns := make([]ColumnInfo, len(colTypes))
	for i, ct := range colTypes {
		ci := ColumnInfo{Name: ct.Name(), Type: ct.DatabaseTypeName()}
		if nullable, ok := ct.Nullable(); ok {
			ci.Nullable = nullable
		}
		columns[i] = ci
	}

	out := &StreamResult{Columns: columns}
	sent := false

	pending := make([][]Value, 0, batch)
	flush := func() (bool, error) {
		if len(pending) == 0 {
			return true, nil
		}
		b := StreamBatch{Rows: pending}
		if !sent {
			b.Columns = columns
			sent = true
		}
		cont, err := emit(b)
		pending = make([][]Value, 0, batch)
		if err != nil {
			return false, err
		}
		return cont, nil
	}
	for rows.Next() {
		holders := make([]any, len(colTypes))
		scan := make([]any, len(colTypes))
		for i := range holders {
			holders[i] = &scan[i]
		}
		if err := rows.Scan(holders...); err != nil {
			return nil, err
		}
		vals := make([]Value, len(colTypes))
		for i, v := range scan {
			vals[i] = valueForColumn(v, colTypes[i].DatabaseTypeName())
		}
		pending = append(pending, vals)
		if len(pending) >= batch {
			cont, err := flush()
			if err != nil || !cont {
				out.DurationMS = time.Since(start).Milliseconds()
				return out, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if _, err := flush(); err != nil {
		out.DurationMS = time.Since(start).Milliseconds()
		return out, err
	}
	out.DurationMS = time.Since(start).Milliseconds()
	return out, nil
}

// ---- databases -------------------------------------------------------------

// CreateDatabase creates a database (MySQL/PostgreSQL/SQL Server) or reports a
// clear error for engines where "create a database" is not a session
// statement (Oracle uses DBCA; SQLite's database is a file — the sqlite driver
// overrides this). CREATE DATABASE cannot run inside a transaction, so it is
// issued on a dedicated pool connection.
func (s *SessionCore) CreateDatabase(ctx context.Context, name string, opts map[string]string) error {
	if s.createDBFn != nil {
		return s.createDBFn(ctx, name, opts)
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("create database: no name given")
	}
	var q string
	switch s.dialect.Kind() {
	case KindMySQL:
		q = "CREATE DATABASE " + s.dialect.QuoteIdent(name)
		if cs := opts["charset"]; cs != "" {
			if err := checkIdent("charset", cs); err != nil {
				return err
			}
			q += " CHARACTER SET " + cs
		}
		if co := opts["collation"]; co != "" {
			if err := checkIdent("collation", co); err != nil {
				return err
			}
			q += " COLLATE " + co
		}
	case KindPostgres:
		q = "CREATE DATABASE " + s.dialect.QuoteIdent(name)
		if cs := opts["charset"]; cs != "" {
			q += " ENCODING " + quotedLiteral(cs)
		}
		if co := opts["collation"]; co != "" {
			q += " LC_COLLATE " + quotedLiteral(co)
		}
	case KindMSSQL:
		q = "CREATE DATABASE " + s.dialect.QuoteIdent(name)
		if co := opts["collation"]; co != "" {
			if err := checkIdent("collation", co); err != nil {
				return err
			}
			q += " COLLATE " + co
		}
	case KindOracle:
		return fmt.Errorf("oracle does not support creating databases from a client session; use DBCA or your DBA tooling")
	case KindSQLite:
		return fmt.Errorf("sqlite: a database is a file; use dbcore/sqlite.Open to create one")
	default:
		return ErrNotSupported
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, q); err != nil {
		return fmt.Errorf("create database %q: %w", name, err)
	}
	return nil
}

// checkIdent refuses option values that are not plain identifiers — they reach
// the server as identifiers, never as bind args, so they are validated not quoted.
func checkIdent(what, v string) error {
	if v == "" {
		return fmt.Errorf("%s must not be empty", what)
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-') {
			return fmt.Errorf("%s %q is not a plain identifier", what, v)
		}
	}
	return nil
}

func quotedLiteral(v string) string {
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}
