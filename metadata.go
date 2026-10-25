package dbcore

import (
	"context"
	"strings"
	"time"
)

// Metadata structures returned by catalog introspection. They are shared across all
// consumers so each project no longer defines its own (contract §4.1, kills the
// "4x type-fold" duplication, P1-3).

type SchemaInfo struct {
	Name string
}

type TableInfo struct {
	Schema  string
	Name    string
	Type    string // "table" | "view" | "materialized_view" (PostgreSQL relkind 'm'; consumers without a matview concept can fold it into "view")
	Comment string // object comment; empty when unset or the engine has none (SQLite)
	// Rows is the engine's row-count estimate; -1 means unknown (views, SQLite,
	// or statistics that have never been gathered). ApproxRows reports whether
	// Rows is an estimate from catalog statistics rather than an exact count.
	Rows       int64
	ApproxRows bool
}

type ColumnInfo struct {
	Name         string
	Type         string
	Nullable     bool
	Default      *string
	IsPrimaryKey bool   // column participates in the primary key
	Comment      string // column comment; empty when unset or the engine has none (SQLite)
	// OrdinalPosition is the column's 1-based position in the table (attnum /
	// ORDINAL_POSITION / column_id / cid+1); 0 when the engine reports none.
	OrdinalPosition int32
	// IsAutoIncrement reports identity/auto-increment columns (AUTO_INCREMENT,
	// GENERATED ... AS IDENTITY, is_identity, identity_column) and PostgreSQL
	// serial columns, whose default is nextval(...) over the column's sequence.
	IsAutoIncrement bool
}

// Index kind values reported by every driver.
const (
	IndexKindPrimary = "primary"
	IndexKindUnique  = "unique"
	IndexKindIndex   = "index"
)

// IndexPart is one key part of an index: a plain column, an expression, and the
// part's sort direction. Expression indexes (PostgreSQL/MSSQL computed columns,
// MySQL 8 functional parts, Oracle function-based indexes, SQLite expression
// indexes) have no column name at all, so Column stays empty and Expression
// carries the engine's rendered expression text.
type IndexPart struct {
	Column     string // plain column name; empty when the part is an expression
	Expression string // expression text; empty when the part is a plain column
	Desc       bool   // key part is DESC (ASC when false, which is also the default)
}

type IndexInfo struct {
	Name    string
	Kind    string // IndexKindPrimary | IndexKindUnique | IndexKindIndex
	Unique  bool
	Primary bool
	// Columns keeps the plain column names in key order, for callers that
	// quote identifiers (DDL builders). Expression parts are not identifiers
	// and must not be quoted, so they are absent here; read Parts for them.
	Columns []string
	// Method is the engine's index access method: PostgreSQL btree/hash/gin/
	// gist/brin, MySQL BTREE/HASH/FULLTEXT/SPATIAL, SQL Server CLUSTERED/
	// NONCLUSTERED/XML/SPATIAL, Oracle NORMAL/BITMAP/FUNCTION-BASED NORMAL.
	// SQLite only has B-tree indexes, so it reports "btree" for every index;
	// empty means the engine did not report one.
	Method string
	// Parts is the ordered key list including expressions and DESC flags. Use
	// it when rendering an index faithfully instead of joining Columns.
	Parts []IndexPart
	// Predicate is the partial/filtered index predicate (PostgreSQL indpred,
	// SQL Server filter_definition, SQLite WHERE); empty for a full index.
	Predicate string
}

// CheckConstraintInfo is one CHECK constraint of a table: the constraint name
// and the engine-rendered predicate. Engines normalize the text (a wrapping
// "CHECK (...)" may or may not be included), so callers display it rather than
// parse it.
type CheckConstraintInfo struct {
	Name       string
	Expression string
}

// RoutineKind values reported by ExtraMetadataReader.Routines.
const (
	RoutineFunction  = "FUNCTION"
	RoutineProcedure = "PROCEDURE"
)

