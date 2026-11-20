package main

// dbcore-bridge 的协议与造数验证（§360）：handle 直调 + 真实 SQLite
// 端到端——ListActions 清单、RunAction 现场拨号、内核 GenerateInserts
// 经 sidecar 真把行写进表。

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ben-guanzhi/dbcore/plugindriver"
	"github.com/Ben-guanzhi/dbcore/sqlite"
)

func TestBridgeListsActions(t *testing.T) {
	resp := handle(plugindriver.Request{Method: plugindriver.MethodConnListActions})
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	var actions []struct{ ID string }
	if err := json.Unmarshal(resp.Result, &actions); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range actions {
		if a.ID == "datagen" {
			found = true
		}
	}
	if !found {
		t.Fatalf("datagen missing from %v", actions)
	}
}

func TestBridgeDataGenWritesRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bridge.db")
	drv, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := drv.Execute(ctx, "CREATE TABLE items (id INTEGER PRIMARY KEY, label TEXT)"); err != nil {
		t.Fatal(err)
	}
	drv.Close()

	args := map[string]any{
		"kind": "sqlite", "database": path, "table": "items",
		"rows": float64(5), "seed": float64(7),
	}
	resp := handle(plugindriver.Request{
		Method: plugindriver.MethodConnRunAction,
		Params: mustJSON(map[string]any{"action": "datagen", "args": args}),
	})
	if resp.Error != "" {
		t.Fatalf("datagen: %s", resp.Error)
	}

	// 验证行真的进了表（sidecar 拨的号已关，重新打开读）。
	drv2, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer drv2.Close()
	res, err := drv2.Execute(ctx, "SELECT COUNT(*) FROM items")
	if err != nil {
		t.Fatal(err)
	}
	if n := res.Rows[0][0]; n.Kind != 1 || n.I != 5 { // Kind 1 = ValInt
		t.Fatalf("rows in table = %v, want 5", n)
	}
}

func TestBridgeBadKindErrors(t *testing.T) {
	resp := handle(plugindriver.Request{
		Method: plugindriver.MethodConnRunAction,
		Params: mustJSON(map[string]any{
			"action": "datagen",
			"args":   map[string]any{"kind": "oracle-nope", "table": "t"},
		}),
	})
	if resp.Error == "" || !strings.Contains(resp.Error, "拨号失败") {
		t.Fatalf("unknown kind must fail dialing, got %q", resp.Error)
	}
}
