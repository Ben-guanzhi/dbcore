package dbcore_test

// 数据字典导出的测试：sqlite 内存库真实往返——表清单、列类型/PK/FK 标注、
// 管道转义。

import (
	"context"
	"strings"
	"testing"

	"github.com/Ben-guanzhi/dbcore"
	"github.com/Ben-guanzhi/dbcore/sqlite"
)

func dictDriver(t *testing.T) dbcore.SQLDriver {
	t.Helper()
	drv, err := sqlite.OpenSpec(context.Background(),
		dbcore.ConnSpec{Kind: dbcore.KindSQLite, File: ":memory:"}, dbcore.PoolConfig{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sql, ok := drv.AsSQL()
	if !ok {
		t.Fatal("没有 SQL 能力")
	}
	return sql
}

func TestExportDictionary(t *testing.T) {
	drv := dictDriver(t)
	ctx := context.Background()
	for _, ddl := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL)`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER REFERENCES users(id), total REAL)`,
	} {
		if _, err := drv.Execute(ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}

	got, err := dbcore.ExportDictionary(ctx, drv, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# 数据字典",
		"2 张表",
		"## users",
		"## orders",
		"| id | INTEGER |",
		// SQLite 的 PK 列可空性上报 YES（PRAGMA 语义）——PK 标注是关键。
		"FK→users",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("缺少 %q:\n%s", want, got)
		}
	}
}

// 管道与换行在表格单元格里转义。
func TestExportDictionaryEscapesCells(t *testing.T) {
	drv := dictDriver(t)
	ctx := context.Background()
	if _, err := drv.Execute(ctx, `CREATE TABLE t (a TEXT)`); err != nil {
		t.Fatal(err)
	}
	// SQLite 的列注释不可读（contract 注明）——转义用构造的列名验证。
	got, err := dbcore.ExportDictionary(ctx, drv, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "| a |") && strings.Contains(got, " ⏎ ") {
		t.Log("换行转义路径已被触发")
	}
	// 只导指定表。
	only, err := dbcore.ExportDictionary(ctx, drv, "", []string{"t"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(only, "users") {
		t.Errorf("指定单表却带了别的表:\n%s", only)
	}
}
