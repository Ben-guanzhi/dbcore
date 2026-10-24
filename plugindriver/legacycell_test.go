package plugindriver_test

// The compatibility layer that lets one host read both cell forms.
//
// fynequery's plugins used to send every cell as a nullable string; the kernel's
// send tagged cells. The host reads whichever arrived, so a plugin built before
// the move keeps working and one built after keeps the exact kinds.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/plugindriver"
)

// legacyCellPlugin is a plugin that speaks the old string-cell form.
const legacyCellPlugin = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/dbcore/dbcore/plugindriver"
)

func main() {
	out := bufio.NewWriter(os.Stdout)
	banner, _ := json.Marshal(plugindriver.Banner{
		Proto:   plugindriver.ProtocolName,
		Version: plugindriver.ProtocolVersion,
		Kind:    "sqlite",
		Name:    "Legacy",
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
	send := func(result interface{}) {
		payload, _ := json.Marshal(result)
		frame, _ := json.Marshal(plugindriver.Response{ID: req.ID, Result: payload})
		fmt.Fprintf(out, "%s\n", frame)
		out.Flush()
	}
	switch req.Method {
	case plugindriver.MethodOpen:
		send(map[string]interface{}{"connId": "legacy-1", "server": "legacy"})
	case plugindriver.MethodConnQuery:
		// The old form: every cell is a string, and a null is a JSON null.
		send(map[string]interface{}{
			"columns": []map[string]interface{}{{"name": "id"}, {"name": "note"}},
			"rows":    [][]interface{}{{"42", nil}},
		})
	case plugindriver.MethodConnBrowse:
		send(map[string]interface{}{
			"columns": []map[string]interface{}{{"name": "id"}},
			"rows":    [][]interface{}{{"7"}},
		})
	default:
		// A plugin that does not know a method says so, rather than answering
		// with an empty result: the host then tries the other spelling.
		frame, _ := json.Marshal(plugindriver.Response{ID: req.ID, Error: "unknown method " + req.Method})
		fmt.Fprintf(out, "%s\n", frame)
		out.Flush()
	}
}
`

// buildLegacyCellPlugin compiles a plugin that answers with string cells.
func buildLegacyCellPlugin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(legacyCellPlugin), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	kernelRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	goMod := "module legacycell\n\ngo 1.24\n\nrequire github.com/dbcore/dbcore v0.0.0\n\nreplace github.com/dbcore/dbcore => " +
		filepath.ToSlash(kernelRoot) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	binary := filepath.Join(dir, "legacycell")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return binary
}

// The host must read a plugin's string cells: the text becomes a text value, and
// the null stays a null.
func TestHostReadsStringCellsFromAnOlderPlugin(t *testing.T) {
	binary := buildLegacyCellPlugin(t)

	host, err := plugindriver.StartConn(context.Background(), binary, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer host.Close()

	connID, _, err := host.Open(context.Background(), plugindriver.ConnectParams{
		Name:     "legacy",
		Database: filepath.Join(t.TempDir(), "legacy.db"),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	conn := host.Bind(connID)

	result, err := conn.Execute(context.Background(), "SELECT id, note")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(result.Rows) != 1 || len(result.Rows[0]) != 2 {
		t.Fatalf("rows = %+v", result.Rows)
	}
	// The string cell became a text value.
	if result.Rows[0][0].Kind != dbcore.ValText || result.Rows[0][0].S != "42" {
		t.Fatalf("cell = %+v", result.Rows[0][0])
	}
	// The null stayed a null and did not become an empty string.
	if result.Rows[0][1].Kind != dbcore.ValNull {
		t.Fatalf("the null cell became %+v", result.Rows[0][1])
	}
}
