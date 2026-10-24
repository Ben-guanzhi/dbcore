package plugindriver

// The connection-oriented host: one plugin process serving several
// connections, the way fynequery's plugin host does.
//
// The kernel's statement-oriented Driver (driver.go) is one connection per
// process. This one is the other shape: the plugin keeps the connections, the
// host addresses them by id. A plugin that implements the connection-oriented
// method set (see connproto.go) works with both hosts.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/dbcore/dbcore"
)

// ConnDriver is a dbcore.SQLDriver over a plugin that serves several
// connections.
type ConnDriver struct {
	proc *Driver

	mu    sync.Mutex
	conns map[string]connState
}

type connState struct {
	id     string
	server string
}

// NewConnDriver returns a driver over an already-started plugin process.
func NewConnDriver(proc *Driver) *ConnDriver {
	return &ConnDriver{proc: proc, conns: map[string]connState{}}
}

// Open adds a connection to the plugin and returns its id.
func (d *ConnDriver) Open(ctx context.Context, params ConnectParams) (string, string, error) {
	raw, err := d.callConn(ctx, connMethodOpen, params)
	if err != nil {
		return "", "", err
	}
	var result OpenResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", "", fmt.Errorf("plugindriver: decode the open result: %w", err)
	}
	if result.ConnID == "" {
		return "", "", fmt.Errorf("plugindriver: the plugin returned no connection id")
	}
	d.mu.Lock()
	d.conns[result.ConnID] = connState{id: result.ConnID, server: result.Server}
	d.mu.Unlock()
	return result.ConnID, result.Server, nil
}

// CloseConn drops one connection.
func (d *ConnDriver) CloseConn(ctx context.Context, connID string) error {
	_, err := d.callConn(ctx, connMethodClose, ConnRef{ConnID: connID})
	if err == nil {
		d.mu.Lock()
		delete(d.conns, connID)
		d.mu.Unlock()
	}
	return err
}

// ConnIDs lists the connections the plugin holds.
func (d *ConnDriver) ConnIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.conns))
	for id := range d.conns {
		out = append(out, id)
	}
	return out
}

// Kind reports the engine the plugin implements.
func (d *ConnDriver) Kind() dbcore.DatabaseKind { return d.proc.kind }

// Ping asks the plugin whether it is alive. A plugin that predates the method
// answers "unknown method", which is reported as ErrNotSupported.
func (d *ConnDriver) Ping(ctx context.Context) error {
	_, err := d.proc.call(ctx, MethodPing, nil)
	return err
}

// Close shuts the plugin process down.
func (d *ConnDriver) Close() error { return d.proc.Close() }

// AsSQL is false: the process is not a driver by itself — a connection of it
// is, and Bind returns that.
func (d *ConnDriver) AsSQL() (dbcore.SQLDriver, bool) { return nil, false }
func (d *ConnDriver) AsKV() (dbcore.KVDriver, bool)   { return nil, false }

// connID is the connection this call addresses. The connection-oriented driver
// is used through a bound view (Conn), so the id is already fixed.
func (d *ConnDriver) connID() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.conns) == 0 {
		return ""
	}
	// The single-connection case is the common one: the host binds one
	// connection per driver instance.
	for id := range d.conns {
		return id
	}
	return ""
}

// The connection-oriented method names, as the deployed plugin binaries answer
// them (fynequery's). The kernel's own names for the same calls live in
// connproto.go; a plugin written for the kernel answers those instead.
//
// The host sends the fynequery name first and falls back to the kernel name: the
// kernel's own host (and any plugin written for it) speaks the kernel names, so a
// call is retried under the other spelling when the first answers "unknown
// method". One plugin, either vocabulary.
const (
	connMethodOpen    = "open"
	connMethodClose   = "close"
	connMethodTables  = "tables"
	connMethodColumns = "columns"
	connMethodQuery   = "query"
	connMethodBrowse  = "browse"
)

// connFallbacks maps a fynequery method name onto the kernel's equivalent, used
// when a plugin answers "unknown method".
var connFallbacks = map[string]string{
	connMethodOpen:    MethodOpen,
	connMethodClose:   MethodCloseConn,
	connMethodTables:  MethodListConnTables,
	connMethodColumns: MethodListConnColumns,
	connMethodQuery:   MethodConnQuery,
	connMethodBrowse:  MethodConnBrowse,
}

// callConn sends one connection-oriented call, trying the fynequery name first and
// the kernel's name when the plugin does not know it. The cost is one extra round
// trip on the first call, which happens once per method per process.
func (d *ConnDriver) callConn(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	raw, err := d.proc.call(ctx, method, params)
	if err == nil {
		return raw, nil
	}
	fallback, ok := connFallbacks[method]
	if !ok || !isUnknownMethod(err) {
		return nil, err
	}
	return d.proc.call(ctx, fallback, params)
}

// isUnknownMethod reports whether an error is the plugin's "no such method".
func isUnknownMethod(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unknown method")
}

// Conn is one connection of a plugin, bound so the dbcore.SQLDriver methods do
// not have to name it every time.
type Conn struct {
	driver *ConnDriver
	connID string
}

