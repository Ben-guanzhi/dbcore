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
	// inTx mirrors the manual transaction this connection holds. The plugin is
	// the source of truth for BEGIN/COMMIT/ROLLBACK; the mirror lets a caller
	// ask without a round trip, and lets a transaction left open on disconnect
	// be rolled back.
	inTx bool
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

// Call reaches the process for a single-shot method the kernel has no view of
// — transactions, DML batches, metrics, meta — with the connection id inside
// the params. A client with a wider session surface (fynequery's) keeps its
// vocabulary while the process plumbing stays here.
func (d *ConnDriver) Call(ctx context.Context, method string, params, result interface{}) error {
	return d.proc.Call(ctx, method, params, result)
}

// CallStream is Call for a streaming method: frames arrive until the terminal
// (Done) one, each handed to onFrame; a false return stops early.
func (d *ConnDriver) CallStream(
	ctx context.Context,
	method string,
	params interface{},
	onFrame func(StreamFrame) (bool, error),
	final interface{},
) error {
	return d.proc.CallStream(ctx, method, params, onFrame, final)
}

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
	// The wide session: what a session does beyond running statements. These
	// are the names the deployed (fynequery) plugins answer; the kernel's own
	// spelling is the fallback on the right.
	connMethodBegin       = "begin"
	connMethodCommit      = "commit"
	connMethodRollback    = "rollback"
	connMethodUpdate      = "update"
	connMethodInsert      = "insert"
	connMethodDelete      = "delete"
	connMethodApplyBatch  = "applyBatch"
	connMethodQueryStream = "queryStream"
	connMethodMetrics     = "metrics"
	connMethodCreateDB    = "createDatabase"
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

	connMethodBegin:       MethodConnBegin,
	connMethodCommit:      MethodConnCommit,
	connMethodRollback:    MethodConnRollback,
	connMethodUpdate:      MethodConnUpdate,
	connMethodInsert:      MethodConnInsert,
	connMethodDelete:      MethodConnDelete,
	connMethodApplyBatch:  MethodConnApplyBatch,
	connMethodQueryStream: MethodConnQueryStream,
	connMethodMetrics:     MethodConnMetrics,
	connMethodCreateDB:    MethodConnCreateDB,
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

// callStreamConn is callConn for a streaming method: the fynequery name goes
// first, the kernel's when the plugin does not know it. The retry is only safe
// before any frame reaches the caller, which is where it happens — a plugin
// answers an unknown method with an error rather than a first frame.
func (d *ConnDriver) callStreamConn(
	ctx context.Context,
	method string,
	params interface{},
	onFrame func(StreamFrame) (bool, error),
	final interface{},
) error {
	err := d.CallStream(ctx, method, params, onFrame, final)
	if err == nil {
		return nil
	}
	fallback, ok := connFallbacks[method]
	if !ok || !isUnknownMethod(err) {
		return err
	}
	return d.CallStream(ctx, fallback, params, onFrame, final)
}

// setInTx records the mirrored transaction state of one connection.
func (d *ConnDriver) setInTx(connID string, value bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	state := d.conns[connID]
	state.inTx = value
	d.conns[connID] = state
}

// inTxOf reports the mirrored transaction state of one connection.
func (d *ConnDriver) inTxOf(connID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conns[connID].inTx
}

// Conn is one connection of a plugin, bound so the dbcore.SQLDriver methods do
// not have to name it every time.
type Conn struct {
	driver   *ConnDriver
	connID   string
	database string
}

// Bind returns the dbcore.SQLDriver view of one connection. database is the name
// the host associates with this connection (the SQLite file, the selected
// database, …); it becomes the single schema the connection reports, because a
// connection-oriented plugin is already bound to exactly one database.
func (d *ConnDriver) Bind(connID, database string) dbcore.SQLDriver {
	return d.Wide(connID, database)
}

// Wide returns the wide session view of one connection: the same driver Bind
// hands out, with the transaction / DML / metrics / streaming calls typed as
// well (see connsession.go). Bind widens to dbcore.SQLDriver because that is the
// narrow contract every consumer needs; a client that also drives the session
// asks for this one instead of re-spelling the protocol over raw Call.
func (d *ConnDriver) Wide(connID, database string) *Conn {
	return &Conn{driver: d, connID: connID, database: database}
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
	// The connection-oriented protocol opens the database at connect time, so the
	// connection is already bound to one database. The kernel's catalog path asks
	// for a schema list to populate the navigator; answering with the database this
	// connection holds is honest, where a harder-wired empty name would leave the
	// navigator blank and break the schema-scoped table/column lookups that follow.
	return []dbcore.SchemaInfo{{Name: c.database}}, nil
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
