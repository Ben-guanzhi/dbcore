package sqltext

// 可视化查询构建器（Navicat 的 View Builder、DBeaver 的 Visual Query Builder、
// Access 风格的拖拽查询）的数据模型与 SQL 生成：
//
// UI 是各客户端的事（拖拽画布/下拉拼条件），内核提供两件事——**查询的数据
// 模型**（可序列化、可存储为"构建的查询"）与**方言正确的 SQL 生成**。
// 生成是纯函数：同一 spec 永远产出同一 SQL（测试可复现，UI 可安全重生成）。
//
// 覆盖：多表 JOIN（INNER/LEFT/RIGHT/CROSS + ON 条件）、列投影（含聚合与
// 别名）、WHERE（AND/OR 组合的比较条件）、GROUP BY + HAVING、ORDER BY、
// LIMIT/OFFSET、DISTINCT。不支持子查询（UI 侧先拼内层再当表用是更简单的
// 组合方式）——边界写在这里。

import (
	"fmt"
	"strings"
)

// QBJoin 是一次表连接。
type QBJoin struct {
	Table string   // 被连接的表
	Alias string   // 别名（可空）
	Type  string   // "INNER" | "LEFT" | "RIGHT" | "CROSS"（默认 INNER）
	On    []QBCond // ON 条件（全部 AND 连接；CROSS 忽略）
}

// QBCond 是一个条件（比较或组合）。Kind: "cmp"|"and"|"or"|"raw"。
type QBCond struct {
	Kind string
	// cmp 字段
	Col   string // 列（可以是 "t.col" 限定形式）
	Op    string // "=", "<>", "!=", ">", "<", ">=", "<=", "LIKE", "NOT LIKE", "IN", "IS NULL", "IS NOT NULL"
	Value string // 字面量（客户端已按类型渲染：文本带引号、数字裸值）；IN 用逗号分隔
	// and/or 字段
	Children []QBCond
	// raw：调用方自带片段（含自己的转义责任——生成器原样拼）。
	Raw string
}

// QBColumn 是一个投影列。
type QBColumn struct {
	Col      string // "col" 或 "t.col"
	Agg      string // ""|"COUNT"|"SUM"|"AVG"|"MIN"|"MAX"（空 = 普通列）
	Alias    string // 可空
	Distinct bool   // 仅 COUNT DISTINCT 有意义
}

// QBOrder 是一个排序项。
type QBOrder struct {
	Col  string
	Desc bool
}

// SelectSpec 是一个查询的完整描述。
type SelectSpec struct {
	Table    string     // 主表
	Alias    string     // 主表别名（可空）
	Joins    []QBJoin   // 依序连接
	Columns  []QBColumn // 投影；空 = SELECT *
	Distinct bool
	Where    []QBCond // 全部 AND 连接
	GroupBy  []string // 列（限定形式）
	Having   []QBCond
	OrderBy  []QBOrder
	Limit    int // 0 = 不加
	Offset   int // 0 = 不加（Limit>0 才带）
}

// qbcCond 渲染一个条件（递归 and/or）。
func qbcCond(c QBCond) string {
	switch c.Kind {
	case "and", "or":
		parts := make([]string, 0, len(c.Children))
		for _, ch := range c.Children {
			parts = append(parts, qbcCond(ch))
		}
		if len(parts) == 0 {
			return ""
		}
		return "(" + strings.Join(parts, " "+strings.ToUpper(c.Kind)+" ") + ")"
	case "raw":
		return c.Raw
	default: // cmp
		op := strings.ToUpper(strings.TrimSpace(c.Op))
		if op == "" {
			op = "="
		}
		switch op {
		case "IS NULL", "IS NOT NULL":
			return c.Col + " " + op
		case "IN":
			return c.Col + " IN (" + c.Value + ")"
		default:
			return c.Col + " " + op + " " + c.Value
		}
	}
}

// BuildSelect 把 spec 生成 SELECT 语句（纯函数：同 spec 同 SQL）。
// 标识符引用交给调用方预引用（Col/Table/GroupBy 已是引用好的片段——UI 在
// 选列时即经 QuoteIdentLoose，生成器不重复判断），值的转义由客户端按类型
// 渲染（文本 QuotedLiteral、数字裸值）。
func BuildSelect(spec SelectSpec) string {
	var b strings.Builder
	b.WriteString("SELECT ")
	if spec.Distinct {
		b.WriteString("DISTINCT ")
	}
	// 列投影
	if len(spec.Columns) == 0 {
		b.WriteString("*")
	} else {
		parts := make([]string, 0, len(spec.Columns))
		for _, col := range spec.Columns {
			expr := col.Col
			if col.Agg != "" {
				inner := col.Col
				if col.Distinct {
					inner = "DISTINCT " + inner
				}
				expr = strings.ToUpper(col.Agg) + "(" + inner + ")"
			}
			if col.Alias != "" {
				expr += " AS " + col.Alias
			}
			parts = append(parts, expr)
		}
		b.WriteString(strings.Join(parts, ", "))
	}
	// FROM + JOIN
	b.WriteString("\nFROM " + spec.Table)
	if spec.Alias != "" {
		b.WriteString(" " + spec.Alias)
	}
	for _, j := range spec.Joins {
		jt := strings.ToUpper(strings.TrimSpace(j.Type))
		if jt == "" || (jt != "LEFT" && jt != "RIGHT" && jt != "CROSS") {
			jt = "INNER"
		}
		b.WriteString("\n  " + jt + " JOIN " + j.Table)
		if j.Alias != "" {
			b.WriteString(" " + j.Alias)
		}
		if jt != "CROSS" {
			conds := make([]string, 0, len(j.On))
			for _, c := range j.On {
				if s := qbcCond(c); s != "" {
					conds = append(conds, s)
				}
			}
			if len(conds) > 0 {
				b.WriteString(" ON " + strings.Join(conds, " AND "))
			}
		}
	}
	// WHERE
	if conds := qbcCondsAnd(spec.Where); conds != "" {
		b.WriteString("\nWHERE " + conds)
	}
	// GROUP BY + HAVING
	if len(spec.GroupBy) > 0 {
		b.WriteString("\nGROUP BY " + strings.Join(spec.GroupBy, ", "))
	}
	if conds := qbcCondsAnd(spec.Having); conds != "" && len(spec.GroupBy) > 0 {
		b.WriteString("\nHAVING " + conds)
	}
	// ORDER BY
	if len(spec.OrderBy) > 0 {
		parts := make([]string, 0, len(spec.OrderBy))
		for _, o := range spec.OrderBy {
			dir := "ASC"
			if o.Desc {
				dir = "DESC"
			}
			parts = append(parts, o.Col+" "+dir)
		}
		b.WriteString("\nORDER BY " + strings.Join(parts, ", "))
	}
	// LIMIT/OFFSET
	if spec.Limit > 0 {
		if spec.Offset > 0 {
			b.WriteString(fmt.Sprintf("\nLIMIT %d OFFSET %d", spec.Limit, spec.Offset))
		} else {
			b.WriteString(fmt.Sprintf("\nLIMIT %d", spec.Limit))
		}
	}
	b.WriteString(";")
	return b.String()
}

// qbcCondsAnd 把条件列表 AND 连接（空列表返回空串）。
func qbcCondsAnd(conds []QBCond) string {
	parts := make([]string, 0, len(conds))
	for _, c := range conds {
		if s := qbcCond(c); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " AND ")
}
