package dbcore

import (
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
	// GENERATED ... AS IDENTITY, is_identity, identity_column). Serial columns
	// created via pg_get_serial_sequence defaults are not detected.
	IsAutoIncrement bool
}

// Index kind values reported by every driver.
const (
	IndexKindPrimary = "primary"
	IndexKindUnique  = "unique"
	IndexKindIndex   = "index"
)

type IndexInfo struct {
	Name    string
	Kind    string // IndexKindPrimary | IndexKindUnique | IndexKindIndex
	Unique  bool
	Primary bool
	Columns []string
}

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
}

type TableRowsResult struct {
	Columns []ColumnInfo
	Rows    [][]Value
	Total   uint64
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
