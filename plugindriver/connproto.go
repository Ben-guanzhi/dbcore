package plugindriver

// The connection-oriented part of the plugin protocol, shared by the clients that
// host plugins (fynequery and TableProGo).
//
// The kernel protocol of proto.go is statement-oriented: one host, one
// connection, one statement per call. fynequery's plugin protocol was
// connection-oriented: one plugin process served several connections at once,
// identified by a conn id.
//
// Rather than keep two wire formats, the connection-oriented calls are expressed
// here as a *superset* over the same transport: a plugin that implements the
// kernel protocol answers the calls below by keeping the connections itself.
//
// A host that speaks only the kernel protocol never sends these calls, so a
// plugin written for one works with the other host unchanged.

import (
	"encoding/json"
	"fmt"

	"github.com/dbcore/dbcore"
)

// Connection-oriented method names. They are additive: a plugin that predates one
// answers ERR_NOT_SUPPORTED, and the host degrades to the capability it has.
const (
	// MethodOpenMeta asks the plugin to identify itself.
	MethodOpenMeta = "meta"
	// MethodOpen opens a connection and returns its id.
	MethodOpen = "open"
	// MethodCloseConn drops one connection.
	MethodCloseConn = "closeConn"
	// MethodListConnTables lists the objects of one connection.
	MethodListConnTables = "connTables"
	// MethodListConnColumns lists the columns of one object.
	MethodListConnColumns = "connColumns"
	// MethodConnQuery runs one statement on one connection.
	MethodConnQuery = "connQuery"
	// MethodConnBrowse reads the first rows of one object.
	MethodConnBrowse = "connBrowse"
	// MethodConnCreateDB creates a database or schema.
	MethodConnCreateDB = "connCreateDB"
	// MethodConnUpdate changes one row.
	MethodConnUpdate = "connUpdate"
	// MethodConnInsert adds one row.
	MethodConnInsert = "connInsert"
	// MethodConnDelete removes one row.
	MethodConnDelete = "connDelete"
	// MethodConnApplyBatch applies a whole commit in one transaction.
	MethodConnApplyBatch = "connApplyBatch"
	// MethodConnBegin starts a transaction.
	MethodConnBegin = "connBegin"
	// MethodConnCommit commits it.
	MethodConnCommit = "connCommit"
	// MethodConnRollback discards it.
	MethodConnRollback = "connRollback"
	// MethodConnQueryStream streams a statement's rows in batches.
	MethodConnQueryStream = "connQueryStream"
	// MethodConnMetrics reads a connection's server metrics.
	MethodConnMetrics = "connMetrics"
)

// ConnectParams opens a connection on a plugin.
type ConnectParams struct {
	Name     string            `json:"name"`
	Host     string            `json:"host"`
	Port     string            `json:"port"`
	User     string            `json:"user"`
	Password string            `json:"password"`
	Database string            `json:"database"`
	Options  map[string]string `json:"options,omitempty"`
}

// OpenResult answers MethodOpen.
type OpenResult struct {
	ConnID string `json:"connId"`
	// Server is the server's identification string, shown in the status bar.
	Server string `json:"server,omitempty"`
}

// ConnRef identifies one connection on a plugin.
type ConnRef struct {
	ConnID string `json:"connId"`
}

// ConnTableRef names one object of one connection.
type ConnTableRef struct {
	ConnID string `json:"connId"`
	Table  string `json:"table"`
}

// ConnObject is one object of a connection's catalog.
type ConnObject struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"` // "table" | "view"
	Rows       int64  `json:"rows"`
	ApproxRows bool   `json:"approxRows"`
}

// ConnColumn is one column of an object.
type ConnColumn struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
	// Key is "PK" when the column is part of the primary key.
	Key string `json:"key,omitempty"`
}

// ConnQueryParams runs one statement on one connection.
type ConnQueryParams struct {
	ConnID  string `json:"connId"`
	SQL     string `json:"sql"`
	MaxRows int    `json:"maxRows"`
}