// Foreign-key actions are normalized to the SQL standard spellings so callers
// do not branch on engine catalogs (MSSQL/NULL, MySQL, SQLite all differ).
const (
	FKActionNoAction   = "NO ACTION"
	FKActionCascade    = "CASCADE"
	FKActionSetNull    = "SET NULL"
	FKActionSetDefault = "SET DEFAULT"
	FKActionRestrict   = "RESTRICT"
)

type ForeignKeyInfo struct {
	Name       string
	Columns    []string
	RefSchema  string
	RefTable   string
	RefColumns []string
	OnDelete   string // normalized action; empty when the engine did not report one
	OnUpdate   string
}

// NormalizeFKAction maps engine spellings (NO_ACTION, set null, ...) to the
// standard form so callers never branch on catalog dialects.
func NormalizeFKAction(s string) string {
	upper := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), "_", " "))
	switch upper {
	case "":
		return ""
	case "NO ACTION":
		return FKActionNoAction
	case "CASCADE":
		return FKActionCascade
	case "SET NULL":
		return FKActionSetNull
	case "SET DEFAULT":
		return FKActionSetDefault
	case "RESTRICT":
		return FKActionRestrict
	default:
		return upper
	}
}

// SortKey and FilterExpr describe BrowseRows ordering/filtering (parameterized:
// filter values are bound, never concatenated — contract §6.3 / P0-4).
type SortKey struct {
	Column string
	Desc   bool
}

type FilterOp string

const (
	FilterEq   FilterOp = "="
	FilterNeq  FilterOp = "!="
	FilterGt   FilterOp = ">"
	FilterGte  FilterOp = ">="
	FilterLt   FilterOp = "<"
	FilterLte  FilterOp = "<="
	FilterLike FilterOp = "like"
	// FilterIsNull and FilterIsNotNull compare against nothing: the value is
	// ignored, and a driver renders them as IS NULL / IS NOT NULL rather than
	// "= NULL", which SQL does not allow.
	FilterIsNull    FilterOp = "is null"
	FilterIsNotNull FilterOp = "is not null"
)

type FilterExpr struct {
	Column string
	Op     FilterOp
	Value  Value
}

type TableRowsRequest struct {
	Page     int
	PageSize int
	Sort     []SortKey
	Filter   []FilterExpr
	// After turns paging into keyset continuation: values aligned with Sort.
	// Exactly one sort key is supported (the guarded deep-paging case);
	// anything else degrades to OFFSET. When set, Page/offset are ignored.
	After []Value
}

type TableRowsResult struct {
	Columns []ColumnInfo
	Rows    [][]Value
	Total   uint64
	// TotalFound reports whether Total was actually read. Without it a failed
	// COUNT (permissions, cancelled context) is indistinguishable from an
	// empty table, and the client's pager would say "0 rows" about a table it
	// is holding rows of.
	TotalFound bool
}

// QueryResult is the generic result of Execute.
type QueryResult struct {
	Columns      []ColumnInfo
	Rows         [][]Value
	AffectedRows int64
	LastInsertID int64
}

// Redis/KV types (contract §4.6). Kept minimal but present so KVDriver is complete.
type RedisKeyType string

const (
	RedisNone   RedisKeyType = "none"
	RedisStr    RedisKeyType = "string"
	RedisList   RedisKeyType = "list"
	RedisSet    RedisKeyType = "set"
	RedisZSet   RedisKeyType = "zset"
	RedisHash   RedisKeyType = "hash"
	RedisStream RedisKeyType = "stream"
)

type RedisValue struct {
	Type  RedisKeyType
	Str   string
	Int   int64
	Items []string
	// Scores is only populated for sorted sets and runs parallel to Items.
	Scores []float64
	Hash   map[string]string
	// Stream carries the entries of a stream key with identity and fields kept
	// separate, so a consumer renders its own display format instead of parsing
	// a rendered blob (Items keeps the flat "id k=v" rendering for callers
	// built before Stream existed).
	Stream []RedisStreamEntry
}

// RedisStreamEntry is one stream entry: the server-assigned ID and its fields.
type RedisStreamEntry struct {
	ID     string
	Fields []RedisField
}

