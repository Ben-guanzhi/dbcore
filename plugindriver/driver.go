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
	"time"

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
	// mu guards pending and streams.
	mu      sync.Mutex
	pending map[int64]chan Response
	// streams maps a streaming request id to its channel; its frames arrive
	// one by one until the terminal (Done) one retires it.
	streams map[int64]chan Response
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
	return StartWithOpts(ctx, path, args, StartOpts{})
}

// StartWithOpts is Start with launch options: a stderr sink, a hidden window,
// and a banner timeout. A plugin that does not banner in time is killed — a
// silent executable must fail the launch, not hang it.
func StartWithOpts(ctx context.Context, path string, args []string, opts StartOpts) (*Driver, error) {
	cmd := exec.Command(path, args...)
	if opts.Stderr != nil {
		cmd.Stderr = opts.Stderr
	}
	if opts.HideWindow {
		hideWindow(cmd)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("plugindriver: stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("plugindriver: stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("plugindriver: start %s: %w", path, err)
	}

	d := &Driver{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReaderSize(stdout, 1<<20),
		pending: make(map[int64]chan Response),
		streams: make(map[int64]chan Response),
	}

	banner, err := d.readBanner(opts.bannerTimeout())
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
	return StartConnWithOpts(ctx, path, args, StartOpts{})
}

// StartConnWithOpts is StartConn with launch options (see StartOpts).
func StartConnWithOpts(ctx context.Context, path string, args []string, opts StartOpts) (*ConnDriver, error) {
	drv, err := StartWithOpts(ctx, path, args, opts)
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
// A plugin that never banners fails the launch after the timeout.
func (d *Driver) readBanner(timeout time.Duration) (Banner, error) {
	type read struct {
		line []byte
		err  error
	}
	ch := make(chan read, 1)
	go func() {
		line, err := d.stdout.ReadBytes('\n')
		ch <- read{line, err}
	}()
	var raw []byte
	select {
	case r := <-ch:
		if r.err != nil {
			return Banner{}, fmt.Errorf("plugindriver: read banner: %w", r.err)
		}
		raw = r.line
	case <-time.After(timeout):
		return Banner{}, fmt.Errorf("plugindriver: no banner after %s", timeout)
	}
	var banner Banner
	if err := json.Unmarshal(raw, &banner); err != nil {
		return Banner{}, fmt.Errorf("plugindriver: banner is not JSON (%q): %w", string(raw), err)
	}
	// The engine is named either way: the kernel's banner says "kind", the
	// fynequery one says "code". A plugin written for either client is accepted.
	kind := banner.Kind
	if kind == "" {
		kind = banner.Code
	}
	if kind == "" {
		return Banner{}, fmt.Errorf("plugindriver: the banner does not name an engine: %q", string(raw))
	}
	banner.Kind = kind
	banner.Code = kind
	return banner, nil
}

// readLoop dispatches replies to the call that is waiting for each id. A
// streaming reply arrives as several frames under one id; each is forwarded
// until the terminal (Done) frame retires it.
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
			if sc, ok := d.streams[resp.ID]; ok {
				// Streaming frame: keep the channel open until the terminal
				// (Done) frame, then retire it.
				if resp.Done {
					delete(d.streams, resp.ID)
				}
				d.mu.Unlock()
				sc <- resp
				continue
			}
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

// failAll fails every waiting call — single-shot and streaming alike: once the
// pipe is gone, nothing can answer them.
func (d *Driver) failAll(err error) {
	d.mu.Lock()
	d.readErr = err
	waiting := d.pending
	d.pending = make(map[int64]chan Response)
	streaming := d.streams
	d.streams = make(map[int64]chan Response)
	d.mu.Unlock()
	for _, ch := range waiting {
		ch <- Response{Error: err.Error()}
	}
	for _, ch := range streaming {
		ch <- Response{Error: err.Error(), Done: true}
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
	streaming := d.streams
	d.streams = make(map[int64]chan Response)
	d.mu.Unlock()
	for _, ch := range streaming {
		ch <- Response{Error: "plugindriver: shut down", Done: true}
	}
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
