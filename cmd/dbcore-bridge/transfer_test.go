package main

// transfer 端到端（§361）：源/目标双 SQLite 文件库经 sidecar 的
// actionTransfer——内核 TransferTable 的真实调用路径。

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Ben-guanzhi/dbcore"
	"github.com/Ben-guanzhi/dbcore/plugindriver"
	"github.com/Ben-guanzhi/dbcore/sqlite"
)

func TestBridgeTransferSQLiteToSQLite(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.db")
	dstPath := filepath.Join(dir, "dst.db")

	// 源库建表 + 3 行；目标库建同名空表。
	for _, spec := range []struct {
		path string
		seed bool
	}{
		{srcPath, true},
		{dstPath, false},
	} {
		drv, err := sqlite.Open(spec.path)
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if _, err := drv.Execute(ctx, "CREATE TABLE goods (id INTEGER PRIMARY KEY, label TEXT)"); err != nil {
			t.Fatal(err)
		}
		if spec.seed {
			for i := 1; i <= 3; i++ {
				if _, err := drv.Execute(ctx,
					"INSERT INTO goods VALUES (?, ?)", dbcoreInt(int64(i)), dbcoreText("row")); err != nil {
					t.Fatal(err)
				}
			}
		}
		drv.Close()
	}

	resp := handle(plugindriver.Request{
		Method: plugindriver.MethodConnRunAction,
		Params: mustJSON(map[string]any{
			"action": "transfer",
			"args": map[string]any{
				"src_kind": "sqlite", "src_file": srcPath,
				"dst_kind": "sqlite", "dst_file": dstPath,
				"table": "goods",
			},
		}),
	})
	if resp.Error != "" {
		t.Fatalf("transfer: %s", resp.Error)
	}

	// 目标库应有 3 行。
	drv, err := sqlite.Open(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	defer drv.Close()
	res, err := drv.Execute(context.Background(), "SELECT COUNT(*) FROM goods")
	if err != nil {
		t.Fatal(err)
	}
	if n := res.Rows[0][0]; n.I != 3 {
		t.Fatalf("rows in dst = %v, want 3", n)
	}
}

// dbcoreText 用内核 QuotedLiteral 生成文本字面量（SQLite 直接受）。
func dbcoreInt(v int64) dbcore.Value   { return dbcore.Value{Kind: dbcore.ValInt, I: v} }
func dbcoreText(v string) dbcore.Value { return dbcore.Value{Kind: dbcore.ValText, S: v} }
