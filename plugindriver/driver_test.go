package plugindriver_test

// End-to-end test of the plugin protocol: a real child process, built by the test
// itself, speaking the wire format over a pipe. The unit tests cover the payload
// conversions; only this shows that a plugin can actually be launched, handshaked
// with, driven and shut down.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/plugindriver"
)

// fakePluginSource is a minimal plugin: it declares itself "sqlite" and answers the
// handful of methods the test calls. Written to disk and built, so the test exercises
// the same exec path a real plugin takes.
const fakePluginSource = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/plugindriver"
)

func main() {
	out := bufio.NewWriter(os.Stdout)
	// Banner first, as the protocol requires.
	banner, _ := json.Marshal(plugindriver.Banner{
		Proto:   plugindriver.ProtocolName,
		Version: plugindriver.ProtocolVersion,
		Kind:    "sqlite",
		Name:    "Fake SQLite",
	})
	fmt.Fprintf(out, "%s\n", banner)
	out.Flush()

	in := bufio.NewReader(os.Stdin)
	for {
		line, err := in.ReadBytes('\n')
		if len(line) > 0 {
			var req plugindriver.Request
			if json.Unmarshal(line, &req) == nil {
				reply(out, req)
			}
		}
		if err != nil {
			return
		}
	}
}

func reply(out *bufio.Writer, req plugindriver.Request) {
	send := func(result interface{}, errText, code string) {
		payload, _ := json.Marshal(result)
		resp := plugindriver.Response{ID: req.ID, Result: payload, Error: errText, Code: code}
		frame, _ := json.Marshal(resp)
		fmt.Fprintf(out, "%s\n", frame)
		out.Flush()
	}

	switch req.Method {
	case plugindriver.MethodPing:
		send("pong", "", "")
	case plugindriver.MethodClose:
		send(nil, "", "")
		os.Exit(0)
	case plugindriver.MethodListSchemas:
		send([]string{"main"}, "", "")
	case plugindriver.MethodListTables:
		send([]plugindriver.TablePayload{{Schema: "main", Name: "parts", Type: "table"}}, "", "")
	case plugindriver.MethodListColumns:
		def := "42"
		send([]plugindriver.ColumnPayload{
			{Name: "id", Type: "INTEGER", IsPrimaryKey: true},
			{Name: "name", Type: "TEXT", Nullable: true},
			{Name: "qty", Type: "INTEGER", Default: &def},
		}, "", "")
	case plugindriver.MethodListIndexes:
		send([]dbcore.IndexInfo{{Name: "idx_parts_name", Kind: "index", Columns: []string{"name"}}}, "", "")
	case plugindriver.MethodListForeign:
		send([]dbcore.ForeignKeyInfo{}, "", "")
	case plugindriver.MethodCreateTableDB:
		send("CREATE TABLE parts (id INTEGER PRIMARY KEY)", "", "")
	case plugindriver.MethodExecute:
		// Echo the argument kinds back so the test can prove cells survived.
		var args struct {
			SQL  string                    ` + "`json:\"sql\"`" + `
			Args []plugindriver.CellPayload ` + "`json:\"args\"`" + `
		}
		json.Unmarshal(req.Params, &args)
		if len(args.Args) > 0 && args.Args[0].Kind == "text" && args.Args[0].Text == "unsupported" {
			send(nil, "the plugin cannot do that", dbcore.ErrCodeNotSupported)
			return
		}
		send(plugindriver.ResultPayload{
			Columns: []plugindriver.ColumnPayload{
				{Name: "id", Type: "INTEGER"}, {Name: "name", Type: "TEXT"},
				{Name: "ratio", Type: "REAL"}, {Name: "raw", Type: "BLOB"},
				{Name: "flag", Type: "BOOL"}, {Name: "at", Type: "TIMESTAMP"},
				{Name: "missing", Type: "TEXT"},
			},
			Rows: [][]plugindriver.CellPayload{{
				{Kind: "int", Int: 9007199254740993},
				{Kind: "text", Text: args.SQL},
				{Kind: "float", Flt: 1.5},
				{Kind: "blob", Blob: []byte{1, 2, 3}},
				{Kind: "bool", Bool: true},
				{Kind: "time", Time: "2026-10-24T12:00:00.123456789Z"},
				{Kind: "null"},
			}},
			AffectedRows: 1,
		}, "", "")
	case plugindriver.MethodBrowseRows:
		send(plugindriver.BrowseResult{
			Columns: []plugindriver.ColumnPayload{{Name: "id", Type: "INTEGER"}},
			Rows:    [][]plugindriver.CellPayload{{{Kind: "int", Int: 1}}},
			Total:   7,
		}, "", "")
	case plugindriver.MethodBrowseCount:
		send(uint64(7), "", "")
	case "exit":
		// The crash path: answer first, then die — the host must observe the
		// corpse instead of claiming the process is alive.
		send(nil, "", "")
		os.Exit(3)
	case "stream":
		// Three frames: columns first, rows next, counters in the terminal
		// (Done) one — the shape Driver.CallStream walks.
		frame := func(f plugindriver.StreamFrame, done bool) {
			payload, _ := json.Marshal(f)
			resp := plugindriver.Response{ID: req.ID, Result: payload, Done: done}
			raw, _ := json.Marshal(resp)
			fmt.Fprintf(out, "%s\n", raw)
			out.Flush()
		}
		frame(plugindriver.StreamFrame{Columns: []plugindriver.ColumnPayload{{Name: "n", Type: "INTEGER"}}}, false)
		frame(plugindriver.StreamFrame{Rows: [][]plugindriver.CellPayload{{{Kind: "int", Int: 1}}, {{Kind: "int", Int: 2}}}}, false)
		frame(plugindriver.StreamFrame{Rows: [][]plugindriver.CellPayload{{{Kind: "int", Int: 3}}}}, false)
		// The terminal frame carries the aggregate counters only; its rows
		// field stays empty by convention.
		frame(plugindriver.StreamFrame{RowsAffected: 3, DurationMS: 1}, true)
	default:
		send(nil, "unknown method "+req.Method, dbcore.ErrCodeNotSupported)
	}
}
`

// buildFakePlugin writes the plugin source into a temp module and builds it.
func buildFakePlugin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(fakePluginSource), 0o644); err != nil {
		t.Fatalf("write plugin: %v", err)
	}
	// The plugin imports this module, so a go.mod with a replace makes it buildable
	// from anywhere.
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	goMod := "module fakeplugin\n\ngo 1.21\n\nrequire github.com/dbcore/dbcore v0.0.0\n\nreplace github.com/dbcore/dbcore => " + filepath.ToSlash(repoRoot) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	// go.sum entries come from the replaced module, so none are needed.
	binary := filepath.Join(dir, "fakeplugin")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build plugin: %v\n%s", err, out)
	}
	return binary
}

