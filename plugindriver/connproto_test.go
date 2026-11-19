package plugindriver

// 声明式动作表单协议的兼容测试：新字段 omitempty、旧插件的 JSON-in-Default
// 选项仍能解析。

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestConnActionParamAdditiveEvolution(t *testing.T) {
	// 旧插件的四字段 JSON——新字段全零值。
	old := []byte(`{"name":"target","label":"Target","kind":"select","required":true}`)
	var p ConnActionParam
	if err := json.Unmarshal(old, &p); err != nil {
		t.Fatal(err)
	}
	if p.Name != "target" || p.Kind != "select" || p.Options != nil || p.Rows != 0 {
		t.Fatalf("旧格式解析错: %+v", p)
	}

	// 新插件的直接选项。
	cur := []byte(`{"name":"fmt","kind":"select","options":["json","csv"],"rows":4,"multiple":true,"help":"输出格式"}`)
	if err := json.Unmarshal(cur, &p); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.ParamOptions(), []string{"json", "csv"}) {
		t.Fatalf("options = %v", p.ParamOptions())
	}
	if p.Rows != 4 || !p.Multiple || p.Help != "输出格式" {
		t.Fatalf("新字段解析错: %+v", p)
	}
}

func TestParamOptionsLegacyJSONDefault(t *testing.T) {
	p := ConnActionParam{Kind: "select", Default: `["a","b"]`}
	if got := p.ParamOptions(); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("legacy options = %v", got)
	}
	// Default 不是 JSON 数组（普通默认值）——不当选项。
	p2 := ConnActionParam{Kind: "text", Default: "hello"}
	if got := p2.ParamOptions(); got != nil {
		t.Fatalf("普通默认值不应当选项: %v", got)
	}
}
