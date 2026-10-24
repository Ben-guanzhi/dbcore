package plugindriver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"

	"github.com/dbcore/dbcore"
)

// Driver is a dbcore.SQLDriver backed by a plugin process.
//
// One process serves one connection: a plugin holds the database handle, so sharing
// a process between connections would make every call carry a connection id and put
// the plugin's own state at risk. A process per connection is the cheap, obvious
// arrangement, and it is what makes a crash contain itself.
// proc is the plugin process: the pipes, the pending-request registry and the
// banner the plugin announced.
type Driver struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	// writeMu serializes frames: a Response must not be interleaved mid-line.
	writeMu sync.Mutex
	// pending maps a request id to the channel waiting for its reply.
	mu      sync.Mutex
	pending map[int64]chan Response
	nextID  atomic.Int64

	// kind is what the plugin's banner declared.
	kind dbcore.DatabaseKind
	// closed is set once Close ran, so a later call fails with a clear error
	// instead of writing to a dead pipe.
	closed atomic.Bool
	// readErr records why the reader stopped, so a hang becomes an error.
	readErr error
}

// Start launches a plugin and completes the handshake. A plugin that does not print
// a compatible banner is killed immediately: the alternative is a host that sends
// requests into a process speaking something else.
func Start(ctx context.Context, path string, args []string) (*Driver, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("plugindriver: stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("plugindriver: stdout: %w", err)
	}
	// The plugin's stderr stays the plugin's: it is diagnostic output, and the host
	// never parses it.
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("plugindriver: start %s: %w", path, err)
	}

	d := &Driver{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReaderSize(stdout, 1<<20),
		pending: make(map[int64]chan Response),
	}

	banner, err := d.readBanner()
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	if !acceptsProtocol(banner.Proto) {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("plugindriver: %s speaks %q, want %q", path, banner.Proto, ProtocolName)
	}
	if banner.Version != ProtocolVersion {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("plugindriver: protocol version %d, this host speaks %d", banner.Version, ProtocolVersion)
	}
	d.kind = dbcore.DatabaseKind(banner.Kind)

	go d.readLoop()
	return d, nil
}

// StartConn launches a plugin for the connection-oriented host: one process
// serving several connections.
func StartConn(ctx context.Context, path string, args []string) (*ConnDriver, error) {
	drv, err := Start(ctx, path, args)
	if err != nil {
		return nil, err
	}
	return NewConnDriver(drv), nil
}

// acceptsProtocol reports whether a banner names a protocol this host speaks.
//
// The kernel's own name is accepted, and so is the fynequery one the deployed
// plugin binaries announce: the protocol is shared, so its name is too, and a
// plugin built before the move must not be orphaned by it.
func acceptsProtocol(name string) bool {
	switch name {
	case ProtocolName, LegacyProtocolName:
		return true
	}
	return false
}

// readBanner reads the single line a plugin prints before the protocol starts.
func (d *Driver) readBanner() (Banner, error) {
	line, err := d.stdout.ReadBytes('\n')
	if err != nil {
		return Banner{}, fmt.Errorf("plugindriver: read banner: %w", err)
	}
	var banner Banner
	if err := json.Unmarshal(line, &banner); err != nil {
		return Banner{}, fmt.Errorf("plugindriver: banner is not JSON (%q): %w", string(line), err)
	}
	// The engine is named either way: the kernel's banner says "kind", the
	// fynequery one says "code". A plugin written for either client is accepted.
	kind := banner.Kind
	if kind == "" {
		kind = banner.Code
	}
	if kind == "" {
		return Banner{}, fmt.Errorf("plugindriver: the banner does not name an engine: %q", string(line))
	}
	banner.Kind = kind
	banner.Code = kind
	return banner, nil
}

// readLoop dispatches replies to the call that is waiting for each id.
func (d *Driver) readLoop() {
	for {
		line, err := d.stdout.ReadBytes('\n')
		if len(line) > 0 {
			var resp Response
			if jsonErr := json.Unmarshal(line, &resp); jsonErr != nil {
				d.failAll(fmt.Errorf("plugindriver: bad reply (%q): %w", string(line), jsonErr))
				return
			}
			d.mu.Lock()
			ch, ok := d.pending[resp.ID]
			delete(d.pending, resp.ID)
			d.mu.Unlock()
			if ok {
				// Buffered: the sender must never block on a host that moved on.
				ch <- resp
			}
		}
		if err != nil {
			if err != io.EOF {
				d.failAll(fmt.Errorf("plugindriver: read: %w", err))
			} else {
				d.failAll(fmt.Errorf("plugindriver: the plugin exited"))
			}
			return
		}
	}
}

// failAll fails every waiting call: once the pipe is gone, nothing can answer them.
func (d *Driver) failAll(err error) {
	d.mu.Lock()
	d.readErr = err
	waiting := d.pending
	d.pending = make(map[int64]chan Response)
	d.mu.Unlock()
	for _, ch := range waiting {
		ch <- Response{Error: err.Error()}
	}
}

