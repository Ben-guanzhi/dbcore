package dbcore_test

// 格式化大 SQL 的量化：用户高频操作（编辑后格式化 / 粘贴后格式化）。
// 用真实形状的 SQL（多表 JOIN + 子查询 + 表达式）。

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Ben-guanzhi/dbcore/sqlformat"
)

func benchBigSQL() string {
	// 模拟真实中等规模查询：5 表 JOIN + WHERE 表达式 + 分组 + 嵌套。
	parts := []string{
		"SELECT o.id, o.amount, u.name, i.qty, s.code",
		"FROM orders o",
		"JOIN users u ON u.id = o.user_id",
		"LEFT JOIN items i ON i.order_id = o.id",
		"LEFT JOIN shops s ON s.id = o.shop_id",
		"WHERE o.amount > 100 AND (o.status = 'paid' OR o.status = 'pending')",
		"  AND (u.deleted_at IS NULL OR u.deleted_at > now())",
		"GROUP BY o.id, o.amount, u.name, i.qty, s.code",
		"ORDER BY o.amount DESC, o.id",
		"LIMIT 50 OFFSET 0",
	}
	// 重复 20 条带表别名的查询，模拟大批量脚本。
	var b strings.Builder
	for i := 0; i < 20; i++ {
		b.WriteString(fmt.Sprintf("%s;\n", parts[0]))
		for _, p := range parts[1:] {
			b.WriteString(p + "\n")
		}
	}
	return b.String()
}

func BenchmarkFormatSQLLarge(b *testing.B) {
	sql := benchBigSQL()
	// 预检输入规模。
	b.Logf("input: %d bytes / %d statements", len(sql), 20)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sqlformat.Format(sql)
	}
}
