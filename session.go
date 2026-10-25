package dbcore

import "context"

// The wide session surface.
//
// SQLDriver is the narrow, statement-oriented contract every relational driver
// answers: run a statement, read the catalog. A few clients also need a *wide*
// session — one that owns a manual transaction, applies row edits addressed by
// primary key, reports server metrics and streams a large result in batches.
//
// Those capabilities used to live only in one client's plugin protocol
// (fynequery drove them through a private method vocabulary over the kernel's
// raw Call/CallStream escape hatch), so a second client wanting the same
// abilities had to re-implement every call by hand. They are declared here
// instead, in the kernel's own shape — context in, dbcore.Value on the wire —
// which any driver, built-in or plugin, can implement.
//
// Everything here is optional, the way KVDriver's ElementWriter / KeyAdmin are:
// a driver implements what it can, and a caller asserts the capability it needs
// before using it. AsWide and the per-capability helpers below do that in one
// line.
//
// Note the two different notions of "transaction":
//   - SQLTxDriver is the kernel's own transaction: TxManager holds an *sql.Tx
//     and hands it to the driver's ExecuteIn.
//   - SessionTx here is a transaction the *session* owns — a plugin process
//     keeps it on its side of the wire and the kernel only drives it
//     (begin / commit / rollback). A driver must not satisfy both for the same
//     underlying transaction.

// Metric is one labelled server metric, as the metrics panel shows it. Value is
// already formatted for display; Group is the optional section heading.
type Metric struct {
	Label string
	Value string
	Group string
}

// RowChange pairs the primary key that identifies one row with the columns to
// change in it. A key column that is missing means the row cannot be addressed.
type RowChange struct {
	PK      map[string]Value
	Changes map[string]Value
}

// BatchRequest is one whole commit: row edits, deletions, inserts and optional
// structural statements, applied atomically. A driver that cannot run the DDL
// inside the same transaction must reject the whole request rather than apply
// part of it.
type BatchRequest struct {
	Table   string
	Updates []RowChange
	Deletes []map[string]Value
	Inserts []map[string]Value
	DDL     []string
}

// StreamBatch is one batch of a streamed result. The first batch carries the
// column metadata; later ones may leave Columns empty, because the columns do
// not change mid-stream.
type StreamBatch struct {
	Columns []ColumnInfo
	Rows    [][]Value
}

// SessionTx is a manual transaction owned by the session, not by the kernel's
// TxManager. InTransaction mirrors the session's state rather than asking it:
// most engines cannot report "am I in a transaction" without one to ask, and a
// client that needs the answer before it sends anything should not pay a round
// trip for it.
type SessionTx interface {
	BeginTx(ctx context.Context) error
	CommitTx(ctx context.Context) error
	RollbackTx(ctx context.Context) error
	InTransaction() bool
}

// RowWriter is row-level DML addressed by primary key, backing an editable grid
// one row at a time. A caller that can stage a whole commit should prefer
// BatchWriter: one round trip, one transaction.
type RowWriter interface {
	UpdateRow(ctx context.Context, table string, pk, changes map[string]Value) error
	InsertRow(ctx context.Context, table string, values map[string]Value) error
	DeleteRow(ctx context.Context, table string, pk map[string]Value) error
}

// BatchWriter applies a whole commit inside one transaction, with the same
// affect-exactly-one-row guarantee a row-at-a-time caller would get.
type BatchWriter interface {
	ApplyBatch(ctx context.Context, req BatchRequest) error
}

// MetricsSource reports a curated set of read-only server metrics.
type MetricsSource interface {
	ServerMetrics(ctx context.Context) ([]Metric, error)
}

// StreamResult is the terminal aggregate of a streamed statement: what the
// session reports once the last batch has gone out. The rows themselves never
// land here — they are handed to the caller batch by batch — so this carries the
// counts and the server-side duration, not a result set.
type StreamResult struct {
	Columns      []ColumnInfo
	AffectedRows int64
	// DurationMS is the duration the session measured, 0 when it reports none.
	// A caller that needs a figure regardless should time the call itself.
	DurationMS int64
}

// StreamQuerier reads a statement's result incrementally, handing each batch to
// emit as it arrives so a large result is never held in memory at once. emit
// receives the column metadata on the first call; later calls may leave
// Columns empty. Returning false (or an error) stops the stream early.
type StreamQuerier interface {
	QueryStream(ctx context.Context, query string, batch int, emit func(StreamBatch) (bool, error)) (*StreamResult, error)
}

// DatabaseCreator creates a database or schema on the server, reusing the
// already open session. charset/collation arrive as opts (e.g.
// {"charset": "utf8mb4"}); an implementor validates and quotes them, because
// they reach the server as identifiers.
type DatabaseCreator interface {
	CreateDatabase(ctx context.Context, name string, opts map[string]string) error
}

// WideSession is the whole wide surface a session can offer. It is a
// convenience aggregate: a driver implementing only part of the surface is not
// a WideSession, and callers wanting one capability should assert that one
// with the helpers below.
type WideSession interface {
	SQLDriver
	SessionTx
	RowWriter
	BatchWriter
	MetricsSource
	StreamQuerier
	DatabaseCreator
}

// Capability assertions. Each reports whether the driver implements one part of
// the wide surface, so a caller can degrade instead of fail: no metrics panel
// for a session without metrics, a row-at-a-time edit path for one without
// batching.

