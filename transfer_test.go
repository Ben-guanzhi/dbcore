package dbcore_test

// 数据传输的内核侧测试：sqlite 双连接（真实引擎往返）——列按名对齐、
// 分页拉取、进度回调。

import (
	"context"
	"strings"
	"testing"

	"github.com/Ben-guanzhi/dbcore"
	"github.com/Ben-guanzhi/dbcore/sqlite"
)

func TestTransferTableRoundTrip(t *testing.T) {
	ctx := context.Background()
	src := openTestSQLite(t, ":memory:")
	dst := openTestSQLite(t, ":memory:")

	for _, ddl := range []string{
		`CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)`,
		`INSERT INTO t (name) VALUES ('a'), ('b'), ('c'), ('d'), ('e')`,
	} {
		if _, err := src.Execute(ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	// 目标表少一列（name 对齐，extra 列忽略）。
	if _, err := dst.Execute(ctx, `CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT, extra TEXT)`); err != nil {
		t.Fatal(err)
	}

	var last dbcore.TransferProgress
	got, err := dbcore.TransferTable(ctx, src, dst,
		dbcore.TransferSpec{Table: "t", BatchSize: 2},
		func(p dbcore.TransferProgress) { last = p })
	if err != nil {
		t.Fatalf("TransferTable: %v", err)
	}
	if got != 5 {
		t.Fatalf("传了 %d 行, want 5", got)
	}
	if last.Rows != 5 || last.Batch != 3 {
		t.Errorf("进度不对: %+v（5 行 / 2 批大小 → 3 批）", last)
	}
	res, err := dst.Execute(ctx, "SELECT name FROM t ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 5 || res.Rows[0][0].String() != "a" || res.Rows[4][0].String() != "e" {
		t.Fatalf("目标行不对: %+v", res.Rows)
	}
}

// 源与目标没有同名列 → 明确报错（不静默传空）。
func TestTransferTableNoCommonColumns(t *testing.T) {
	ctx := context.Background()
	src := openTestSQLite(t, ":memory:")
	dst := openTestSQLite(t, ":memory:")
	if _, err := src.Execute(ctx, `CREATE TABLE t (a TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.Execute(ctx, `CREATE TABLE t (b TEXT)`); err != nil {
		t.Fatal(err)
	}
	_, err := dbcore.TransferTable(ctx, src, dst, dbcore.TransferSpec{Table: "t"}, nil)
	if err == nil || !strings.Contains(err.Error(), "没有同名列") {
		t.Fatalf("应报没有同名列: %v", err)
	}
}

// 目标不支持 BatchWriter → 明确报错。
func TestTransferTableDstNotWritable(t *testing.T) {
	ctx := context.Background()
	src := openTestSQLite(t, ":memory:")
	if _, err := src.Execute(ctx, `CREATE TABLE t (a TEXT)`); err != nil {
		t.Fatal(err)
	}
	_, err := dbcore.TransferTable(ctx, src, nil, dbcore.TransferSpec{Table: "t"}, nil)
	if err == nil {
		t.Fatal("nil dst 应报错")
	}
}

// openTestSQLite 打开一个内存库的驱动（测试 helper）。
func openTestSQLite(t *testing.T, dsn string) dbcore.SQLDriver {
	t.Helper()
	drv, err := sqlite.OpenSpec(ctxBackground(), dbcore.ConnSpec{Kind: dbcore.KindSQLite, File: dsn}, dbcore.PoolConfig{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sql, ok := drv.AsSQL()
	if !ok {
		t.Fatal("sqlite driver 没有 SQL 能力")
	}
	return sql
}

func ctxBackground() context.Context { return context.Background() }
