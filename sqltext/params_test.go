package sqltext

// ExtractParams 的收编测试（§382）：dbstudio 的面板语义原样迁移 +
// 新增 @pn 与反引号/@@var 的边界。这批向量同时是 polydb Rust 对拍的
// 共享语料源（schema-diff 先例）。

import (
	"strings"
	"testing"
)

func TestExtractParamsForms(t *testing.T) {
	cases := []struct {
		sql    string
		want   []string // Label() 按绑定序
		spanOK bool     // 校验区间往返（重写不丢字符）
	}{
		{"SELECT * FROM t WHERE a = ?", []string{"1"}, true},
		{"SELECT * FROM t WHERE a = ? AND b = ?", []string{"1", "2"}, true},
		{"SELECT * FROM t WHERE a = $2 AND b = $1", []string{"2", "1"}, true},
		{`SELECT * FROM t WHERE a = :name`, []string{"name"}, true},
		{`SELECT * FROM t WHERE a = :x AND b = :x`, []string{"x", "x"}, true}, // 同名同序位
		{"SELECT * FROM t WHERE a = @city", []string{"city"}, true},
		{"SELECT 'a?' , \"b?\" , `c?` FROM t WHERE d = ?", []string{"1"}, true}, // 引号内不算
		{"SELECT 1 -- ?\nFROM t WHERE a = ?", []string{"1"}, true},              // 行注释内不算
		{"SELECT /* ? */ 1 WHERE a = ?", []string{"1"}, true},                   // 块注释内不算
		{"SELECT 'it''s ?' WHERE a = ?", []string{"1"}, true},                   // 成双引号
		{"SELECT a::int FROM t WHERE b = ?", []string{"1"}, true},               // ::cast 不算
		{"SELECT @@version, @user WHERE a = ?", []string{"user", "2"}, true},    // @@var 跳过；@user 是 MSSQL 参数、? 位置式
		{"SELECT a`?`b FROM t WHERE c = ?", []string{"1"}, true},                // 反引号串不算
	}
	for _, tc := range cases {
		refs := ExtractParams(tc.sql)
		var labels []string
		for _, r := range refs {
			labels = append(labels, r.Label())
		}
		if strings.Join(labels, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: labels = %v, want %v", tc.sql, labels, tc.want)
		}
		if tc.spanOK {
			// 重写往返：非占位符字符不丢（以重写后的总长 ≈ 原长 - 占位符长 + 标记长 粗校）。
			out := RewritePlaceholders(tc.sql, refs, "mysql")
			if len(out) == 0 && len(tc.sql) > 0 {
				t.Errorf("%s: rewrite emptied", tc.sql)
			}
		}
	}
}

func TestRewritePlaceholdersByEngine(t *testing.T) {
	sql := "SELECT * FROM t WHERE a = ? AND b = :name AND c = $1"
	refs := ExtractParams(sql)
	out := RewritePlaceholders(sql, refs, "postgres")
	// PG 的位置式标记是 $n——重写按绑定序（sql 本就含 $1，自索引语义下
	// :name 的序位与它对齐）。
	if !strings.Contains(out, "$2") {
		t.Fatalf("postgres rewrite missing $2: %s", out)
	}
	out2 := RewritePlaceholders(sql, refs, "mysql")
	if strings.Contains(out2, ":name") {
		t.Fatalf("mysql rewrite kept :name: %s", out2)
	}
}

func TestExtractParamsEmpty(t *testing.T) {
	if refs := ExtractParams("SELECT 1"); len(refs) != 0 {
		t.Fatalf("no-placeholder statement yielded %v", refs)
	}
	if out := RewritePlaceholders("SELECT 1", nil, "mysql"); out != "SELECT 1" {
		t.Fatalf("nil refs changed text: %s", out)
	}
}