func AsWide(d Driver) (WideSession, bool)            { w, ok := d.(WideSession); return w, ok }
func AsSessionTx(d Driver) (SessionTx, bool)         { w, ok := d.(SessionTx); return w, ok }
func AsRowWriter(d Driver) (RowWriter, bool)         { w, ok := d.(RowWriter); return w, ok }
func AsBatchWriter(d Driver) (BatchWriter, bool)     { w, ok := d.(BatchWriter); return w, ok }
func AsMetricsSource(d Driver) (MetricsSource, bool) { w, ok := d.(MetricsSource); return w, ok }
func AsStreamQuerier(d Driver) (StreamQuerier, bool) { w, ok := d.(StreamQuerier); return w, ok }
func AsDatabaseCreator(d Driver) (DatabaseCreator, bool) {
	w, ok := d.(DatabaseCreator)
	return w, ok
}

// KeyPageReader is the optional capability for reading large collection keys
// in windows instead of whole (HGETALL / SMEMBERS / LRANGE 0 -1 on a
// million-member key would end every client). List and zset page by rank with
// the collection length for the pager; hash and set page by SCAN cursor, the
// only way Redis offers them. Optional in the ElementWriter style: consumers
// assert with AsKeyPageReader and fall back to full reads.
type KeyPageReader interface {
	// ListRange reads list items [start, stop] (Redis semantics, -1 = end)
	// plus LLEN, so the UI can size its pager.
	ListRange(ctx context.Context, key string, start, stop int64) (items []string, length int64, err error)
	// HashPage reads one HSCAN step. cursor 0 starts the walk; the returned
	// cursor is passed back until it comes back 0.
	HashPage(ctx context.Context, key string, cursor uint64, count int64) (fields []RedisField, next uint64, err error)
	// SetPage reads one SSCAN step.
	SetPage(ctx context.Context, key string, cursor uint64, count int64) (members []string, next uint64, err error)
	// ZSetRange reads zset members by rank with their scores, plus ZCARD.
	ZSetRange(ctx context.Context, key string, start, stop int64) (members []string, scores []float64, length int64, err error)
	// StreamRange reads up to count entries strictly after afterID ("" = from
	// the start), plus XLEN, so a stream browser can continue where it left
	// off instead of only ever seeing the first hundred entries.
	StreamRange(ctx context.Context, key, afterID string, count int64) (entries []RedisStreamEntry, length int64, err error)
}

// AsKeyPageReader is the assertion helper matching the AsSQLTx family.
func AsKeyPageReader(d Driver) (KeyPageReader, bool) {
	p, ok := d.(KeyPageReader)
	return p, ok
}

// AsElementWriter, AsElementDeleter, AsRowAppender, AsKeyAdmin, AsKeyScanner
// and AsTypedCommander assert the single-key capabilities defined in
// dbcore.go. The helpers exist so a consumer never type-asserts the driver
// type directly (the plugin driver and the built-in driver can both serve).
func AsElementWriter(d Driver) (ElementWriter, bool) {
	w, ok := d.(ElementWriter)
	return w, ok
}

func AsElementDeleter(d Driver) (ElementDeleter, bool) {
	w, ok := d.(ElementDeleter)
	return w, ok
}

func AsRowAppender(d Driver) (RowAppender, bool) {
	w, ok := d.(RowAppender)
	return w, ok
}

func AsKeyAdmin(d Driver) (KeyAdmin, bool) {
	w, ok := d.(KeyAdmin)
	return w, ok
}

func AsKeyScanner(d Driver) (KeyScanner, bool) {
	w, ok := d.(KeyScanner)
	return w, ok
}

func AsTypedCommander(d Driver) (TypedCommander, bool) {
	w, ok := d.(TypedCommander)
	return w, ok
}

// ExtraMetadataReader is the optional capability for the catalog facts beyond
// tables/columns/indexes/foreign keys: a view's DDL, a table's CHECK
// constraints, and a schema's sequences and routines (functions/procedures).
//
// The interface is aggregate but the concepts are not universal, so each
// method answers ErrNotSupported for an engine that has no such concept
// (SQLite: sequences and routines; MySQL: sequences; SQLite: CHECK constraint
// catalog). That keeps "not applicable" distinguishable from "the read failed",
// which is what lets a client hide the section instead of showing an empty one.
//
// Recipes live in sqltext (objects.go); drivers only execute and map. Optional
// in the ElementWriter/KeyPageReader style: consumers assert with
// AsExtraMetadata and degrade when the driver predates this capability.
type ExtraMetadataReader interface {
	// ViewDefinition returns the DDL that recreates the view: complete CREATE
	// VIEW text where the engine can produce it (SQLite/MSSQL) or a composed
	// CREATE OR REPLACE VIEW ... AS <body> elsewhere.
	ViewDefinition(ctx context.Context, schema, view string) (string, error)
	// CheckConstraints lists one table's CHECK constraints in name order.
	CheckConstraints(ctx context.Context, schema, table string) ([]CheckConstraintInfo, error)
	// Sequences lists one schema's sequences in name order.
	Sequences(ctx context.Context, schema string) ([]SequenceInfo, error)
	// Routines lists one schema's functions and procedures in name order.
	Routines(ctx context.Context, schema string) ([]RoutineInfo, error)
}

// AsExtraMetadata is the capability assertion helper, matching AsSQLTx and
// friends.
func AsExtraMetadata(d Driver) (ExtraMetadataReader, bool) {
	x, ok := d.(ExtraMetadataReader)
	return x, ok
}