// RedisElement identifies one editable cell inside a key: Identity names the
// element's identity column ("key" for the key itself, "field", "index" or
// "member"), ID is that column's value, Column is the writable value column
// ("value", or "score" for sorted sets) and Value is the new text.
type RedisElement struct {
	Key      string
	Identity string
	ID       string
	Column   string
	Value    string
}

// RedisField is one field/value pair of a stream entry.
type RedisField struct {
	Name  string
	Value string
}

// RedisRow is one row to append to a key whose rows are addressed by value
// rather than position: a set member or a stream entry. RedisElement describes
// an existing cell to overwrite; a set member and a stream entry have no
// writable value, so SADD/XADD add a row instead.
type RedisRow struct {
	Key    string
	Kind   RedisKeyType // RedisSet or RedisStream
	Member string       // set member
	ID     string       // stream entry id; empty or "*" lets the server assign
	Fields []RedisField // stream fields, in order
}

// KeyMeta describes a key's identity: its type and, when the key expires, the
// remaining lifetime.
type KeyMeta struct {
	Kind   RedisKeyType
	TTL    time.Duration // meaningful only when HasTTL
	HasTTL bool          // false = the key never expires
}

type RedisScanPage struct {
	Cursor uint64
	Keys   []string
}

type RedisReply struct {
	Raw string
}

// RedisKeyEntry is one key of an enriched SCAN page: identity fused with type
// and remaining lifetime, so a consumer can render a page with one driver call
// instead of one TYPE+TTL round trip per key.
type RedisKeyEntry struct {
	Key  string
	Kind RedisKeyType
	// TTL collapses a raw TTL reply to seconds: positive = remaining seconds;
	// TTLNoExpiry / TTLMissing pass the sentinel through as-is. Nil means no
	// TTL was reported.
	TTL *int64
}

// Raw TTL sentinels as a TTL/PTTL reply reports them.
const (
	TTLNoExpiry int64 = -1
	TTLMissing  int64 = -2
)

// EnrichedScanPage is what KeyScanner.ScanKeysEnriched returns.
type EnrichedScanPage struct {
	Cursor uint64
	Keys   []RedisKeyEntry
}

// KeyScanner is optionally implemented by KV drivers that fuse the per-key
// type/TTL enrichment and the "fill the page" scan loop into the driver: scan
// rounds continue until count keys are collected, the cursor is exhausted, or
// the round cap (100, spec/behavior.md §1.3 "D5") is hit — the continuation
// cursor then names the real resume point.
type KeyScanner interface {
	ScanKeysEnriched(ctx context.Context, cursor uint64, pattern string, count int) (*EnrichedScanPage, error)
}

// Structured RESP reply kinds (TypedCommander).
const (
	RedisReplyKindNull       = "null"
	RedisReplyKindBulkString = "bulk_string"
	RedisReplyKindInteger    = "integer"
	RedisReplyKindError      = "error"
	RedisReplyKindArray      = "array"
)

// RedisTypedReply is a structured alternative to RedisReply's rendered text:
// simple and bulk strings both collapse to bulk_string (go-redis cannot tell
// them apart, and the Rust redis crate reads both as bulk), nil is null, and
// errors nested in arrays keep their kind.
type RedisTypedReply struct {
	Kind  string // RedisReplyKind*
	Value any    // string | int64 | []RedisTypedReply
}

// TypedCommander is optionally implemented by KV drivers that can return the
// raw command reply as structure instead of rendered text.
type TypedCommander interface {
	ExecCommandTyped(ctx context.Context, args []string) (RedisTypedReply, error)
}

// SequenceInfo is one row of ExtraMetadataReader.Sequences. Start is the
// sequence's declared start value and Increment its step; both are 0 when the
// engine refused to report them (PostgreSQL hides start values the current role
// may not use). Oracle keeps no START WITH in its data dictionary, so its Start
// is the sequence's MIN_VALUE — the same number unless the two differ.
type SequenceInfo struct {
	Name      string
	Start     int64
	Increment int64
}

// RoutineInfo is one row of ExtraMetadataReader.Routines; Kind is
// RoutineFunction or RoutineProcedure.
type RoutineInfo struct {
	Name string
	Kind string
}
