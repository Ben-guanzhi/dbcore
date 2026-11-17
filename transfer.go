package dbcore

// 数据传输（Navicat 的 Data Transfer）：把一张表的数据从源连接整表搬到
// 目标连接。
//
// 分层：内核负责**读取-变换-批量写入**的机械（分页拉取、按列名对齐、
// 组 BatchRequest、逐批 ApplyBatch、进度回调），客户端负责连接管理、参数
// UI 与确认闸门（与 ProcessLister/UserAdmin 的分层一致）。
//
// 列对齐规则：**按名匹配**。目标里没有的源列忽略（源有 extra 列是常态——
// 两边的 schema 本来就可能不同）；源里没有的目标**非空列**会让批次失败
// （由引擎报错，错误信息里有列名）。不猜类型：值按源驱动的原生类型带过去。
//
// 明确不含：DDL 迁移（表结构由 schemadiff 负责）、并行多线程（单连接顺序
// 批次足够 GUI 场景）、冲突处理策略（目标已有同主键的行时引擎报错——客户端
// 展示后让用户决定清表或跳过）。

import (
	"context"
	"fmt"
)

// TransferProgress 是一次进度回调：已传行数与当前批号。
type TransferProgress struct {
	Batch int
	Rows  int
	Err   error // 非 nil 时传输中止
}

// TransferSpec 描述一次传输。
type TransferSpec struct {
	Schema    string // 源 schema（目标同 schema 名——跨库传输由客户端开目标连接解决）
	Table     string
	BatchSize int   // 每批行数（0 = 默认 200）
	MaxRows   int64 // 0 = 整表；>0 = 只传前 N 行（预览/采样）
}

// 默认批大小：一次往返 200 行在 GUI 的进度节奏与单批体积间取平衡。
const transferBatchSize = 200

// TransferTable 把 src 的一张表的数据写入 dst 的同名表。
// onProgress 每 batch 调一次（可为 nil）；出错即中止并返回已传行数。
func TransferTable(ctx context.Context, src, dst SQLDriver, spec TransferSpec, onProgress func(TransferProgress)) (int64, error) {
	bw, ok := dst.(BatchWriter)
	if !ok {
		return 0, fmt.Errorf("目标驱动不支持批量写入（BatchWriter）")
	}
	batch := spec.BatchSize
	if batch <= 0 {
		batch = transferBatchSize
	}

	// 列对齐：以**源的第一页**的列为准，与目标列按名匹配——两边 schema 可能
	// 不同（同名表的列子集/超集都常见），位置对齐会把值塞进错误的列。
	page := 1
	first, err := src.BrowseRows(ctx, spec.Schema, spec.Table, &TableRowsRequest{
		Page: page, PageSize: batch, NoCount: true,
	})
	if err != nil {
		return 0, fmt.Errorf("读取源表: %w", err)
	}
	dstCols, err := dst.ListColumns(ctx, spec.Schema, spec.Table)
	if err != nil {
		return 0, fmt.Errorf("读取目标列: %w", err)
	}
	dstNames := make(map[string]bool, len(dstCols))
	for _, c := range dstCols {
		dstNames[stringsLower(c.Name)] = true
	}
	use := make([]ColumnInfo, 0, len(first.Columns))
	for _, c := range first.Columns {
		if dstNames[stringsLower(c.Name)] {
			use = append(use, c)
		}
	}
	if len(use) == 0 {
		return 0, fmt.Errorf("源表与目标表没有同名列")
	}

	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		req := BatchRequest{Table: spec.Table}
		for _, row := range first.Rows {
			ins := make(map[string]Value, len(use))
			for i, c := range use {
				if i < len(row) {
					ins[c.Name] = row[i]
				}
			}
			req.Inserts = append(req.Inserts, ins)
		}
		if len(req.Inserts) > 0 {
			if err := bw.ApplyBatch(ctx, req); err != nil {
				return total, fmt.Errorf("写入目标（已传 %d 行）: %w", total, err)
			}
			total += int64(len(req.Inserts))
		}
		if onProgress != nil {
			onProgress(TransferProgress{Batch: page, Rows: int(total)})
		}
		if len(first.Rows) < batch { // 最后一页
			return total, nil
		}
		if spec.MaxRows > 0 && total >= spec.MaxRows {
			return total, nil
		}
		page++
		first, err = src.BrowseRows(ctx, spec.Schema, spec.Table, &TableRowsRequest{
			Page: page, PageSize: batch, NoCount: true,
		})
		if err != nil {
			return total, fmt.Errorf("读取源表第 %d 页: %w", page, err)
		}
	}
}

// stringsLower 是 ASCII 小写（列名对齐用——引擎的列名大小写惯例不同，
// MySQL 表内一致、PG 小写、MSSQL 保原样，ASCII 折叠已覆盖对齐需求）。
func stringsLower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
