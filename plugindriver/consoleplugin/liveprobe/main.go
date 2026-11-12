// Command liveprobe drives a REAL consoleplugin binary through the framed plugin
// protocol against a REAL service. It is a manual verification tool, not an automated
// test: it needs live services, so it must never run in `go test ./...`.
//
// Usage:
//
//	go run ./liveprobe <provider> <baseUrl> [extra open params as KEY=VALUE]
//
// It prints one line per protocol frame with the response decoded enough to see
// whether the real service's REST shape matches what the provider assumed.

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type request struct {
	ID     int             `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type response struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
	Code   string          `json:"code"`
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: liveprobe <provider> <baseUrl> [KEY=VALUE ...]")
		os.Exit(2)
	}
	provider, baseURL := os.Args[1], os.Args[2]
	options := map[string]string{"baseUrl": baseURL}
	for _, kv := range os.Args[3:] {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			options[k] = v
		}
	}

	// The plugin binary is built by the caller and passed in TABLEPRO_LIVE_BIN;
	// the default is what `go build -o consoleplugin.exe .` produces.
	bin := os.Getenv("TABLEPRO_LIVE_BIN")
	if bin == "" {
		bin = "./consoleplugin.exe"
	}
	cmd := exec.Command(bin, provider)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	out := bufio.NewWriter(stdin)
	in := bufio.NewReader(stdout)
	// The banner is the first line.
	banner, err := in.ReadString('\n')
	if err != nil {
		fatal(err)
	}
	fmt.Printf("BANNER %s", banner)

	id := 0
	call := func(method string, params any) response {
		id++
		var raw json.RawMessage
		if params != nil {
			raw, _ = json.Marshal(params)
		}
		frame, _ := json.Marshal(request{ID: id, Method: method, Params: raw})
		fmt.Fprintf(out, "%s\n", frame)
		_ = out.Flush()
		line, err := in.ReadString('\n')
		if err != nil {
			fatal(fmt.Errorf("read frame %d (%s): %w", id, method, err))
		}
		var resp response
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			fatal(fmt.Errorf("decode frame %d: %w (%s)", id, err, line))
		}
		// Print a compact view: the whole result is useful, but a config body or a
		// stats blob would flood the terminal, so cap it.
		res := strings.TrimSpace(string(resp.Result))
		if len(res) > 1200 {
			res = res[:1200] + "…(truncated)"
		}
		fmt.Printf("%-22s id=%d err=%q code=%q result=%s\n", method, resp.ID, resp.Error, resp.Code, res)
		return resp
	}

	// Derive host/port the way the real client does: ConnectParams carries them
	// SEPARATELY, so a baseUrl is a HTTP-provider-only override. Kafka reads
	// Host/Port, which is why passing "localhost:9092" as baseUrl alone used to
	// resolve to a bogus "live" host in the first live run.
	host, port := "localhost", ""
	if u := os.Getenv("TABLEPRO_LIVE_HOST"); u != "" {
		host = u
	} else {
		// Strip scheme and split host:port from the positional baseUrl.
		clean := strings.TrimPrefix(strings.TrimPrefix(baseURL, "http://"), "https://")
		if h, p, ok := strings.Cut(clean, ":"); ok {
			host, port = h, p
		} else {
			host = clean
		}
	}
	if p := os.Getenv("TABLEPRO_LIVE_PORT"); p != "" {
		port = p
	}
	openParams := map[string]any{
		"host":    host,
		"options": options,
	}
	if port != "" {
		openParams["port"] = port
	}
	open := call("open", openParams)
	var opened struct {
		ConnID string `json:"connId"`
	}
	_ = json.Unmarshal(open.Result, &opened)
	if open.Error != "" || opened.ConnID == "" {
		fatal(fmt.Errorf("open failed: %s %s", open.Error, open.Code))
	}
	ref := map[string]string{"connId": opened.ConnID}

	call("tables", ref)
	call("connListActions", ref)
	// Browse needs an object name; the caller can override via OBJECT=...
	obj := os.Getenv("TABLEPRO_LIVE_OBJECT")
	if obj == "" {
		obj = "DEFAULT_GROUP/liveprobe"
	}
	call("browse", map[string]any{"connId": opened.ConnID, "table": obj, "limit": 10})
	// A read-only action where one exists; the caller can override ACTION/ARGS.
	action := os.Getenv("TABLEPRO_LIVE_ACTION")
	if action == "" {
		action = "query-config"
	}
	args := map[string]any{}
	if a := os.Getenv("TABLEPRO_LIVE_ARGS"); a != "" {
		_ = json.Unmarshal([]byte(a), &args)
	}
	if len(args) == 0 {
		args = map[string]any{"dataId": "liveprobe", "group": "DEFAULT_GROUP"}
	}
	call("connRunAction", map[string]any{"connId": opened.ConnID, "action": action, "args": args})

	// Ask it to close politely, then give it a moment so a broken plugin cannot hang us.
	call("close", nil)
	time.Sleep(200 * time.Millisecond)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "liveprobe:", err)
	os.Exit(1)
}
