package dbcore

// 数据字典导出（DBeaver 的 "Generate ER Diagram/Documentation" 的文档半边）：
// 一个 schema 的表/列/主键/外键 → Markdown 数据字典。
//
// 产物是**人读的 Markdown**（表一节、列一表、PK/FK/注释齐备），用途是团队
// 文档/评审/交接——不是机器可读的 schema（那是 schemadiff 的 Snapshot）。
//
// 输入来自已有的 catalog 能力（ListTables/ListColumns/ListForeignKeys），
// 与客户端的树缓存同源——客户端把 app.db.SQL 交给 ExportDictionary 即可，
// 不需要新协议。

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// DictionarySource 是数据字典需要的 catalog 子集。SQLDriver 天然满足；
// 客户端不必为了导出字典实现整个 SQLDriver——把 Session 的目录能力包成
// 这三个方法即可（fynequery 的插件会话就是这样接入的）。
type DictionarySource interface {
	ListTables(ctx context.Context, schema string) ([]TableInfo, error)
	ListColumns(ctx context.Context, schema, table string) ([]ColumnInfo, error)
	ListForeignKeys(ctx context.Context, schema, table string) ([]ForeignKeyInfo, error)
}

// ExportDictionary 把一个 schema 的结构导出为 Markdown 数据字典。
// tables 为空 = 全部 base table。
func ExportDictionary(ctx context.Context, drv DictionarySource, schema string, tables []string) (string, error) {
	objs, err := drv.ListTables(ctx, schema)
	if err != nil {
		return "", fmt.Errorf("列出表: %w", err)
	}
	want := map[string]bool{}
	for _, t := range tables {
		want[strings.ToLower(t)] = true
	}

	names := make([]string, 0, len(objs))
	byName := map[string]TableInfo{}
	for _, o := range objs {
		if o.Type != "" && !strings.EqualFold(o.Type, "table") && !strings.EqualFold(o.Type, "BASE TABLE") {
			continue // 视图另有用途；字典只收基表
		}
		if len(want) > 0 && !want[strings.ToLower(o.Name)] {
			continue
		}
		names = append(names, o.Name)
		byName[o.Name] = o
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "", fmt.Errorf("schema %q 没有可导出的表", schema)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# 数据字典 · %s\n\n", schema)
	fmt.Fprintf(&b, "生成于 %s · %d 张表\n\n", time.Now().Format("2006-01-02"), len(names))
	fmt.Fprintf(&b, "| 表 | 说明 |\n|---|---|\n")
	for _, n := range names {
		comment := byName[n].Comment
		if comment == "" {
			comment = "—"
		}
		fmt.Fprintf(&b, "| [%s](#%s) | %s |\n", n, strings.ToLower(n), escapeCell(comment))
	}
	b.WriteString("\n")

	for _, n := range names {
		e := byName[n]
		cols, err := drv.ListColumns(ctx, schema, n)
		if err != nil {
			fmt.Fprintf(&b, "## %s\n\n> 读取列失败: %v\n\n", n, err)
			continue
		}
		fks, _ := drv.ListForeignKeys(ctx, schema, n)

		fmt.Fprintf(&b, "## %s\n\n", n)
		if e.Comment != "" {
			fmt.Fprintf(&b, "%s\n\n", e.Comment)
		}
		fmt.Fprintf(&b, "| 列 | 类型 | 可空 | 键 | 说明 |\n|---|---|---|---|---|\n")
		for _, c := range cols {
			key := ""
			if c.IsPrimaryKey {
				key = "PK"
			}
			for _, f := range fks {
				for _, fc := range f.Columns {
					if strings.EqualFold(fc, c.Name) {
						if key != "" {
							key += "/"
						}
						key += "FK→" + f.RefTable
					}
				}
			}
			null := "NO"
			if c.Nullable {
				null = "YES"
			}
			comment := c.Comment
			if comment == "" {
				comment = "—"
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n",
				escapeCell(c.Name), escapeCell(c.Type), null,
				escapeCell(key), escapeCell(comment))
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// escapeCell 表格单元格里不破坏 Markdown（管道与换行）。
func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ⏎ ")
	return s
}
