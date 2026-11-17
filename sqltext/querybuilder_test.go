package sqltext

// 查询构建器的 SQL 生成测试：确定性、各子句、条件嵌套、方言中立。

import (
	"strings"
	"testing"
)

func TestBuildSelectSimple(t *testing.T) {
	got := BuildSelect(SelectSpec{
		Table: "`users`",
		Columns: []QBColumn{
			{Col: "`id`"}, {Col: "`name`", Alias: "`uname`"},
		},
		Where: []QBCond{
			{Kind: "cmp", Col: "`active`", Op: "=", Value: "1"},
		},
		Limit: 50,
	})
	want := "SELECT `id`, `name` AS `uname`\nFROM `users`\nWHERE `active` = 1\nLIMIT 50;"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestBuildSelectJoinsAndAggregates(t *testing.T) {
	got := BuildSelect(SelectSpec{
		Table: "`orders`", Alias: "o",
		Joins: []QBJoin{
			{Table: "`users`", Alias: "u", Type: "LEFT", On: []QBCond{
				{Kind: "cmp", Col: "o.`user_id`", Op: "=", Value: "u.`id`"},
			}},
		},
		Columns: []QBColumn{
			{Col: "u.`name`"},
			{Col: "o.`amount`", Agg: "SUM", Alias: "`total`"},
			{Col: "o.`id`", Agg: "COUNT", Distinct: true, Alias: "`cnt`"},
		},
		Where: []QBCond{
			{Kind: "cmp", Col: "o.`amount`", Op: ">", Value: "100"},
			{Kind: "or", Children: []QBCond{
				{Kind: "cmp", Col: "u.`name`", Op: "LIKE", Value: "'a%'"},
				{Kind: "cmp", Col: "u.`name`", Op: "IS NULL"},
			}},
		},
		GroupBy: []string{"u.`name`"},
		Having: []QBCond{
			{Kind: "cmp", Col: "SUM(o.`amount`)", Op: ">=", Value: "500"},
		},
		OrderBy: []QBOrder{{Col: "`total`", Desc: true}},
		Limit:   10,
	})
	for _, want := range []string{
		"SELECT u.`name`, SUM(o.`amount`) AS `total`, COUNT(DISTINCT o.`id`) AS `cnt`",
		"FROM `orders` o",
		"LEFT JOIN `users` u ON o.`user_id` = u.`id`",
		"WHERE o.`amount` > 100 AND (u.`name` LIKE 'a%' OR u.`name` IS NULL)",
		"GROUP BY u.`name`",
		"HAVING SUM(o.`amount`) >= 500",
		"ORDER BY `total` DESC",
		"LIMIT 10;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("缺少片段 %q:\n%s", want, got)
		}
	}
}

// 确定性：同一 spec 两次生成完全一致。
func TestBuildSelectIsDeterministic(t *testing.T) {
	spec := SelectSpec{Table: "`t`", Limit: 5, Offset: 5}
	a, b := BuildSelect(spec), BuildSelect(spec)
	if a != b {
		t.Fatalf("同一 spec 生成了不同 SQL:\n%s\n---\n%s", a, b)
	}
	if !strings.Contains(a, "LIMIT 5 OFFSET 5") {
		t.Errorf("OFFSET 未带上: %s", a)
	}
}

// 空投影 = SELECT *；无 JOIN 的 CROSS 未传 ON 不挂。
func TestBuildSelectDefaults(t *testing.T) {
	got := BuildSelect(SelectSpec{Table: "`t`"})
	if !strings.Contains(got, "SELECT *\nFROM `t`;") {
		t.Errorf("空投影应为 *: %s", got)
	}
	got2 := BuildSelect(SelectSpec{
		Table: "`a`",
		Joins: []QBJoin{{Table: "`b`", Type: "CROSS"}},
	})
	if !strings.Contains(got2, "CROSS JOIN `b`") || strings.Contains(got2, "ON") {
		t.Errorf("CROSS 不应有 ON: %s", got2)
	}
}
