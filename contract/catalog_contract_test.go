// Package contract 承载「跨语言目录配方契约」的 Go 半（C3）。
//
// 契约向量由内核仓库持有：spec/vectors/catalog-contract.json。Rust 侧
// （crates/db-core/tests/catalog_contract.rs）断言同一份向量的另一面。
//
// 为什么要这一层：C1（列/表注释在 Rust 侧全缺）与 C2（PG serial 漏判自增）都不
// 是某一次写错，而是「同一份配方在两种语言里各写一遍、没有共享用例」——改一边
// 不知道另一边。这里断言的是每份配方**必须产出哪些字段所依赖的表达式**，两条
// 已有的漂移都会在这里红。
package contract

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/mssql"
	"github.com/dbcore/dbcore/mysql"
	"github.com/dbcore/dbcore/oracle"
	"github.com/dbcore/dbcore/postgres"
)

type catalogVector struct {
	Kinds map[string]struct {
		Fields []string `json:"fields"`
	} `json:"kinds"`
	Engines map[string]map[string][]string `json:"engines"`
}

// collapse 把 SQL 归一成「小写 + 单空格」：两侧的缩进/换行/大小写不同，
// 但表达式本身必须一致。
func collapse(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

func TestCatalogContract(t *testing.T) {
	raw, err := os.ReadFile("../spec/vectors/catalog-contract.json")
	if err != nil {
		t.Fatalf("读契约向量: %v", err)
	}
	var v catalogVector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("解析契约向量: %v", err)
	}
	if len(v.Engines) == 0 {
		t.Fatal("契约向量里没有任何引擎：测试会空转")
	}
	for name, spec := range v.Kinds {
		if len(spec.Fields) == 0 {
			t.Errorf("kind %q 的字段清单为空", name)
		}
	}

	queries := map[string]func(dbcore.CatalogKind) (string, []dbcore.Value){
		"postgres": func(k dbcore.CatalogKind) (string, []dbcore.Value) {
			return postgres.PostgresDialect{}.CatalogQuery(k, "s", "t")
		},
		"mysql": func(k dbcore.CatalogKind) (string, []dbcore.Value) {
			return mysql.MySQLDialect{}.CatalogQuery(k, "s", "t")
		},
		"mssql": func(k dbcore.CatalogKind) (string, []dbcore.Value) {
			return mssql.MSSQLDialect{}.CatalogQuery(k, "s", "t")
		},
		"oracle": func(k dbcore.CatalogKind) (string, []dbcore.Value) {
			return oracle.OracleDialect{}.CatalogQuery(k, "s", "t")
		},
	}
	kinds := map[string]dbcore.CatalogKind{
		"tables":        dbcore.CatalogTables,
		"columns":       dbcore.CatalogColumns,
		"batch_columns": dbcore.CatalogBatchColumns,
	}

	for engine, perKind := range v.Engines {
		q, ok := queries[engine]
		if !ok {
			t.Fatalf("向量声明了未知引擎 %q：需要同步本测试的引擎表", engine)
		}
		if len(perKind) == 0 {
			t.Errorf("引擎 %q 没有任何 kind", engine)
		}
		for kindName, markers := range perKind {
			kind, ok := kinds[kindName]
			if !ok {
				t.Fatalf("%s: 未知 catalog kind %q", engine, kindName)
			}
			sql, _ := q(kind)
			if strings.TrimSpace(sql) == "" {
				t.Errorf("%s/%s: 生成了空 SQL（该引擎没有实现这个 kind）", engine, kindName)
				continue
			}
			got := collapse(sql)
			for _, m := range markers {
				if !strings.Contains(got, collapse(m)) {
					t.Errorf("%s/%s: SQL 缺少 marker %q —— 这是 C1（注释）/C2（serial 自增）那类漂移\nSQL=%s",
						engine, kindName, m, sql)
				}
			}
		}
	}
}
