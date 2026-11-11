package sqlite

import (
	"context"
	"testing"
)

// P3-4 回归：ListIndexes 的错误路径必须传播，不能吞成 nil,nil。
//
// 旧实现：pragma_index_list 查询失败时返回 nil, nil——注释声称
// "pragma_index_list failing (e.g. view)" 但探测证明视图和不存在的表
// 都返回空结果集而非报错，错误路径只被真实错误（权限拒绝、数据库锁定、
// 取消的 context 等）触发。吞掉这些错误让 schemadiff 把"查不到索引"
// 当成"该删索引"，静默产出 DROP 计划。
//
// 修复：return nil, err——与 MySQL/Postgres/MSSQL/Oracle 驱动一致。
func TestListIndexesErrorPropagation(t *testing.T) {
	ctx := context.Background()
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	for _, ddl := range []string{
		`CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE INDEX ix ON t(name)`,
		`CREATE VIEW v AS SELECT id, name FROM t`,
	} {
		if _, err := d.Execute(ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}

	t.Run("view returns empty list without error", func(t *testing.T) {
		idx, err := d.ListIndexes(ctx, "main", "v")
		if err != nil {
			t.Fatalf("ListIndexes(view): err = %v, want nil (empty result, not error)", err)
		}
		if idx == nil {
			t.Errorf("ListIndexes(view): got nil slice, want empty non-nil slice")
		}
		if len(idx) != 0 {
			t.Errorf("ListIndexes(view): got %d indexes, want 0", len(idx))
		}
	})

	t.Run("nonexistent table returns empty list without error", func(t *testing.T) {
		idx, err := d.ListIndexes(ctx, "main", "nonexistent")
		if err != nil {
			t.Fatalf("ListIndexes(nonexistent): err = %v, want nil (empty result, not error)", err)
		}
		if idx == nil {
			t.Errorf("ListIndexes(nonexistent): got nil slice, want empty non-nil slice")
		}
		if len(idx) != 0 {
			t.Errorf("ListIndexes(nonexistent): got %d indexes, want 0", len(idx))
		}
	})

	t.Run("cancelled context propagates error", func(t *testing.T) {
		cancelCtx, cancel := context.WithCancel(context.Background())
		cancel() // cancel before the call

		idx, err := d.ListIndexes(cancelCtx, "main", "t")
		if err == nil {
			t.Fatalf("ListIndexes(cancelled ctx): err = nil, want error (must not swallow to nil,nil)")
		}
		if idx != nil {
			t.Errorf("ListIndexes(cancelled ctx): got %d indexes, want nil on error", len(idx))
		}
	})

	t.Run("normal table still works", func(t *testing.T) {
		idx, err := d.ListIndexes(ctx, "main", "t")
		if err != nil {
			t.Fatalf("ListIndexes(t): %v", err)
		}
		if len(idx) != 1 {
			t.Fatalf("ListIndexes(t): got %d indexes, want 1", len(idx))
		}
		if idx[0].Name != "ix" {
			t.Errorf("ListIndexes(t): name = %q, want ix", idx[0].Name)
		}
	})
}
