package ddl

// 视图删除的方言差异：MSSQL 用 IF OBJECT_ID 守卫，其余 DROP VIEW IF EXISTS。

import (
	"strings"
	"testing"
)

func TestDropViewDialects(t *testing.T) {
	cases := []struct {
		dbType string
		want   string
	}{
		{"mysql", "DROP VIEW IF EXISTS `v`"},
		{"postgresql", `DROP VIEW IF EXISTS "v"`},
		{"sqlite", `DROP VIEW IF EXISTS "v"`},
		{"sqlserver", "IF OBJECT_ID('v', 'V') IS NOT NULL DROP VIEW [v]"},
	}
	for _, c := range cases {
		got := DropView(c.dbType, func() string {
			switch c.dbType {
			case "mysql":
				return "`v`"
			case "sqlserver":
				return "[v]"
			default:
				return `"v"`
			}
		}())
		if got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.dbType, got, c.want)
		}
	}
}

func TestDropViewSchemaQualified(t *testing.T) {
	got := DropView("postgresql", `"shop"."v"`)
	if !strings.HasPrefix(got, "DROP VIEW IF EXISTS ") || !strings.HasSuffix(got, `"shop"."v"`) {
		t.Fatalf("schema 限定名未原样带上: %s", got)
	}
}
