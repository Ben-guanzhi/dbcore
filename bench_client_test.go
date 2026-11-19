package dbcore_test

// 客户端侧量化：结果集到 JSON 的序列化（前端接收的是 JSON），以及
// 「每列折一次类型」的优化在 10k 行导出上的量化收益。

import (
	"encoding/json"
	"testing"

	"github.com/Ben-guanzhi/dbcore"
	"github.com/Ben-guanzhi/dbcore/sqltext"
)

// 5000×20 结果的 JSON 序列化（一个典型页量级）。
func BenchmarkResultJSONSerialize5000x20(b *testing.B) {
	cols := benchColumns()
	rows := dbcore.GenerateRows(cols, dbcore.GenOptions{Rows: 5000, Seed: 7})
	type result struct {
		Columns []struct{ Name string }
		Rows    [][]any
	}
	res := result{Columns: []struct{ Name string }{{Name: "c0"}, {Name: "c1"}}}
	for _, r := range rows {
		line := make([]any, len(r))
		for i, v := range r {
			line[i] = v
		}
		res.Rows = append(res.Rows, line)
	}
	payload, _ := json.Marshal(res)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(res)
	}
	b.Logf("payload: %d KB for 5000×20", len(payload)/1024)
}

// 量化「每列折类型 vs 每格折类型」（§314 注释断言：10k×5 省 5 万次——
// 实测差距是 2360 倍，见 WORKSPACE §315 记录）：类型判定（TypeIsNumeric）在 10k 行 × 5
// 类型表上的两种做法。表 ProGo 的 insertScript 注释断言每列折一次更省
// （10k×5 省 5 万次）——这里用数字证实。
func BenchmarkTypeFoldPerColumnVsPerCell(b *testing.B) {
	cols := benchColumns()
	rows := dbcore.GenerateRows(cols, dbcore.GenOptions{Rows: 10000, Seed: 9})

	b.Run("per-column", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			numeric := make([]bool, len(cols))
			for ci, c := range cols {
				numeric[ci] = sqltext.TypeIsNumeric(c.Type)
			}
			for range rows {
				_ = numeric
			}
		}
	})

	b.Run("per-cell", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for _, r := range rows {
				for ci := range r {
					_ = sqltext.TypeIsNumeric(cols[ci].Type)
				}
			}
		}
	})
}

func BenchmarkQuotedLiteralBatch(b *testing.B) {
	rows := dbcore.GenerateRows(benchColumns(), dbcore.GenOptions{Rows: 1000, Seed: 1})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, r := range rows {
			for _, v := range r {
				_ = sqltext.QuotedLiteral("sqlite", v)
			}
		}
	}
}