// ConnQueryResult answers a connection-oriented query.
//
// The cells are read leniently: the kernel's tagged form (see CellPayload) and
// the plain strings the fynequery protocol uses are both accepted, so a plugin
// written for either client answers the same host.
type ConnQueryResult struct {
	Columns      []ConnColumn `json:"columns"`
	Rows         [][]any      `json:"rows"`
	RowsAffected int64        `json:"rowsAffected,omitempty"`
	DurationMS   int64        `json:"durationMs"`
}

// ConnBrowseParams reads the first rows of one object.
type ConnBrowseParams struct {
	ConnID string `json:"connId"`
	Table  string `json:"table"`
	Limit  int    `json:"limit"`
}

// ConnCreateDBParams creates a database or schema.
type ConnCreateDBParams struct {
	ConnID    string `json:"connId"`
	Name      string `json:"name"`
	Charset   string `json:"charset,omitempty"`
	Collation string `json:"collation,omitempty"`
}

// ConnDMLRow pairs a primary key with the changed columns of one update.
type ConnDMLRow struct {
	PK      map[string]CellPayload `json:"pk"`
	Changes map[string]CellPayload `json:"changes"`
}

// ConnDMLParams carries one update, insert or delete.
type ConnDMLParams struct {
	ConnID  string                 `json:"connId"`
	Table   string                 `json:"table"`
	PK      map[string]CellPayload `json:"pk,omitempty"`
	Changes map[string]CellPayload `json:"changes,omitempty"`
	Values  map[string]CellPayload `json:"values,omitempty"`
}

// ConnDMLBatchParams carries a whole commit: row edits, deletions, inserts and
// optional structural statements, applied atomically.
type ConnDMLBatchParams struct {
	ConnID  string                   `json:"connId"`
	Table   string                   `json:"table"`
	Updates []ConnDMLRow             `json:"updates,omitempty"`
	Deletes []map[string]CellPayload `json:"deletes,omitempty"`
	Inserts []map[string]CellPayload `json:"inserts,omitempty"`
	DDL     []string                 `json:"ddl,omitempty"`
}

// ConnDMLResult reports how many rows a change touched.
type ConnDMLResult struct {
	RowsAffected int64 `json:"rowsAffected"`
}

// ConnMetric is one labelled server metric.
type ConnMetric struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Group string `json:"group,omitempty"`
}

// ConnMetricsResult answers MethodConnMetrics.
type ConnMetricsResult struct {
	Metrics []ConnMetric `json:"metrics"`
}

// ConnStreamParams streams a statement's rows in batches.
type ConnStreamParams struct {
	ConnID string `json:"connId"`
	SQL    string `json:"sql"`
	// Batch is the maximum rows per frame (0 → the plugin's default).
	Batch int `json:"batch,omitempty"`
}

// ConnStreamFrame is one frame of a streaming reply. The first frame carries the
// column metadata; the terminal frame sets Done and the aggregate counters.
type ConnStreamFrame struct {
	Columns      []ConnColumn    `json:"columns,omitempty"`
	Rows         [][]CellPayload `json:"rows,omitempty"`
	RowsAffected int64           `json:"rowsAffected,omitempty"`
	DurationMS   int64           `json:"durationMs"`
}

// DecodeConnQueryResult turns a plugin's answer into the kernel's result.
//
// The cells are read in whichever form the plugin sent: the kernel's tagged
// cells, or the plain strings (nullable) the fynequery protocol uses. A plain
// string cell is a text cell; a null stays a null.
func DecodeConnQueryResult(raw json.RawMessage) (*dbcore.QueryResult, error) {
	var payload ConnQueryResult
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("plugindriver: decode the result: %w", err)
	}
	out := &dbcore.QueryResult{AffectedRows: payload.RowsAffected}
	for _, col := range payload.Columns {
		out.Columns = append(out.Columns, dbcore.ColumnInfo{
			Name:         col.Name,
			Type:         col.Type,
			Nullable:     col.Nullable,
			IsPrimaryKey: col.Key == "PK",
		})
	}
	for _, row := range payload.Rows {
		cells := make([]dbcore.Value, len(row))
		for i, cell := range row {
			v, err := decodeLenientCell(cell)
			if err != nil {
				return nil, err
			}
			cells[i] = v
		}
		out.Rows = append(out.Rows, cells)
	}
	return out, nil
}

