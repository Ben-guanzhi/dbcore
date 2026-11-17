package model

// 模型设计（Navicat 的 Model、MySQL Workbench 的 EER 模型）的正向工程内核：
// 实体关系模型 → 方言 DDL。
//
// 分层与查询构建器同构：**模型与 DDL 生成归内核**（纯逻辑、可测试），画布
// （拖拽编辑实体/关系）归客户端——模型是可序列化的 JSON（可编辑、可版本
// 控制、可存储为「设计文件」），画布只是编辑器。
//
// 覆盖：实体（表名/列/主键）+ 关系（一对多/多对多——多对多自动出关联表）。
// 明确不含：索引/唯一约束的可视化编辑（模型里留给 DDL 字段直填）、
// 分区/存储引擎等方言部署细节（Client 在生成后手改）。

import (
	"fmt"
	"strings"
)

// Entity 是模型里的一个实体（一张表）。
type Entity struct {
	Name       string   `json:"name"`
	Comment    string   `json:"comment,omitempty"`
	Columns    []Column `json:"columns"`
	PrimaryKey []string `json:"primaryKey"`
}

// Column 是模型里的一列。
type Column struct {
	Name     string `json:"name"`
	Type     string `json:"type"` // 方言中性的类型名（int/long/text/datetime/decimal/bool/uuid/blob）
	Nullable bool   `json:"nullable,omitempty"`
	Comment  string `json:"comment,omitempty"`
}

// Relation 描述两实体间的关系。
//
// Cardinality:
//
//	"1:N"  — 外键落在 target 侧（many 侧）；target 上加 source 的主键引用列
//	"N:M"  — 自动出关联表（两方的名字拼接，含两方主键引用）
type Relation struct {
	Name     string `json:"name,omitempty"`
	From     string `json:"from"`               // "1" 侧（N:M 时无意义，用顺序）
	To       string `json:"to"`                 // "N" 侧
	Card     string `json:"card"`               // "1:N" | "N:M"
	OnDelete string `json:"onDelete,omitempty"` // "CASCADE"|"SET NULL"|"RESTRICT"（默认 RESTRICT）
}

// Model 是一个完整的设计文件。
type Model struct {
	Name      string     `json:"name"`
	Dialect   string     `json:"dialect"` // mysql | postgres | sqlserver（标识符引用与类型映射）
	Entities  []Entity   `json:"entities"`
	Relations []Relation `json:"relations,omitempty"`
}

// Entity 按名取（不存在返回 false）。
func (m *Model) entity(name string) (*Entity, bool) {
	for i := range m.Entities {
		if strings.EqualFold(m.Entities[i].Name, name) {
			return &m.Entities[i], true
		}
	}
	return nil, false
}

// pkOf 实体的主键列名（无显式主键时用第一列——建模工具的常见宽容）。
func (m *Model) pkOf(e *Entity) []string {
	if len(e.PrimaryKey) > 0 {
		return e.PrimaryKey
	}
	if len(e.Columns) > 0 {
		return []string{e.Columns[0].Name}
	}
	return nil
}

// GenerateDDL 把模型生成建表语句（按依赖序：被引用的表先建；N:M 关联表
// 附在最后）。
//
// 生成是纯函数：同一模型同一 DDL。类型是方言中性的（int/long/text/datetime/
// decimal/bool/uuid/blob），由 mapType 映射到目标方言。
func (m *Model) GenerateDDL() (string, error) {
	q := dialectQuoteFor(m.Dialect)
	mt := typeMapFor(m.Dialect)

	// 依赖序：有关系的 target 在 source 之后建（外键需要父表先存在）。
	order := m.dependencyOrder()
	var b strings.Builder
	for _, name := range order {
		e, ok := m.entity(name)
		if !ok {
			continue
		}
		b.WriteString(m.tableDDL(e, mt, q))
		b.WriteString("\n")
	}
	// N:M 关联表。
	for _, r := range m.Relations {
		if r.Card != "N:M" {
			continue
		}
		b.WriteString(m.joinTableDDL(r, mt, q))
		b.WriteString("\n")
	}
	return b.String(), nil
}

// dependencyOrder 拓扑排序：1:N 关系的 from 在 to 之前（父表先建）。
// 环（自引用/循环外键）容忍——按输入顺序兜底，DDL 生成照常（引擎侧建表
// 失败会让用户看到，建模工具不假装能解环）。
func (m *Model) dependencyOrder() []string {
	seen := map[string]bool{}
	var out []string
	var visit func(name string)
	visit = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		// 先递归父表（当前实体是 1:N 的 to 时，from 是父）。
		for _, r := range m.Relations {
			if r.Card == "1:N" && strings.EqualFold(r.To, name) {
				visit(r.From)
			}
		}
		out = append(out, name)
	}
	for _, e := range m.Entities {
		visit(e.Name)
	}
	return out
}

