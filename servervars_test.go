package dbcore

// ServerVariables 能力的内核侧测试：排序口径 + 能力断言。

import (
	"context"
	"testing"
)

func TestSortVariablesCaseInsensitive(t *testing.T) {
	vs := []ServerVariable{
		{Name: "wait_timeout"},
		{Name: "Autocommit"},
		{Name: "back_log"},
		{Name: "character_set_server"},
	}
	SortVariables(vs)
	want := []string{"Autocommit", "back_log", "character_set_server", "wait_timeout"}
	for i, w := range want {
		if vs[i].Name != w {
			t.Fatalf("排序[%d] = %q, want %q", i, vs[i].Name, w)
		}
	}
}

func TestSortVariablesEmptyAndSingle(t *testing.T) {
	SortVariables(nil)
	one := []ServerVariable{{Name: "x"}}
	SortVariables(one)
	if one[0].Name != "x" {
		t.Fatal("单元素被改动")
	}
}

// 能力断言：实现了就给，没实现就 false（UI 据此降级）。
type svDriver struct{ Driver }
type noSVDriver struct{ Driver }

func (d svDriver) ServerVariables(context.Context) ([]ServerVariable, error) { return nil, nil }

func TestAsServerVariablesAssertsCapability(t *testing.T) {
	if _, ok := AsServerVariables(svDriver{}); !ok {
		t.Error("实现了 ServerVariablesReader 的驱动断言失败")
	}
	if _, ok := AsServerVariables(noSVDriver{}); ok {
		t.Error("未实现的驱动不该断言成功")
	}
}
