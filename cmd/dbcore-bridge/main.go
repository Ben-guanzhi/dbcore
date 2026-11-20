// Command dbcore-bridge is the kernel capability sidecar: a newline-JSON
// plugin process (plugindriver protocol) that exposes the kernel's
// non-SQL-catalog capabilities — data generation, row transfer and server
// session management — as console actions, so a non-Go host (sqlgpui's Rust
// workbench) can share them without a language binding.
//
// 设计（§360 决策 B 最小版）：
//   - 无状态：每个 RunAction 自带连接参数（DSN），sidecar 现场拨号、执行、
//     关闭。连接不复用——Rust 宿主的 sqlx 池仍是数据的常驻通道，sidecar
//     只承载「内核逻辑」这一段。
//   - 凭据走 stdin 协议（同机进程，与 TableProGo 插件同级安全面），不进 argv。
//   - 能力随内核增长：sidecar 升级 = Rust 宿主免费获得新版本的能力。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	// 各引擎包 import 即注册工厂（sidecar 支持全部内置引擎的拨号）。
	_ "github.com/Ben-guanzhi/dbcore/dm"
	_ "github.com/Ben-guanzhi/dbcore/mssql"
	_ "github.com/Ben-guanzhi/dbcore/mysql"
	_ "github.com/Ben-guanzhi/dbcore/oracle"
	_ "github.com/Ben-guanzhi/dbcore/postgres"
	_ "github.com/Ben-guanzhi/dbcore/sqlite"

	"github.com/Ben-guanzhi/dbcore"
	"github.com/Ben-guanzhi/dbcore/plugindriver"
)

const sidecarVersion = "0.1.0"

func main() {
	out := bufio.NewWriter(os.Stdout)
	banner, _ := json.Marshal(plugindriver.Banner{
		Proto:   plugindriver.ProtocolName,
		Version: plugindriver.ProtocolVersion,
		Kind:    "dbcore-bridge",
		Name:    "内核能力桥接 sidecar",
	})
	fmt.Fprintf(out, "%s\n", banner)
	out.Flush()

	in := bufio.NewReader(os.Stdin)
	for {
		line, err := in.ReadBytes('\n')
		if len(line) > 0 {
			var req plugindriver.Request
			if json.Unmarshal(line, &req) == nil {
				resp := handle(req)
				resp.ID = req.ID
				b, _ := json.Marshal(resp)
				fmt.Fprintf(out, "%s\n", b)
				out.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// bridgeActions 是 sidecar 声明的能力清单（ListActions 返回）。
func bridgeActions() []plugindriver.ConnAction {
	return []plugindriver.ConnAction{
		{
			ID:    "datagen",
			Title: "生成测试数据",
			Group: "内核能力",
			Params: []plugindriver.ConnActionParam{
				{Name: "kind", Label: "引擎", Required: true, Options: []string{"mysql", "postgres", "sqlserver", "sqlite", "oracle", "dm"}},
				{Name: "host", Label: "主机", Required: true},
				{Name: "port", Label: "端口", Kind: "number"},
				{Name: "user", Label: "用户"},
				{Name: "password", Label: "密码"},
				{Name: "database", Label: "数据库"},
				{Name: "table", Label: "表名", Required: true},
				{Name: "rows", Label: "行数", Kind: "number", Default: "10"},
				{Name: "seed", Label: "种子", Kind: "number", Default: "0"},
			},
		},
	}
}

// openDriver 按结构化连接参数拨号（内核工厂表——引擎包已 import 注册）。
func openDriver(ctx context.Context, args map[string]any) (dbcore.SQLDriver, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	port := 0
	if p := argInt(args, "port", 0); p > 0 {
		port = int(p)
	}
	spec := dbcore.ConnSpec{
		Kind:     dbcore.DatabaseKind(argString(args, "kind")),
		Host:     argString(args, "host"),
		Port:     port,
		User:     argString(args, "user"),
		Password: argString(args, "password"),
		Database: argString(args, "database"),
	}
	// SQLite 是文件型引擎：连接参数里没有 host/user，path 就是库本身。
	if spec.Kind == dbcore.KindSQLite {
		if f := argString(args, "file"); f != "" {
			spec.File = f
		} else {
			spec.File = spec.Database
		}
		spec.Database = ""
	}
	drv, err := dbcore.Open(ctx, spec, dbcore.PoolConfig{MaxOpen: 1})
	if err != nil {
		return nil, err
	}
	sqlDrv, ok := drv.AsSQL()
	if !ok {
		drv.Close()
		return nil, fmt.Errorf("引擎 %s 不提供 SQL 面（KV 引擎不支持本能力）", spec.Kind)
	}
	return sqlDrv, nil
}

// handle 分发请求：ListActions 静态清单，RunAction 现场拨号执行。
func handle(req plugindriver.Request) plugindriver.Response {
	switch req.Method {
	case plugindriver.MethodConnListActions:
		return plugindriver.Response{Result: mustJSON(bridgeActions())}
	case plugindriver.MethodConnRunAction:
		return runAction(req)
	}
	return plugindriver.Response{Error: "unsupported method: " + req.Method}
}

// runAction 执行一个能力：解析参数 → 拨号 → 调内核 → 结果作为网格返回。
func runAction(req plugindriver.Request) plugindriver.Response {
	var p plugindriver.ConnRunActionParams
	if err := json.Unmarshal(mustJSON(req.Params), &p); err != nil {
		return plugindriver.Response{Error: err.Error()}
	}
	if argString(p.Args, "kind") == "" {
		return plugindriver.Response{Error: "缺少 kind 参数（mysql/postgres/sqlserver/sqlite/oracle/dm）"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	drv, err := openDriver(ctx, p.Args)
	if err != nil {
		return plugindriver.Response{Error: "拨号失败: " + err.Error()}
	}
	defer drv.Close()

	switch p.Action {
	case "datagen":
		return actionDataGen(ctx, drv, p.Args)
	default:
		return plugindriver.Response{Error: "unknown action: " + p.Action}
	}
}

// actionDataGen 用内核 GenerateInserts 造数并写回同一连接的表。
func actionDataGen(ctx context.Context, drv dbcore.SQLDriver, args map[string]any) plugindriver.Response {
	table := argString(args, "table")
	if table == "" {
		return plugindriver.Response{Error: "缺少 table 参数"}
	}
	rows := int(argInt(args, "rows", 10))
	seed := uint64(argInt(args, "seed", 0))
	cols, err := drv.ListColumns(ctx, "", table)
	if err != nil {
		return plugindriver.Response{Error: "读取列失败: " + err.Error()}
	}
	stmts := dbcore.GenerateInserts(cols, dbcore.GenOptions{
		Rows: rows, Seed: seed, Table: table, SkipPrimaryKey: true,
	})
	executed := 0
	for _, stmt := range stmts {
		if _, err := drv.Execute(ctx, stmt); err != nil {
			return plugindriver.Response{Error: fmt.Sprintf("第 %d 条失败: %v（已执行 %d 条）", executed+1, err, executed)}
		}
		executed++
	}
	return plugindriver.Response{Result: mustJSON(&plugindriver.ConnQueryResult{
		Columns: []plugindriver.ConnColumn{{Name: "inserted"}, {Name: "table"}, {Name: "seed"}},
		Rows:    [][]any{{executed, table, seed}},
	})}
}

// ---- 小工具 ----

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func argString(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func argInt(args map[string]any, key string, def int64) int64 {
	if v, ok := args[key].(float64); ok {
		return int64(v)
	}
	return def
}
