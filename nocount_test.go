package dbcore_test

// 懒计数（FNTDB ch15 / interactive analytics）：交互式表格不该为每一页付一次
// 全表 COUNT(*)。NoCount 让驱动跳过计数并把 total 留成"未知"，pager 已有未知态
// 的渲染。这里用一个最小的 SQLite 库钉住：同一个查询带不带 NoCount，行数相同、
// 但只有不带的时候才知道总数。

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/Ben-guanzhi/dbcore"
	"github.com/Ben-guanzhi/dbcore/sqlite"
)

func countTestDriver(t *testing.T, rows int) dbcore.SQLDriver {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := 0; i < rows; i++ {
		if _, err := db.Exec(`INSERT INTO t (v) VALUES (?)`, "x"); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	return sqlite.NewFromDB(db)
}

func TestBrowseRowsNoCountSkipsTotal(t *testing.T) {
	ctx := context.Background()
	drv := countTestDriver(t, 5)

	full, err := drv.BrowseRows(ctx, "", "t", &dbcore.TableRowsRequest{Page: 1, PageSize: 3})
	if err != nil {
		t.Fatalf("BrowseRows: %v", err)
	}
	if !full.TotalFound || full.Total != 5 {
		t.Errorf("counted browse: TotalFound=%v Total=%d, want true/5", full.TotalFound, full.Total)
	}

	lazy, err := drv.BrowseRows(ctx, "", "t", &dbcore.TableRowsRequest{Page: 1, PageSize: 3, NoCount: true})
	if err != nil {
		t.Fatalf("BrowseRows(NoCount): %v", err)
	}
	if lazy.TotalFound {
		t.Errorf("NoCount browse reported a total (%d); the point is to skip the count", lazy.Total)
	}
	if len(lazy.Rows) != len(full.Rows) {
		t.Errorf("NoCount changed the page: %d rows vs %d", len(lazy.Rows), len(full.Rows))
	}

	// The count is still available on demand, which is what the pager's explicit
	// "count" action uses.
	n, err := drv.BrowseRowsCount(ctx, "", "t", &dbcore.TableRowsRequest{Page: 1, PageSize: 3})
	if err != nil {
		t.Fatalf("BrowseRowsCount: %v", err)
	}
	if n != 5 {
		t.Errorf("on-demand count = %d, want 5", n)
	}
}