// decodeLenientCell reads one cell in whichever form the plugin sent it: the
// kernel's tagged cell, a plain string, or a null.
func decodeLenientCell(cell any) (dbcore.Value, error) {
	switch value := cell.(type) {
	case nil:
		return dbcore.Value{Kind: dbcore.ValNull}, nil
	case string:
		return dbcore.Value{Kind: dbcore.ValText, S: value}, nil
	case bool:
		return dbcore.Value{Kind: dbcore.ValBool, Bool: value}, nil
	case float64:
		// JSON has one number type: an integral value is an integer, because a
		// count that came back as 1.0 is a count.
		if value == float64(int64(value)) {
			return dbcore.Value{Kind: dbcore.ValInt, I: int64(value)}, nil
		}
		return dbcore.Value{Kind: dbcore.ValFloat, F: value}, nil
	case map[string]any:
		raw, err := json.Marshal(value)
		if err != nil {
			return dbcore.Value{}, fmt.Errorf("plugindriver: decode the cell: %w", err)
		}
		var payload CellPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			return dbcore.Value{}, fmt.Errorf("plugindriver: decode the cell: %w", err)
		}
		return DecodeValue(payload)
	default:
		return dbcore.Value{}, fmt.Errorf("plugindriver: unknown cell of type %T", cell)
	}
}

// DecodeConnObject turns a plugin's object list into the kernel's.
//
// Two shapes are accepted: a bare array (the kernel's own set) and an object
// list under an "objects" key (the fynequery one). A plugin is a plugin whichever
// host launched it.
func DecodeConnObject(raw json.RawMessage) ([]dbcore.TableInfo, error) {
	var payload []ConnObject
	if err := json.Unmarshal(raw, &payload); err != nil {
		var wrapped struct {
			Objects []ConnObject `json:"objects"`
		}
		if wrapErr := json.Unmarshal(raw, &wrapped); wrapErr != nil {
			return nil, fmt.Errorf("plugindriver: decode the objects: %w", err)
		}
		payload = wrapped.Objects
	}
	out := make([]dbcore.TableInfo, 0, len(payload))
	for _, obj := range payload {
		out = append(out, dbcore.TableInfo{
			Name:       obj.Name,
			Type:       obj.Kind,
			Rows:       obj.Rows,
			ApproxRows: obj.ApproxRows,
		})
	}
	return out, nil
}

// DecodeConnColumn turns a plugin's column list into the kernel's. As with the
// object list, both the bare array and the wrapped form are accepted.
func DecodeConnColumn(raw json.RawMessage) ([]dbcore.ColumnInfo, error) {
	var payload []ConnColumn
	if err := json.Unmarshal(raw, &payload); err != nil {
		var wrapped struct {
			Columns []ConnColumn `json:"columns"`
		}
		if wrapErr := json.Unmarshal(raw, &wrapped); wrapErr != nil {
			return nil, fmt.Errorf("plugindriver: decode the columns: %w", err)
		}
		payload = wrapped.Columns
	}
	out := make([]dbcore.ColumnInfo, 0, len(payload))
	for _, col := range payload {
		out = append(out, dbcore.ColumnInfo{
			Name:         col.Name,
			Type:         col.Type,
			Nullable:     col.Nullable,
			IsPrimaryKey: col.Key == "PK",
		})
	}
	return out, nil
}