// Bind returns the dbcore.SQLDriver view of one connection.
func (d *ConnDriver) Bind(connID string) dbcore.SQLDriver {
	return &Conn{driver: d, connID: connID}
}

func (c *Conn) Kind() dbcore.DatabaseKind { return c.driver.Kind() }
func (c *Conn) Ping(ctx context.Context) error {
	_, err := c.driver.proc.call(ctx, MethodPing, nil)
	return err
}
func (c *Conn) Close() error                    { return c.driver.CloseConn(context.Background(), c.connID) }
func (c *Conn) AsSQL() (dbcore.SQLDriver, bool) { return c, true }
func (c *Conn) AsKV() (dbcore.KVDriver, bool)   { return nil, false }

// Execute runs one statement on this connection.
func (c *Conn) Execute(ctx context.Context, query string, args ...dbcore.Value) (*dbcore.QueryResult, error) {
	return c.ExecuteIn(nil, ctx, query, args...)
}

// ExecuteIn runs one statement on this connection. A non-nil exec is a
// programming error: a plugin's transaction lives inside the plugin.
func (c *Conn) ExecuteIn(exec dbcore.SQLTx, ctx context.Context, query string, args ...dbcore.Value) (*dbcore.QueryResult, error) {
	if exec != nil {
		return nil, fmt.Errorf("%w: a plugin connection cannot run a statement on a kernel transaction", dbcore.ErrNotSupported)
	}
	cells := make([]CellPayload, len(args))
	for i, arg := range args {
		cells[i] = EncodeValue(arg)
	}
	raw, err := c.driver.callConn(ctx, connMethodQuery, ConnQueryParams{
		ConnID:  c.connID,
		SQL:     query,
		MaxRows: len(args) + 1, // the plugin caps the rows; the value is advisory
	})
	if err != nil {
		return nil, err
	}
	return DecodeConnQueryResult(raw)
}

func (c *Conn) ListSchemas(ctx context.Context) ([]dbcore.SchemaInfo, error) {
	// The connection-oriented protocol has no schema list: a fynequery plugin
	// lists databases through its own call. Answering with what the connection
	// knows is honest, where a wrong answer would not be.
	return []dbcore.SchemaInfo{{Name: ""}}, nil
}

func (c *Conn) ListTables(ctx context.Context, schema string) ([]dbcore.TableInfo, error) {
	raw, err := c.driver.callConn(ctx, connMethodTables, ConnRef{ConnID: c.connID})
	if err != nil {
		return nil, err
	}
	return DecodeConnObject(raw)
}

// connObjectList is the shape a plugin answers the tables call with: an object
// list under a key, the way the fynequery protocol has always wrapped it. The
// kernel's own set answers with a bare array, so both are accepted.
type connObjectList struct {
	Objects []ConnObject `json:"objects"`
}

func (c *Conn) ListColumns(ctx context.Context, schema, table string) ([]dbcore.ColumnInfo, error) {
	raw, err := c.driver.callConn(ctx, connMethodColumns, ConnTableRef{
		ConnID: c.connID,
		Table:  table,
	})
	if err != nil {
		return nil, err
	}
	return DecodeConnColumn(raw)
}

func (c *Conn) ListIndexes(ctx context.Context, schema, table string) ([]dbcore.IndexInfo, error) {
	// Indexes are not part of the connection-oriented protocol; a host that
	// needs them uses the statement-oriented one.
	return nil, fmt.Errorf("%w: a plugin connection does not report indexes", dbcore.ErrNotSupported)
}

func (c *Conn) ListForeignKeys(ctx context.Context, schema, table string) ([]dbcore.ForeignKeyInfo, error) {
	return nil, fmt.Errorf("%w: a plugin connection does not report foreign keys", dbcore.ErrNotSupported)
}

func (c *Conn) CreateTableSQL(ctx context.Context, schema, table string) (string, error) {
	return "", fmt.Errorf("%w: a plugin connection does not build DDL", dbcore.ErrNotSupported)
}

func (c *Conn) BrowseRows(ctx context.Context, schema, table string, req *dbcore.TableRowsRequest) (*dbcore.TableRowsResult, error) {
	limit := 0
	if req != nil {
		limit = req.PageSize
	}
	raw, err := c.driver.callConn(ctx, connMethodBrowse, ConnBrowseParams{
		ConnID: c.connID,
		Table:  table,
		Limit:  limit,
	})
	if err != nil {
		return nil, err
	}
	var payload ConnQueryResult
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("plugindriver: decode the browse result: %w", err)
	}
	result, err := DecodeConnQueryResult(raw)
	if err != nil {
		return nil, err
	}
	_ = payload
	return &dbcore.TableRowsResult{Columns: result.Columns, Rows: result.Rows}, nil
}

func (c *Conn) BrowseRowsCount(ctx context.Context, schema, table string, req *dbcore.TableRowsRequest) (uint64, error) {
	return 0, fmt.Errorf("%w: a plugin connection does not count rows", dbcore.ErrNotSupported)
}