// call sends one request and waits for its reply.
func (d *Driver) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	if d.closed.Load() {
		return nil, fmt.Errorf("plugindriver: driver is closed")
	}
	id := d.nextID.Add(1)
	var raw json.RawMessage
	if params != nil {
		encoded, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("plugindriver: encode %s: %w", method, err)
		}
		raw = encoded
	}
	frame, err := json.Marshal(Request{ID: id, Method: method, Params: raw})
	if err != nil {
		return nil, fmt.Errorf("plugindriver: encode request: %w", err)
	}
	ch := make(chan Response, 1)
	d.mu.Lock()
	d.pending[id] = ch
	d.mu.Unlock()

	d.writeMu.Lock()
	_, writeErr := d.stdin.Write(append(frame, '\n'))
	d.writeMu.Unlock()
	if writeErr != nil {
		d.mu.Lock()
		delete(d.pending, id)
		d.mu.Unlock()
		return nil, fmt.Errorf("plugindriver: write %s: %w", method, writeErr)
	}

	select {
	case <-ctx.Done():
		d.mu.Lock()
		delete(d.pending, id)
		d.mu.Unlock()
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.Error != "" {
			return nil, pluginError(resp)
		}
		return resp.Result, nil
	}
}

// pluginError maps the plugin's error onto the host's sentinels so a caller can test
// with errors.Is instead of matching strings.
func pluginError(resp Response) error {
	if resp.Code == dbcore.ErrCodeNotSupported {
		return fmt.Errorf("%w: %s", dbcore.ErrNotSupported, resp.Error)
	}
	return fmt.Errorf("%s", resp.Error)
}

// ---------- dbcore.Driver ----------

func (d *Driver) Kind() dbcore.DatabaseKind { return d.kind }

func (d *Driver) Ping(ctx context.Context) error {
	_, err := d.call(ctx, MethodPing, nil)
	return err
}

// Close shuts the plugin down. It is idempotent and never blocks on a dead child.
func (d *Driver) Close() error {
	if d.closed.Swap(true) {
		return nil
	}
	// Ask politely first: a plugin may want to close its handle.
	_, _ = d.call(context.Background(), MethodClose, nil)
	_ = d.stdin.Close()
	d.mu.Lock()
	d.pending = make(map[int64]chan Response)
	d.mu.Unlock()
	if d.cmd.Process != nil {
		_ = d.cmd.Process.Kill()
	}
	if err := d.cmd.Wait(); err != nil {
		// A killed child exits non-zero; that is the expected path here.
		var exitErr *exec.ExitError
		if !isExitError(err, &exitErr) {
			return fmt.Errorf("plugindriver: wait: %w", err)
		}
	}
	return nil
}

func (d *Driver) AsSQL() (dbcore.SQLDriver, bool) { return d, true }
func (d *Driver) AsKV() (dbcore.KVDriver, bool)   { return nil, false }

// ---------- dbcore.SQLDriver ----------

func (d *Driver) Execute(ctx context.Context, query string, args ...dbcore.Value) (*dbcore.QueryResult, error) {
	return d.ExecuteIn(nil, ctx, query, args...)
}

// ExecuteIn runs a statement. The plugin has no transaction handle to hand out, so
// SQLTx is carried as the plugin's own session: passing a non-nil exec is a
// programming error, and the host reports it rather than silently running outside
// the transaction the caller believes it is in.
func (d *Driver) ExecuteIn(exec dbcore.SQLTx, ctx context.Context, query string, args ...dbcore.Value) (*dbcore.QueryResult, error) {
	if exec != nil {
		return nil, fmt.Errorf("%w: a plugin driver cannot run a statement on a kernel transaction; use the plugin's own begin/commit", dbcore.ErrNotSupported)
	}
	cells := make([]CellPayload, len(args))
	for i, arg := range args {
		cells[i] = EncodeValue(arg)
	}
	raw, err := d.call(ctx, MethodExecute, map[string]interface{}{"sql": query, "args": cells})
	if err != nil {
		return nil, err
	}
	var payload ResultPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("plugindriver: decode result: %w", err)
	}
	return decodeResult(payload)
}

func decodeResult(payload ResultPayload) (*dbcore.QueryResult, error) {
	out := &dbcore.QueryResult{
		AffectedRows: payload.AffectedRows,
		LastInsertID: payload.LastInsertID,
	}
	out.Columns = make([]dbcore.ColumnInfo, len(payload.Columns))
	for i, col := range payload.Columns {
		out.Columns[i] = decodeColumn(col)
	}
	out.Rows = make([][]dbcore.Value, len(payload.Rows))
	for i, row := range payload.Rows {
		cells := make([]dbcore.Value, len(row))
		for j, cell := range row {
			v, err := DecodeValue(cell)
			if err != nil {
				return nil, err
			}
			cells[j] = v
		}
		out.Rows[i] = cells
	}
	return out, nil
}

func (d *Driver) ListSchemas(ctx context.Context) ([]dbcore.SchemaInfo, error) {
	raw, err := d.call(ctx, MethodListSchemas, nil)
	if err != nil {
		return nil, err
	}
	var names []string
	if err := json.Unmarshal(raw, &names); err != nil {
		return nil, fmt.Errorf("plugindriver: decode schemas: %w", err)
	}
	out := make([]dbcore.SchemaInfo, len(names))
	for i, name := range names {
		out[i] = dbcore.SchemaInfo{Name: name}
	}
	return out, nil
}

