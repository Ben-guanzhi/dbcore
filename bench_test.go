package dbcore_test

// 性能基准（本工作区第一批）：把此前"看起来对"的性能判断换成数字。
// 三个内核热点：数据生成（纯 CPU）、查询构建器（纯 CPU）、结果物化
// （IO + 分配）。每个基准都用真实形状的输入，不测空循环。

import (
	"context"
	"fmt"
	"testing"

	"github.com/Ben-guanzhi/dbcore"
	"github.com/Ben-guanzhi/dbcore/sqlite"
	"github.com/Ben-guanzhi/dbcore/sqltext"
)

// 20 列的真实表形状：混类型（int/decimal/text/uuid/date），与 GUI 客户端
// 浏览的常见表一致。
func benchColumns() []dbcore.ColumnInfo {
	types := []string{"INT", "BIGINT", "DECIMAL(18,4)", "VARCHAR(120)", "TEXT", "UUID", "DATETIME", "BOOLEAN"}
	cols := make([]dbcore.ColumnInfo, 20)
	for i := range cols {
		cols[i] = dbcore.ColumnInfo{
			Name:         fmt.Sprintf("c%d", i),
			Type:         types[i%len(types)],
			IsPrimaryKey: i == 0,
		}
	}
	return cols
}

func BenchmarkGenerateRows1000x20(b *testing.B) {
	cols := benchColumns()
	opt := dbcore.GenOptions{Rows: 1000, Seed: 42}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows := dbcore.GenerateRows(cols, opt)
		if len(rows) != 1000 {
			b.Fatal("行数不对")
		}
	}
}

func BenchmarkGenerateInserts1000x20(b *testing.B) {
	cols := benchColumns()
	opt := dbcore.GenOptions{Rows: 1000, Seed: 42, Table: "bench", SkipPrimaryKey: true}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stmts := dbcore.GenerateInserts(cols, opt)
		if len(stmts) != 1000 {
			b.Fatal("语句数不对")
		}
	}
}

func BenchmarkBuildSelectJoins(b *testing.B) {
	spec := sqltext.SelectSpec{
		Table: "`orders`", Alias: "o",
		Joins: []sqltext.QBJoin{
			{Table: "`users`", Alias: "u", Type: "LEFT", On: []sqltext.QBCond{
				{Kind: "cmp", Col: "o.`user_id`", Op: "=", Value: "u.`id`"},
			}},
			{Table: "`items`", Alias: "i", Type: "INNER", On: []sqltext.QBCond{
				{Kind: "cmp", Col: "i.`order_id`", Op: "=", Value: "o.`id`"},
			}},
		},
		Columns: []sqltext.QBColumn{
			{Col: "u.`name`"}, {Col: "SUM(o.`amount`)", Agg: "SUM"},
		},
		Where:   []sqltext.QBCond{{Kind: "cmp", Col: "o.`amount`", Op: ">", Value: "100"}},
		GroupBy: []string{"u.`name`"}, Limit: 50,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sqltext.BuildSelect(spec)
	}
}

// 结果物化：SQLite 内存库里 20 列 × 5000 行，用 BrowseRows 一次拉一页。
func BenchmarkBrowseRows5000x20(b *testing.B) {
	drv, err := sqlite.OpenSpec(context.Background(),
		dbcore.ConnSpec{Kind: dbcore.KindSQLite, File: ":memory:"}, dbcore.PoolConfig{MaxOpen: 1})
	if err != nil {
		b.Fatal(err)
	}
	sql, ok := drv.AsSQL()
	if !ok {
		b.Fatal("no SQL")
	}
	ctx := context.Background()
	cols := benchColumns()
	defs := ""
	for _, c := range cols {
		defs += c.Name + " " + c.Type + ","
	}
	defs = defs[:len(defs)-1]
	if _, err := sql.Execute(ctx, "CREATE TABLE bench ("+defs+")"); err != nil {
		b.Fatal(err)
	}
	rows := dbcore.GenerateRows(cols, dbcore.GenOptions{Rows: 5000, Seed: 7, SkipPrimaryKey: true})
	for _, row := range rows {
		vals := ""
		for i, v := range row {
			if i > 0 {
				vals += ","
			}
			vals += sqltext.QuotedLiteral("sqlite", v)
		}
		// SkipPrimaryKey 让生成只有 19 列；补一个 NULL 给主键列以对齐表结构。
		vals += ",NULL"
		if _, err := sql.Execute(ctx, "INSERT INTO bench VALUES ("+vals+")"); err != nil {
			b.Fatal(err)
		}
	}
	req := &dbcore.TableRowsRequest{Page: 1, PageSize: 1000, NoCount: true}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := sql.BrowseRows(ctx, "", "bench", req)
		if err != nil || len(res.Rows) != 1000 {
			b.Fatalf("browse: %v rows=%d", err, len(res.Rows))
		}
	}
}
