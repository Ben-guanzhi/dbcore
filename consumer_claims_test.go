// Package dbcore_test 里的这个文件把 "谁接入了共享内核、接到什么程度" 变成可执行断言。
//
// 背景: WORKSPACE.md / DB-CORE-CONTRACT.md 曾声称 fynequery 的目录读取"走
// dbcore.SQLDriver"、polydb "已消费 dbcore", 而代码里 SQLDriver 引用为 0、
// polydb 只有 SQLite 真正接入。文档漂移无法被 CI 发现, 于是把声称写成测试:
// 代码或文档任何一方变化, 这里就会红, 维护者必须同时改文档与断言。
//
// 断言只读同工作区里的兄弟目录 (../TableProGo 等); 单独 clone dbcore 时自动跳过。
package dbcore_test

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// consumerClaim 描述一个消费者当前被文档承诺的接入程度。
type consumerClaim struct {
	name string
	dir  string // 相对 dbcore 目录
	doc  string // 对应文档位置, 漂移时用于定位

	// requires 必须出现在消费者源码 import 列表里的路径。
	requires []string
	// requiresSymbols 必须出现在消费者源码里的标识符 (证明真的用了, 不只是 import)。
	requiresSymbols []string
	// forbids 尚不应出现的标识符。接入程度提升时, 请同时修改本断言与文档。
	forbids []string

	// dbcoreImportDirs 非空时, 断言"只有这些目录 import 根包 dbcore",
	// 用来固化诸如 "polydb 仅 SQLite 接入" 这类有限接入的现状。
	dbcoreImportDirs []string
}

var claims = []consumerClaim{
	{
		name: "TableProGo",
		dir:  "TableProGo",
		doc:  "WORKSPACE.md §2 TableProGo 行 / DB-CORE-CONTRACT.md §8.3-8.6",
		requires: []string{
			"github.com/dbcore/dbcore",
			"github.com/dbcore/dbcore/sqltext",
			"github.com/dbcore/dbcore/keyset",
			"github.com/dbcore/dbcore/mysql",
			"github.com/dbcore/dbcore/postgres",
			"github.com/dbcore/dbcore/mssql",
			"github.com/dbcore/dbcore/oracle",
			"github.com/dbcore/dbcore/sqlite",
			"github.com/dbcore/dbcore/redis",
		},
		// keyset = P1-7：表浏览键集分页算法经 internal/paging 适配层消费内核
		// dbcore/keyset（适配层不得长出引擎知识）。
		requiresSymbols: []string{"dbcore.SQLDriver", "dbcore.SQLTxDriver", "keyset.Keys"},
	},
	{
		name: "fynequery",
		dir:  "fynequery",
		doc:  "WORKSPACE.md §2 fynequery 行 (冻结: 目录/索引/KV 只读消费 + 插件协议经内核 plugindriver; Session/DML/连接不再迁入内核 — INTEGRATION-PLAN P1-6)",
		requires: []string{
			"github.com/dbcore/dbcore",
			"github.com/dbcore/dbcore/sqltext",
			"github.com/dbcore/dbcore/mysql",
			"github.com/dbcore/dbcore/postgres",
			"github.com/dbcore/dbcore/mssql",
			"github.com/dbcore/dbcore/oracle",
			"github.com/dbcore/dbcore/redis",
			"github.com/dbcore/dbcore/plugindriver",
		},
		// fynequery 的接入深度已定格 (P1-6 裁定, 对应 DECISION §2.5 "冻结为组件参考"):
		// 目录/索引经内核驱动只读消费, Redis 经 KVDriver, 方言字符串走 sqltext。
		// 插件协议 (NDJSON + banner) 已上游化进内核 plugindriver: fynequery 的
		// proto 是内核协议的类型别名视图, 线上 cell 用带标签结构体 (NULL 与空串
		// 不再同形)。
		// Session.Query/DML/连接生命周期不再迁入内核 —— 出现下列任一符号即说明
		// 冻结被打破, 请先更新 DECISION 再动代码。
		requiresSymbols: []string{"dbcore.IndexInfo", "plugindriver.ProtocolName"},
		forbids: []string{
			"dbcore.Open(",      // 连接生命周期迁内核的标志
			"dbcore.PoolConfig", // 连接池迁内核的标志
			"dbcore.TxManager",  // DML/事务迁内核的标志
		},
	},
	{
		name: "polydb",
		dir:  "polydb/go",
		doc:  "WORKSPACE.md §2 polydb 行 (实际: bridge 再导出 + SQLite/MySQL/PG/MSSQL/Oracle 五引擎目录与 DDL + Redis KV 均接内核)",
		requires: []string{
			"github.com/dbcore/dbcore",
			"github.com/dbcore/dbcore/mysql",
			"github.com/dbcore/dbcore/postgres",
			"github.com/dbcore/dbcore/mssql",
			"github.com/dbcore/dbcore/oracle",
			"github.com/dbcore/dbcore/redis",
			"github.com/polydb/polydb/pkg/dbcore/bridge",
		},
		// 只有这些包的 import 列表会被检查 (见 dbcoreImportDirs)。四引擎的目录/
		// DDL 经 NewFromDB 委托内核驱动, Redis 经 KVDriver 委托 (P1-5, 含 D5
		// 续扫/结构化 RESP/流条目三个内核增值接口)；执行/事务/表浏览分页仍是
		// polydb 自有实现 (见 polydb AGENTS.md 的边界声明)。
		dbcoreImportDirs: []string{
			"pkg/dbcore/bridge", "pkg/dbsqlite",
			"pkg/dbmysql", "pkg/dbpostgres", "pkg/dbmssql", "pkg/dboracle",
			"pkg/dbredis",
		},
	},
}