func (d *Driver) ListTables(ctx context.Context, schema string) ([]dbcore.TableInfo, error) {
	raw, err := d.call(ctx, MethodListTables, SchemaArgs{Schema: schema})
	if err != nil {
		return nil, err
	}
	var payload []TablePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("plugindriver: decode tables: %w", err)
	}
	out := make([]dbcore.TableInfo, len(payload))
	for i, t := range payload {
		out[i] = dbcore.TableInfo{Schema: t.Schema, Name: t.Name, Type: t.Type, Comment: t.Comment, Rows: t.Rows, ApproxRows: t.ApproxRows}
	}
	return out, nil
}

func (d *Driver) ListColumns(ctx context.Context, schema, table string) ([]dbcore.ColumnInfo, error) {
	raw, err := d.call(ctx, MethodListColumns, SchemaArgs{Schema: schema, Table: table})
	if err != nil {
		return nil, err
	}
	var payload []ColumnPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("plugindriver: decode columns: %w", err)
	}
	out := make([]dbcore.ColumnInfo, len(payload))
	for i, c := range payload {
		out[i] = decodeColumn(c)
	}
	return out, nil
}

func (d *Driver) ListIndexes(ctx context.Context, schema, table string) ([]dbcore.IndexInfo, error) {
	raw, err := d.call(ctx, MethodListIndexes, SchemaArgs{Schema: schema, Table: table})
	if err != nil {
		return nil, err
	}
	var out []dbcore.IndexInfo
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("plugindriver: decode indexes: %w", err)
	}
	return out, nil
}

func (d *Driver) ListForeignKeys(ctx context.Context, schema, table string) ([]dbcore.ForeignKeyInfo, error) {
	raw, err := d.call(ctx, MethodListForeign, SchemaArgs{Schema: schema, Table: table})
	if err != nil {
		return nil, err
	}
	var out []dbcore.ForeignKeyInfo
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("plugindriver: decode foreign keys: %w", err)
	}
	return out, nil
}

func (d *Driver) CreateTableSQL(ctx context.Context, schema, table string) (string, error) {
	raw, err := d.call(ctx, MethodCreateTableDB, SchemaArgs{Schema: schema, Table: table})
	if err != nil {
		return "", err
	}
	var ddl string
	if err := json.Unmarshal(raw, &ddl); err != nil {
		return "", fmt.Errorf("plugindriver: decode DDL: %w", err)
	}
	return ddl, nil
}

func (d *Driver) BrowseRows(ctx context.Context, schema, table string, req *dbcore.TableRowsRequest) (*dbcore.TableRowsResult, error) {
	raw, err := d.call(ctx, MethodBrowseRows, browseArgs(schema, table, req))
	if err != nil {
		return nil, err
	}
	var payload BrowseResult
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("plugindriver: decode browse: %w", err)
	}
	out := &dbcore.TableRowsResult{Total: payload.Total}
	out.Columns = make([]dbcore.ColumnInfo, len(payload.Columns))
	for i, c := range payload.Columns {
		out.Columns[i] = decodeColumn(c)
	}
	out.Rows = make([][]dbcore.Value, len(payload.Rows))
	for i, row := range payload.Rows {
		cells := make([]dbcore.Value, len(row))
		for j, cell := range row {
			v, err := DecodeValue(cell)
			if err != nil {
				return nil, err
			}
			cells[j] = v
		}
		out.Rows[i] = cells
	}
	return out, nil
}

func (d *Driver) BrowseRowsCount(ctx context.Context, schema, table string, req *dbcore.TableRowsRequest) (uint64, error) {
	raw, err := d.call(ctx, MethodBrowseCount, browseArgs(schema, table, req))
	if err != nil {
		return 0, err
	}
	var count uint64
	if err := json.Unmarshal(raw, &count); err != nil {
		return 0, fmt.Errorf("plugindriver: decode count: %w", err)
	}
	return count, nil
}

// browseArgs converts the kernel request into the wire form.
func browseArgs(schema, table string, req *dbcore.TableRowsRequest) BrowseArgs {
	out := BrowseArgs{Schema: schema, Table: table}
	if req == nil {
		return out
	}
	out.Page = req.Page
	out.PageSize = req.PageSize
	for _, key := range req.Sort {
		out.Sort = append(out.Sort, SortArg{Column: key.Column, Desc: key.Desc})
	}
	for _, filter := range req.Filter {
		out.Filter = append(out.Filter, FilterArg{
			Column: filter.Column,
			Op:     string(filter.Op),
			Value:  EncodeValue(filter.Value),
		})
	}
	return out
}

// isExitError reports whether err is an *exec.ExitError; kept as a helper so Close
// does not import errors just for one check.
func isExitError(err error, target **exec.ExitError) bool {
	if exit, ok := err.(*exec.ExitError); ok {
		*target = exit
		return true
	}
	return false
}
