// Package plugindriver runs a database driver in a child process.
//
// A driver plugin is an ordinary executable that speaks newline-delimited JSON on
// its stdin/stdout: it prints a banner line first, then answers one request per
// line. Doing the driver out of process buys three things a linked driver cannot
// have: a driver written in another language, a driver whose crash kills only the
// child, and one binary shipping drivers it cannot compile on every platform.
//
// The cost is a process and a pipe per connection, which is why it is opt-in: the
// built-in engines stay in-process.
package plugindriver

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/dbcore/dbcore"
)

// ProtocolName and ProtocolVersion identify the wire protocol in the banner. A host
// refuses a plugin that answers anything else, so a stale binary fails at startup
// with a clear message instead of at the first query.
const (
	ProtocolName    = "dbcore-plugin"
	ProtocolVersion = 1
	// LegacyProtocolName is what the protocol was called when it lived in
	// fynequery. A host still accepts it, so the plugin binaries deployed before
	// the move keep working.
	LegacyProtocolName = "fynequery-plugin"
)

// Banner is the first line a plugin writes, so the host can verify what it launched
// before sending a request.
type Banner struct {
	Proto   string `json:"proto"`
	Version int    `json:"version"`
	// Kind is the engine this plugin implements, as dbcore names it
	// ("postgres", "mysql", "oracle", ...).
	Kind string `json:"kind"`
	// Code is the same engine in the fynequery vocabulary, so a plugin written
	// for either client is understood by either host.
	Code string `json:"code,omitempty"`
	Name string `json:"name"`
	// PluginVersion is the plugin's own version, for diagnostics only.
	PluginVersion string `json:"pluginVersion,omitempty"`
}

// Request is one host→plugin call.
type Request struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is one plugin→host reply, matched to a Request by ID. A non-empty Error
// fails the call; Done marks the last frame of a streaming reply.
type Response struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
	// Code is a dbcore error code when the plugin can name one
	// (ERR_NOT_SUPPORTED, ...). A host maps it back to its own sentinel.
	Code string `json:"code,omitempty"`
	Done bool   `json:"done,omitempty"`
}

// Method names. Additive only: a plugin that predates a method answers
// ERR_NOT_SUPPORTED, and the host degrades to the capability it does have.
const (
	MethodPing          = "ping"
	MethodClose         = "close"
	MethodExecute       = "execute"
	MethodListSchemas   = "listSchemas"
	MethodListTables    = "listTables"
	MethodListColumns   = "listColumns"
	MethodListIndexes   = "listIndexes"
	MethodListForeign   = "listForeignKeys"
	MethodCreateTableDB = "createTableSQL"
	MethodBrowseRows    = "browseRows"
	MethodBrowseCount   = "browseRowsCount"
	MethodBegin         = "begin"
	MethodExecuteIn     = "executeIn"
	MethodCommit        = "commit"
	MethodRollback      = "rollback"
)

// ---------- parameter and result payloads ----------

// SchemaArgs is the payload of the schema-ish catalog calls.
type SchemaArgs struct {
	Schema string `json:"schema,omitempty"`
	Table  string `json:"table,omitempty"`
}

// BrowseArgs is the payload of browseRows / browseRowsCount. It mirrors
// dbcore.TableRowsRequest: a plugin renders the statement from these parts, so the
// host never sends SQL for a browse.
type BrowseArgs struct {
	Schema   string      `json:"schema,omitempty"`
	Table    string      `json:"table"`
	Page     int         `json:"page,omitempty"`
	PageSize int         `json:"pageSize,omitempty"`
	Sort     []SortArg   `json:"sort,omitempty"`
	Filter   []FilterArg `json:"filter,omitempty"`
}

// SortArg is one dbcore.SortKey.
type SortArg struct {
	Column string `json:"column"`
	Desc   bool   `json:"desc,omitempty"`
}

// FilterArg is one dbcore.FilterExpr.
type FilterArg struct {
	Column string      `json:"column"`
	Op     string      `json:"op"`
	Value  CellPayload `json:"value"`
}

// BrowseResult mirrors dbcore.TableRowsResult on the wire.
type BrowseResult struct {
	Columns []ColumnPayload `json:"columns"`
	Rows    [][]CellPayload `json:"rows"`
	Total   uint64          `json:"total,omitempty"`
}

// ColumnPayload is dbcore.ColumnInfo on the wire. Default is a pointer so "no
// default" and "default is the empty string" stay distinct, the same reason the
// kernel keeps it a *string.
type ColumnPayload struct {
	Name         string  `json:"name"`
	Type         string  `json:"type"`
	Nullable     bool    `json:"nullable"`
	Default      *string `json:"default,omitempty"`
	IsPrimaryKey bool    `json:"isPrimaryKey,omitempty"`
	Comment      string  `json:"comment,omitempty"`
	// Key is the fynequery spelling of the primary-key marker ("PK"), which
	// arrives on the streaming path: a plugin written for fynequery names the
	// column's key where a plugin written for the kernel sets isPrimaryKey.
	// Both are accepted, so either plugin marks its primary keys on either host
	// (see isPrimaryColumn).
	Key string `json:"key,omitempty"`
}

