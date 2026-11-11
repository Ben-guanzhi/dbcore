package masking

import (
	"strings"
	"testing"
)

// 列名识别: 常见敏感列片段命中, 无关列放过, 大小写与分隔符不敏感。
func TestSensitiveNames(t *testing.T) {
	s := Default().Names
	for _, col := range []string{"email", "Email", "user_email", "e-mail", "MobileNo", "HOME_ADDRESS_PHONE", "api_key", "APIKEY", "bank_card_no", "password_hash"} {
		if !s.IsSensitive(col) {
			t.Errorf("%q 应被识别为敏感列", col)
		}
	}
	for _, col := range []string{"name", "address", "city", "created_at", "id", "note", "amount"} {
		if s.IsSensitive(col) {
			t.Errorf("%q 不应被识别为敏感列 (靠形态层兜底)", col)
		}
	}
}

// 形态识别: 邮箱/手机号/身份证/卡号各自保留可用形状。
func TestValueShapes(t *testing.T) {
	p := Default().Patterns
	cases := []struct{ in, want string }{
		// 邮箱: 保留 local 首字符 + 域名首字符 + TLD。
		{"alice@example.com", "a***@e**********.com"},
		{"13812345678", "138****5678"},
		{"110101199003077777", "11**************77"},
		{"4111111111111111", "4111********1111"},
		// 混在正文里也能命中。
		{"联系 13812345678 确认", "联系 138****5678 确认"},
		{"邮件发到 bob.smith@corp.io 吧", "邮件发到 b***@c******.io 吧"},
	}
	for _, c := range cases {
		if got := p.Value(c.in); got != c.want {
			t.Errorf("Value(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// 非敏感文本原样通过。
	if got := p.Value("这是一段普通备注"); got != "这是一段普通备注" {
		t.Errorf("普通文本不应被改: %q", got)
	}
	// 短数字串 (订单号 6 位) 不是卡号。
	if got := p.Value("订单 123456"); got != "订单 123456" {
		t.Errorf("短数字不应被当卡号: %q", got)
	}
}

// 行掩码: 敏感列整值遮罩, 形态层兜底普通列。
func TestRowString(t *testing.T) {
	pol := Default()
	cols := []string{"id", "email", "note", "password"}
	row := []string{"1", "alice@example.com", "手机 13812345678", "s3cret!"}
	got := pol.RowsString(cols, [][]string{row})[0]
	if got[0] != "1" {
		t.Errorf("id 列不应动: %q", got[0])
	}
	if got[1] != strings.Repeat("*", len("alice@example.com")) {
		t.Errorf("敏感列 email 应整值遮罩: %q", got[1])
	}
	if !strings.Contains(got[2], "138****5678") {
		t.Errorf("note 里的手机号应被形态层遮罩: %q", got[2])
	}
	if got[3] != "*******" {
		t.Errorf("password 应整值遮罩: %q", got[3])
	}
}

// 接口行掩码 (TableProGo 的 [][]interface{} 形态): nil 与非字符串原样通过。
func TestRowInterface(t *testing.T) {
	pol := Default()
	cols := []string{"id", "email", "score"}
	row := []interface{}{int64(7), "bob@corp.io", 3.14}
	got := pol.RowInterface(cols, row)
	if got[0] != int64(7) || got[2] != 3.14 {
		t.Errorf("非字符串不应动: %v", got)
	}
	if got[1] == "bob@corp.io" {
		t.Errorf("email 列应被遮罩: %v", got[1])
	}
	if s, _ := got[1].(string); !strings.HasPrefix(s, "*") {
		t.Errorf("遮罩结果应是星串: %q", s)
	}
}

// 自定义列名策略: 调用方可加内部黑名单片段。
func TestCustomSensitiveNames(t *testing.T) {
	s := NewSensitiveNames("project_code", "email")
	if !s.IsSensitive("PROJECT_CODE") || !s.IsSensitive("e-mail") {
		t.Fatal("自定义片段应命中")
	}
	if s.IsSensitive("phone") {
		t.Fatal("未列入的片段不应命中")
	}
}
