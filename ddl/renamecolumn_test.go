package ddl

// 列重命名 DDL 的测试：方言拼写、输入校验、不支持引擎的拒绝。

import (
	"strings"
	"testing"
)

func TestRenameColumnDialects(t *testing.T) {
	cases := []struct {
		dbType string
		want   string
	}{
		{"mysql", "ALTER TABLE `users` RENAME COLUMN `name` TO `full_name`"},
		{"postgresql", `ALTER TABLE "users" RENAME COLUMN "name" TO "full_name"`},
		{"sqlserver", "ALTER TABLE [users] RENAME COLUMN [name] TO [full_name]"},
		{"sqlite", `ALTER TABLE "users" RENAME COLUMN "name" TO "full_name"`},
	}
	for _, c := range cases {
		got, err := RenameColumn(c.dbType, "", "users", "name", "full_name")
		if err != nil {
			t.Fatalf("%s: %v", c.dbType, err)
		}
		if got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.dbType, got, c.want)
		}
	}
}

func TestRenameColumnSchemaQualified(t *testing.T) {
	got, err := RenameColumn("postgresql", "shop", "users", "name", "full_name")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"shop"."users"`) {
		t.Fatalf("schema 未带上: %s", got)
	}
}

func TestRenameColumnRejectsEmptyNames(t *testing.T) {
	if _, err := RenameColumn("mysql", "", "users", "", "x"); err == nil {
		t.Error("空旧名必须拒绝")
	}
	if _, err := RenameColumn("mysql", "", "users", "a", "  "); err == nil {
		t.Error("空新名必须拒绝")
	}
}