// isPrimaryColumn reports whether a column payload marks a primary key, in
// either spelling.
func isPrimaryColumn(c ColumnPayload) bool { return c.IsPrimaryKey || c.Key == "PK" }

// CellPayload is one dbcore.Value on the wire.
//
// A tagged struct rather than a bare JSON scalar: JSON has one number type, and a
// host that had to guess whether 1 is an int or a float would corrupt big integers
// and decimals — the same trap the grid's text cursors exist to avoid.
type CellPayload struct {
	Kind string  `json:"k"`
	Int  int64   `json:"i,omitempty"`
	Flt  float64 `json:"f,omitempty"`
	Text string  `json:"s,omitempty"`
	Blob []byte  `json:"b,omitempty"`
	Bool bool    `json:"t,omitempty"`
	// Time is RFC3339Nano in UTC.
	Time string `json:"tm,omitempty"`
}

// ResultPayload is a dbcore.QueryResult on the wire.
type ResultPayload struct {
	Columns      []ColumnPayload `json:"columns"`
	Rows         [][]CellPayload `json:"rows"`
	AffectedRows int64           `json:"affectedRows"`
	LastInsertID int64           `json:"lastInsertId"`
}

// StreamFrame is one frame of a streaming reply (see Driver.CallStream). The
// first frame carries the column metadata; the terminal frame — the one the
// reply marks Done — carries the aggregate counters as well.
type StreamFrame struct {
	Columns      []ColumnPayload `json:"columns,omitempty"`
	Rows         [][]CellPayload `json:"rows,omitempty"`
	RowsAffected int64           `json:"rowsAffected,omitempty"`
	DurationMS   int64           `json:"durationMs"`
}

// TablePayload is dbcore.TableInfo on the wire.
type TablePayload struct {
	Schema     string `json:"schema"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Comment    string `json:"comment,omitempty"`
	Rows       int64  `json:"rows,omitempty"`
	ApproxRows bool   `json:"approxRows,omitempty"`
}

// ---------- conversions ----------

// EncodeValue maps a kernel cell onto the wire. A plugin needs it to answer a call.
func EncodeValue(v dbcore.Value) CellPayload {
	switch v.Kind {
	case dbcore.ValInt:
		return CellPayload{Kind: "int", Int: v.I}
	case dbcore.ValFloat:
		return CellPayload{Kind: "float", Flt: v.F}
	case dbcore.ValText:
		return CellPayload{Kind: "text", Text: v.S}
	case dbcore.ValBlob:
		return CellPayload{Kind: "blob", Blob: v.B}
	case dbcore.ValBool:
		return CellPayload{Kind: "bool", Bool: v.Bool}
	case dbcore.ValTime:
		return CellPayload{Kind: "time", Time: v.T.UTC().Format(timeFormat)}
	default:
		return CellPayload{Kind: "null"}
	}
}

// timeFormat keeps the nanoseconds a database column can hold: truncating to
// seconds would silently change a timestamp's value on the way through the pipe.
const timeFormat = "2006-01-02T15:04:05.999999999Z07:00"

// DecodeValue is the inverse of EncodeValue. An unknown kind is an error rather
// than a silent NULL: a dropped cell is worse than a failed call.
func DecodeValue(c CellPayload) (dbcore.Value, error) {
	switch c.Kind {
	case "", "null":
		return dbcore.Value{Kind: dbcore.ValNull}, nil
	case "int":
		return dbcore.Value{Kind: dbcore.ValInt, I: c.Int}, nil
	case "float":
		return dbcore.Value{Kind: dbcore.ValFloat, F: c.Flt}, nil
	case "text":
		return dbcore.Value{Kind: dbcore.ValText, S: c.Text}, nil
	case "blob":
		return dbcore.Value{Kind: dbcore.ValBlob, B: c.Blob}, nil
	case "bool":
		return dbcore.Value{Kind: dbcore.ValBool, Bool: c.Bool}, nil
	case "time":
		t, err := time.Parse(timeFormat, c.Time)
		if err != nil {
			return dbcore.Value{}, fmt.Errorf("plugindriver: bad time %q: %w", c.Time, err)
		}
		return dbcore.Value{Kind: dbcore.ValTime, T: t}, nil
	}
	return dbcore.Value{}, fmt.Errorf("plugindriver: unknown cell kind %q", c.Kind)
}

func encodeColumn(c dbcore.ColumnInfo) ColumnPayload {
	return ColumnPayload{
		Name:         c.Name,
		Type:         c.Type,
		Nullable:     c.Nullable,
		Default:      c.Default,
		IsPrimaryKey: c.IsPrimaryKey,
		Comment:      c.Comment,
	}
}

func decodeColumn(c ColumnPayload) dbcore.ColumnInfo {
	return dbcore.ColumnInfo{
		Name:         c.Name,
		Type:         c.Type,
		Nullable:     c.Nullable,
		Default:      c.Default,
		IsPrimaryKey: c.IsPrimaryKey,
		Comment:      c.Comment,
	}
}