func TestPluginDriverRoundTrip(t *testing.T) {
	binary := buildFakePlugin(t)
	ctx := context.Background()

	drv, err := plugindriver.Start(ctx, binary, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer drv.Close()

	if drv.Kind() != dbcore.KindSQLite {
		t.Fatalf("kind = %q", drv.Kind())
	}
	if err := drv.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	schemas, err := drv.ListSchemas(ctx)
	if err != nil {
		t.Fatalf("schemas: %v", err)
	}
	if len(schemas) != 1 || schemas[0].Name != "main" {
		t.Fatalf("schemas = %+v", schemas)
	}

	tables, err := drv.ListTables(ctx, "main")
	if err != nil {
		t.Fatalf("tables: %v", err)
	}
	if len(tables) != 1 || tables[0].Name != "parts" || tables[0].Type != "table" {
		t.Fatalf("tables = %+v", tables)
	}

	columns, err := drv.ListColumns(ctx, "main", "parts")
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	if len(columns) != 3 {
		t.Fatalf("columns = %+v", columns)
	}
	if !columns[0].IsPrimaryKey {
		t.Error("the primary key flag was lost")
	}
	if columns[1].Nullable != true {
		t.Error("nullability was lost")
	}
	// A pointer default keeps "no default" and "default is 42" distinct.
	if columns[2].Default == nil || *columns[2].Default != "42" {
		t.Errorf("default = %v", columns[2].Default)
	}
	if columns[0].Default != nil {
		t.Errorf("a column with no default got one: %v", *columns[0].Default)
	}

	indexes, err := drv.ListIndexes(ctx, "main", "parts")
	if err != nil {
		t.Fatalf("indexes: %v", err)
	}
	if len(indexes) != 1 || indexes[0].Name != "idx_parts_name" || len(indexes[0].Columns) != 1 {
		t.Fatalf("indexes = %+v", indexes)
	}

	ddl, err := drv.CreateTableSQL(ctx, "main", "parts")
	if err != nil {
		t.Fatalf("ddl: %v", err)
	}
	if !strings.Contains(ddl, "CREATE TABLE parts") {
		t.Fatalf("ddl = %q", ddl)
	}

	browse, err := drv.BrowseRows(ctx, "main", "parts", &dbcore.TableRowsRequest{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if browse.Total != 7 || len(browse.Rows) != 1 {
		t.Fatalf("browse = %+v", browse)
	}
	count, err := drv.BrowseRowsCount(ctx, "main", "parts", nil)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 7 {
		t.Fatalf("count = %d", count)
	}
}

// Every cell kind must survive the pipe unchanged; a big integer that came back as a
// float would be a silent data corruption.
func TestPluginDriverKeepsCellKinds(t *testing.T) {
	binary := buildFakePlugin(t)
	drv, err := plugindriver.Start(context.Background(), binary, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer drv.Close()

	result, err := drv.Execute(context.Background(), "SELECT marker", dbcore.Value{Kind: dbcore.ValText, S: "hi"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("rows = %+v", result.Rows)
	}
	row := result.Rows[0]
	if row[0].Kind != dbcore.ValInt || row[0].I != 9007199254740993 {
		t.Fatalf("the big integer was corrupted: %+v", row[0])
	}
	if row[1].Kind != dbcore.ValText || row[1].S != "SELECT marker" {
		t.Fatalf("argument did not reach the plugin: %+v", row[1])
	}
	if row[2].Kind != dbcore.ValFloat || row[2].F != 1.5 {
		t.Fatalf("float: %+v", row[2])
	}
	if row[3].Kind != dbcore.ValBlob || len(row[3].B) != 3 {
		t.Fatalf("blob: %+v", row[3])
	}
	if row[4].Kind != dbcore.ValBool || !row[4].Bool {
		t.Fatalf("bool: %+v", row[4])
	}
	if row[5].Kind != dbcore.ValTime || row[5].T.Nanosecond() != 123456789 {
		t.Fatalf("time lost its nanoseconds: %+v", row[5])
	}
	if row[6].Kind != dbcore.ValNull {
		t.Fatalf("null: %+v", row[6])
	}
}

// A plugin's ERR_NOT_SUPPORTED maps onto the kernel sentinel, so callers can test
// with errors.Is rather than matching text.
func TestPluginDriverMapsNotSupported(t *testing.T) {
	binary := buildFakePlugin(t)
	drv, err := plugindriver.Start(context.Background(), binary, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer drv.Close()

	_, err = drv.Execute(context.Background(), "anything", dbcore.Value{Kind: dbcore.ValText, S: "unsupported"})
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), dbcore.ErrCodeNotSupported) {
		t.Fatalf("err = %v", err)
	}
}

// A plugin that prints something other than a banner must be refused at startup, not
// half-way through a query.
func TestPluginDriverRejectsABadBanner(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "bad.sh")
	if runtime.GOOS == "windows" {
		// A .bat with the same effect: print a non-banner line and exit.
		script = filepath.Join(dir, "bad.bat")
		if err := os.WriteFile(script, []byte("@echo not-a-banner\r\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	} else {
		if err := os.WriteFile(script, []byte("#!/bin/sh\necho not-a-banner\n"), 0o755); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	_, err := plugindriver.Start(context.Background(), script, nil)
	if err == nil {
		t.Fatal("a process that is not a plugin must be refused")
	}
	if !strings.Contains(err.Error(), "banner") {
		t.Fatalf("err = %v", err)
	}
}

// Closing must be idempotent and must make later calls fail with a clear message
// rather than a dead-pipe write.
func TestPluginDriverCloseIsFinal(t *testing.T) {
	binary := buildFakePlugin(t)
	drv, err := plugindriver.Start(context.Background(), binary, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := drv.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := drv.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := drv.ListSchemas(context.Background()); err == nil {
		t.Fatal("a closed driver must refuse calls")
	}
}

// A cancelled context must not hang the host waiting for a reply the plugin will not
// send.
func TestPluginDriverHonoursContext(t *testing.T) {
	binary := buildFakePlugin(t)
	drv, err := plugindriver.Start(context.Background(), binary, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer drv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := drv.ListSchemas(ctx); err == nil {
		t.Fatal("a cancelled context must fail the call")
	}
}

// Call is the raw round trip a wider-session client (fynequery's host) uses
// for its own vocabulary; CallStream walks the multi-frame replies. Both go
// through the connection-oriented shape, so the test drives StartConn — one
// process, one Open, the Bind view — exactly the way the UI host consumes it.
func TestCallRawAndStreamThroughConnDriver(t *testing.T) {
	binary := buildFakePlugin(t)
	ctx := context.Background()

	conn, err := plugindriver.StartConn(ctx, binary, nil)
	if err != nil {
		t.Fatalf("start conn: %v", err)
	}
	defer conn.Close()

	// Raw single-shot call in the plugin's own vocabulary.
	var pong string
	if err := conn.Call(ctx, plugindriver.MethodPing, nil, &pong); err != nil {
		t.Fatalf("call ping: %v", err)
	}
	if pong != "pong" {
		t.Fatalf("ping = %q", pong)
	}

	// Streaming: every frame in order, then the terminal aggregate.
	frames := 0
	var rows []int64
	var final struct {
		RowsAffected int64 `json:"rowsAffected"`
	}
	err = conn.CallStream(ctx, "stream", nil, func(f plugindriver.StreamFrame) (bool, error) {
		frames++
		if frames == 1 && (len(f.Columns) != 1 || f.Columns[0].Name != "n") {
			t.Fatalf("first frame = %+v", f)
		}
		for _, row := range f.Rows {
			if len(row) == 1 {
				rows = append(rows, row[0].Int)
			}
		}
		return true, nil
	}, &final)
	if err != nil {
		t.Fatalf("call stream: %v", err)
	}
	if frames != 3 || len(rows) != 3 || rows[2] != 3 {
		t.Fatalf("frames = %d rows = %v", frames, rows)
	}
	if final.RowsAffected != 3 {
		t.Fatalf("terminal aggregate = %+v", final)
	}

	// Bind still hands out the kernel's SQLDriver view of a connection.
	if bound := conn.Bind("any", "db"); bound == nil {
		t.Fatal("bind returned no driver view")
	}
}

// TestConnDriverDeadAfterPluginExit pins the death signal: a plugin that exits
// mid-session turns the driver dead — not just the one call that was in
// flight — so a host can tell "restartable corpse" from "live process" without
// probing it with a request.
func TestConnDriverDeadAfterPluginExit(t *testing.T) {
	binary := buildFakePlugin(t)
	ctx := context.Background()

	conn, err := plugindriver.StartConn(ctx, binary, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer conn.Close()
	if conn.Dead() {
		t.Fatal("a fresh driver reports dead")
	}
	if err := conn.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	// The plugin answers this call and then exits: the reply lands, the
	// process dies, and the host must notice within a bounded wait.
	var exitAck interface{}
	if err := conn.Call(ctx, "exit", nil, &exitAck); err != nil {
		t.Fatalf("call exit: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !conn.Dead() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !conn.Dead() {
		t.Fatal("driver still reports alive after the plugin exited")
	}
	if err := conn.Ping(ctx); err == nil {
		t.Fatal("a ping on a dead driver must fail")
	}

	// The wide view of a connection on the dead process reports the same.
	if !conn.Wide("c1", "db").Dead() {
		t.Fatal("a wide view of a dead process reports alive")
	}
}

// TestDriverDeadAfterClose covers the polite path: an explicitly closed driver
// is dead the moment Close returns, not only once the child reaps.
func TestDriverDeadAfterClose(t *testing.T) {
	binary := buildFakePlugin(t)
	drv, err := plugindriver.Start(context.Background(), binary, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := drv.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !drv.Dead() {
		t.Fatal("a closed driver reports alive")
	}
}