// tableDDL 一张表的 CREATE TABLE。
func (m *Model) tableDDL(e *Entity, mt typeMapper, q quoter) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE %s (\n", q.ident(e.Name))
	pk := m.pkOf(e)
	pkSet := map[string]bool{}
	for _, k := range pk {
		pkSet[k] = true
	}
	for i, c := range e.Columns {
		comma := ","
		if i == len(e.Columns)-1 {
			comma = ""
		}
		fmt.Fprintf(&b, "  %s %s", q.ident(c.Name), mt.mapType(c.Type))
		if !c.Nullable {
			b.WriteString(" NOT NULL")
		}
		if pkSet[c.Name] {
			b.WriteString(" PRIMARY KEY")
		}
		if c.Comment != "" {
			fmt.Fprintf(&b, " COMMENT %s", q.literal(c.Comment))
		}
		b.WriteString(comma + "\n")
	}
	// 外键：所有 1:N 关系的 target 是本表时，加引用 from 的约束。
	fks := m.foreignKeysOf(e.Name)
	for _, f := range fks {
		// 外键前要给列区收尾的逗号（列循环按"最后一列"判过 comma 了）。
		b.WriteString(",\n")
		refs := make([]string, len(f.cols))
		parentRefs := make([]string, len(f.parentCols))
		for i := range f.cols {
			refs[i] = q.ident(f.cols[i])
			parentRefs[i] = q.ident(f.parentCols[i])
		}
		fmt.Fprintf(&b, "  FOREIGN KEY (%s) REFERENCES %s (%s)%s\n",
			strings.Join(refs, ", "), q.ident(f.parent),
			strings.Join(parentRefs, ", "), f.onDelete)
	}
	b.WriteString(");")
	return b.String()
}

// fk 是一个外键的渲染参数。
type fk struct {
	cols       []string // 本表侧的引用列
	parent     string
	parentCols []string
	onDelete   string // 已渲染的后缀（含前导空格）
}

// foreignKeysOf 收集所有 target 为表名的 1:N 关系（含从 source 主键推导的
// 引用列名——source 的 pk 列名直接沿用，模型侧不重命名）。
func (m *Model) foreignKeysOf(name string) []fk {
	var out []fk
	for _, r := range m.Relations {
		if r.Card != "1:N" || !strings.EqualFold(r.To, name) {
			continue
		}
		parent, ok := m.entity(r.From)
		if !ok {
			continue
		}
		pk := m.pkOf(parent)
		cols := make([]string, len(pk))
		for i, k := range pk {
			cols[i] = k
		}
		od := ""
		if r.OnDelete != "" {
			od = " ON DELETE " + r.OnDelete
		}
		out = append(out, fk{cols: cols, parent: r.From, parentCols: pk, onDelete: od})
	}
	return out
}

// joinTableDDL N:M 关系的关联表：两方主键引用 + 联合主键。
func (m *Model) joinTableDDL(r Relation, mt typeMapper, q quoter) string {
	a, okA := m.entity(r.From)
	bb, okB := m.entity(r.To)
	if !okA || !okB {
		return ""
	}
	ak, bk := m.pkOf(a), m.pkOf(bb)
	table := m.joinTableName(r)
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE %s (\n", q.ident(table))
	all := make([]string, 0, len(ak)+len(bk))
	for _, k := range ak {
		fmt.Fprintf(&b, "  %s %s NOT NULL,\n", q.ident(k), mt.mapType(typeOfCol(a, k)))
		all = append(all, q.ident(k))
	}
	for _, k := range bk {
		fmt.Fprintf(&b, "  %s %s NOT NULL,\n", q.ident(k), mt.mapType(typeOfCol(bb, k)))
		all = append(all, q.ident(k))
	}
	fmt.Fprintf(&b, "  PRIMARY KEY (%s)\n", strings.Join(all, ", "))
	fmt.Fprintf(&b, ");")
	return b.String()
}

// joinTableName 关联表名：两方按字母序拼接（确定性——不依赖用户输入顺序）。
func (m *Model) joinTableName(r Relation) string {
	a, b := r.From, r.To
	if strings.ToLower(a) > strings.ToLower(b) {
		a, b = b, a
	}
	return a + "_" + b
}

// typeOfCol 实体里一列的类型（找不到给 text——宽容）。
func typeOfCol(e *Entity, name string) string {
	for _, c := range e.Columns {
		if strings.EqualFold(c.Name, name) {
			return c.Type
		}
	}
	return "text"
}
