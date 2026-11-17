package model

// UIModel 是客户端编辑器用的可变包装：在 Model 上补一组「编辑动作」方法
// （加列/删列/切主键/加删实体与关系），让 UI 不必直接操作切片下标。
// Model 本体保持纯数据（可序列化），动作都在这里。

import "strings"

// UIModel 嵌入一个 Model 提供编辑动作（嵌入让 u.Entities/u.Relations 直接
// 可用——包装指针的间接层只会让每个方法多一次解引用）。
type UIModel struct {
	*Model
}

// EntityNames 按输入顺序返回实体名（UI 的下拉清单）。
func (u *UIModel) EntityNames() []string {
	out := make([]string, 0, len(u.Entities))
	for _, e := range u.Entities {
		out = append(out, e.Name)
	}
	return out
}

// Entity 按名取（只读视角）。
func (u *UIModel) Entity(name string) (*Entity, bool) {
	return u.Model.entity(name)
}

// AddEntity 新建一个实体（自动带一个 long 主键列——建模工具的起点）。
func (u *UIModel) AddEntity(name string) {
	name = strings.TrimSpace(name)
	if name == "" || u.Model.existsEntity(name) {
		return
	}
	u.Entities = append(u.Entities, Entity{
		Name:       name,
		Columns:    []Column{{Name: "id", Type: "long"}},
		PrimaryKey: []string{"id"},
	})
}

// RemoveEntity 按名删除（引用它的关系一并删除——悬空关系没有意义）。
func (u *UIModel) RemoveEntity(name string) {
	for i := range u.Entities {
		if strings.EqualFold(u.Entities[i].Name, name) {
			u.Entities = append(u.Entities[:i], u.Entities[i+1:]...)
			kept := u.Relations[:0]
			for _, r := range u.Relations {
				if !strings.EqualFold(r.From, name) && !strings.EqualFold(r.To, name) {
					kept = append(kept, r)
				}
			}
			u.Relations = kept
			return
		}
	}
}

// AddColumn 给实体加一列（实体不存在则忽略）。
func (u *UIModel) AddColumn(entity string, c Column) {
	if e, ok := u.Model.entity(entity); ok {
		e.Columns = append(e.Columns, c)
	}
}

// RemoveColumn 按下标删列（并从主键里移除）。
func (u *UIModel) RemoveColumn(entity string, idx int) {
	e, ok := u.Model.entity(entity)
	if !ok || idx < 0 || idx >= len(e.Columns) {
		return
	}
	name := e.Columns[idx].Name
	e.Columns = append(e.Columns[:idx], e.Columns[idx+1:]...)
	pk := e.PrimaryKey[:0]
	for _, k := range e.PrimaryKey {
		if !strings.EqualFold(k, name) {
			pk = append(pk, k)
		}
	}
	e.PrimaryKey = pk
}

// TogglePK 把一列加入/移出主键。
func (u *UIModel) TogglePK(entity, col string) {
	e, ok := u.Model.entity(entity)
	if !ok {
		return
	}
	for i, k := range e.PrimaryKey {
		if strings.EqualFold(k, col) {
			e.PrimaryKey = append(e.PrimaryKey[:i], e.PrimaryKey[i+1:]...)
			return
		}
	}
	if u.Model.hasColumn(entity, col) {
		e.PrimaryKey = append(e.PrimaryKey, col)
	}
}

// IsPK 报告一列是否主键成员（UI 的 ★ 标记）。
func (u *UIModel) IsPK(entity, col string) bool {
	e, ok := u.Model.entity(entity)
	if !ok {
		return false
	}
	for _, k := range e.PrimaryKey {
		if strings.EqualFold(k, col) {
			return true
		}
	}
	return false
}

// AddRelation 追加一个关系（from/to 存在才加）。
func (u *UIModel) AddRelation(r Relation) {
	if u.Model.existsEntity(r.From) && u.Model.existsEntity(r.To) {
		u.Relations = append(u.Relations, r)
	}
}

// RemoveRelation 按下标删除。
func (u *UIModel) RemoveRelation(idx int) {
	if idx >= 0 && idx < len(u.Relations) {
		u.Relations = append(u.Relations[:idx], u.Relations[idx+1:]...)
	}
}

// existsEntity / hasColumn 是内部谓词。
func (u *UIModel) existsEntity(name string) bool {
	_, ok := u.Model.entity(name)
	return ok
}

func (u *UIModel) hasColumn(entity, col string) bool {
	e, ok := u.Model.entity(entity)
	if !ok {
		return false
	}
	for _, c := range e.Columns {
		if strings.EqualFold(c.Name, col) {
			return true
		}
	}
	return false
}