func TestConsumersKeepTheIntegrationTheyClaim(t *testing.T) {
	assertWorkspaceDocPointsHere(t)
	for _, c := range claims {
		c := c
		t.Run(c.name, func(t *testing.T) {
			root := filepath.Join("..", c.dir)
			if _, err := os.Stat(root); err != nil {
				t.Skipf("兄弟目录 %s 不存在 (单独 clone dbcore 时属正常): %v", root, err)
			}
			imports, sources, err := scanGo(root)
			if err != nil {
				t.Fatalf("scan %s: %v", root, err)
			}
			for _, want := range c.requires {
				if _, ok := imports[want]; !ok {
					t.Errorf("%s: 缺少必需 import %q (%s)\n文档声称的接入程度与代码不一致, 请同步更新 %s 或恢复代码",
						c.name, want, c.doc, c.doc)
				}
			}
			for _, sym := range c.requiresSymbols {
				if !strings.Contains(sources, sym) {
					t.Errorf("%s: 源码里找不到 %q (%s)", c.name, sym, c.doc)
				}
			}
			for _, sym := range c.forbids {
				if strings.Contains(sources, sym) {
					t.Errorf("%s: 出现 %q, 说明接入程度已提升; 请更新本断言并同步 %s", c.name, sym, c.doc)
				}
			}
			if len(c.dbcoreImportDirs) > 0 {
				got := dirsImportingDBCore(t, root)
				if !sameSet(got, c.dbcoreImportDirs) {
					t.Errorf("%s: import 根包 dbcore 的目录为 %v, 期望 %v (%s)",
						c.name, got, c.dbcoreImportDirs, c.doc)
				}
			}
			if err := requireLocalReplace(root); err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
		})
	}
}

// assertWorkspaceDocPointsHere 保证 WORKSPACE.md 仍然指向本断言文件:
// 文档若删掉这条指引, 读者就再也找不到"声称 vs 代码"的校验入口。
func assertWorkspaceDocPointsHere(t *testing.T) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "WORKSPACE.md"))
	if err != nil {
		t.Skipf("WORKSPACE.md 不可读 (单独 clone dbcore 时属正常): %v", err)
	}
	if !strings.Contains(string(b), "consumer_claims_test.go") {
		t.Errorf("WORKSPACE.md 未提及 consumer_claims_test.go: 接入程度声明失去了可执行校验入口")
	}
}

// scanGo 解析目录下所有 .go 文件的 import, 并返回拼接后的源码文本。
// 排除依赖目录与生成目录; 测试文件也算 (测试也会 import 内核)。
func scanGo(root string) (map[string]struct{}, string, error) {
	imports := map[string]struct{}{}
	var src strings.Builder
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "vendor", "dist", "build", "target", ".git":
				if path != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		for _, imp := range f.Imports {
			imports[strings.Trim(imp.Path.Value, `"`)] = struct{}{}
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		src.Write(b)
		src.WriteByte('\n')
		return nil
	})
	return imports, src.String(), err
}

// dirsImportingDBCore 返回 (相对 root 的) 直接 import 根包 dbcore 的目录集合。
func dirsImportingDBCore(t *testing.T, root string) []string {
	t.Helper()
	const pkg = `"github.com/dbcore/dbcore"`
	seen := map[string]struct{}{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "vendor", "dist", "build", "target", ".git":
				if path != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(b), pkg) {
			rel, _ := filepath.Rel(root, filepath.Dir(path))
			seen[filepath.ToSlash(rel)] = struct{}{}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}

// requireLocalReplace 校验消费者的 go.mod 仍用 replace 指向工作区里的 dbcore:
// 内核尚未发布, 去掉 replace 后本地构建会失败。
func requireLocalReplace(root string) error {
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return err
	}
	if !strings.Contains(string(b), "github.com/dbcore/dbcore =>") {
		return fmt.Errorf("go.mod 缺少 `replace github.com/dbcore/dbcore => ...`: 内核尚未发布, 去掉 replace 后本地无法构建")
	}
	return nil
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}
