package model

// 模型→DDL 的测试：依赖序、外键、N:M 关联表、方言类型映射。

import (
	"strings"
	"testing"
)

func sampleModel() *Model {
	return &Model{
		Name:    "shop",
		Dialect: "mysql",
		Entities: []Entity{
			{Name: "orders", Columns: []Column{
				{Name: "id", Type: "long"}, {Name: "user_id", Type: "long"},
				{Name: "total", Type: "decimal", Comment: "订单总额"},
			}, PrimaryKey: []string{"id"}},
			{Name: "users", Columns: []Column{
				{Name: "id", Type: "long"}, {Name: "email", Type: "text"},
			}, PrimaryKey: []string{"id"}},
			{Name: "tags", Columns: []Column{
				{Name: "id", Type: "int"}, {Name: "label", Type: "text"},
			}, PrimaryKey: []string{"id"}},
		},
		Relations: []Relation{
			{From: "users", To: "orders", Card: "1:N", OnDelete: "CASCADE"},
			{From: "users", To: "tags", Card: "N:M"},
		},
	}
}

func TestGenerateDDLOrderParentsFirst(t *testing.T) {
	// users 在输入里排第二，但 orders 引用 users——users 必须先建。
	got, err := sampleModel().GenerateDDL()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(got, "CREATE TABLE `users`") > strings.Index(got, "CREATE TABLE `orders`") {
		t.Fatalf("依赖序不对（父表应先建）:\n%s", got)
	}
}

func TestGenerateDDLForeignKeyAndCascade(t *testing.T) {
	got, err := sampleModel().GenerateDDL()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "FOREIGN KEY (`id`) REFERENCES `users` (`id`) ON DELETE CASCADE") {
		t.Errorf("外键/CASCADE 不对:\n%s", got)
	}
	// 引用列沿用 source 主键名（建模者显式建的 user_id 列与引用列并存——
	// 模型的宽容：显式建的列优先，内核不重命名）。
	// 注释走字面量。
	if !strings.Contains(got, "COMMENT '订单总额'") {
		t.Errorf("列注释缺失:\n%s", got)
	}
}

func TestGenerateDDLJoinTableForNM(t *testing.T) {
	got, err := sampleModel().GenerateDDL()
	if err != nil {
		t.Fatal(err)
	}
	// 关联表名按字母序拼接：tags_users。
	if !strings.Contains(got, "CREATE TABLE `tags_users` (") {
		t.Errorf("N:M 关联表缺失:\n%s", got)
	}
	if !strings.Contains(got, "PRIMARY KEY (`id`, `id`)") {
		t.Errorf("关联表联合主键不对（两方主键同名 id——列名沿用）:\n%s", got)
	}
}

func TestGenerateDDLDialectTypeMapping(t *testing.T) {
	m := sampleModel()
	m.Dialect = "postgres"
	got, err := m.GenerateDDL()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"users"`, "BIGINT", "TEXT", "NUMERIC(18,4)"} {
		if !strings.Contains(got, want) {
			t.Errorf("PG 方言缺 %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "TINYINT(1)") || strings.Contains(got, "`") {
		t.Errorf("PG 方言里混入 MySQL 拼写:\n%s", got)
	}
}

// 未知类型透传（用户直写方言类型）。
func TestMapTypePassthrough(t *testing.T) {
	tm := typeMapFor("mysql")
	if got := tm.mapType("JSON"); got != "JSON" {
		t.Errorf("未知类型应透传: %s", got)
	}
}
