package schemadiff

import (
	"fmt"
	"strings"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/ddl"
	"github.com/dbcore/dbcore/sqltext"
)

// Statement is one executable line of a synchronization, tied back to the entry
// it came from so a client can report failures per change rather than per script.
type Statement struct {
	EntryID string `json:"entryId"`
	SQL     string `json:"sql"`
}

// ScriptResult is the rendered plan: the statements to run, in order, plus the
// changes that could not be rendered. A warning is never silently dropped — an
// engine that cannot express a change (SQLite altering a column) says so instead
// of emitting a statement that looks right and fails.
type ScriptResult struct {
	Statements []Statement `json:"statements"`
	Warnings   []string    `json:"warnings"`
}

// Script renders the selected entries as DDL for the target side: the kind and
// schema come from target (that is where the statements run), the definitions
// come from source (that is the shape being copied). Entries are expected in the
// order Diff produced them; Script preserves it.
func Script(source, target *Snapshot, entries []Entry) ScriptResult {
	result := ScriptResult{}
	if source == nil || target == nil {
		return result
	}
	kind := target.Kind
	schema := target.Schema

	seen := map[string]bool{}
	warn := func(format string, args ...any) {
		text := fmt.Sprintf(format, args...)
		if seen[text] {
			return
		}
		seen[text] = true
		result.Warnings = append(result.Warnings, text)
	}

	for _, entry := range entries {
		sourceTable := tableNamed(source, entry.Table)
		if sourceTable == nil && entry.Op != OpDropTable {
			warn("源库中找不到表 %s，跳过 %s", entry.Table, entry.Op)
			continue
		}
		switch entry.Op {
		case OpCreateTable:
			result.Statements = append(result.Statements, Statement{entry.ID,
				ddl.CreateTable(kind, schema, sourceTable.Name,
					ddlColumns(sourceTable.Columns), nil, ddlPrimary(sourceTable), nil)})

		case OpDropTable:
			result.Statements = append(result.Statements, Statement{entry.ID,
				ddl.DropTable(kind, schema, entry.Table)})

		case OpAddColumn:
			column := columnNamed(sourceTable, entry.Object)
			if column == nil {
				warn("源表 %s 中找不到列 %s，跳过新增", entry.Table, entry.Object)
				continue
			}
			result.Statements = append(result.Statements, Statement{entry.ID,
				ddl.AddColumn(kind, schema, entry.Table, ddlColumn(*column))})

		case OpDropColumn:
			result.Statements = append(result.Statements, Statement{entry.ID,
				ddl.DropColumn(kind, schema, entry.Table, entry.Object)})

		case OpAlterColumn:
			if !ddl.CanAlterColumn(kind) {
				warn("%s 不支持原地修改列，表 %s 的列 %s 需重建整个表（本轮不自动生成）",
					sqltext.Normalize(kind), entry.Table, entry.Object)
				continue
			}
			from := columnNamed(tableNamed(target, entry.Table), entry.Object)
			to := columnNamed(sourceTable, entry.Object)
			if from == nil || to == nil {
				warn("找不到列 %s.%s 的一侧定义，跳过修改", entry.Table, entry.Object)
				continue
			}
			for _, sql := range ddl.AlterColumn(kind, schema, entry.Table, ddlColumn(*from), ddlColumn(*to)) {
				result.Statements = append(result.Statements, Statement{entry.ID, sql})
			}

		case OpCreateIndex:
			index := indexNamed(sourceTable, entry.Object)
			if index == nil {
				warn("源表 %s 中找不到索引 %s，跳过创建", entry.Table, entry.Object)
				continue
			}
			result.Statements = append(result.Statements, Statement{entry.ID,
				ddl.CreateIndex(kind, schema, entry.Table, ddlIndex(*index))})

		case OpDropIndex:
			result.Statements = append(result.Statements, Statement{entry.ID,
				ddl.DropIndex(kind, schema, entry.Table, entry.Object)})

		case OpAddPrimaryKey:
			pk := primaryKeyOf(*sourceTable)
			if pk == nil {
				warn("源表 %s 没有主键，跳过新增主键", entry.Table)
				continue
			}
			result.Statements = append(result.Statements, Statement{entry.ID,
				ddl.AddPrimaryKey(kind, schema, entry.Table, ddlIndex(*pk))})

		case OpDropPrimaryKey:
			sql := ddl.DropPrimaryKey(kind, schema, entry.Table, entry.Object)
			if sql == "" {
				warn("%s 不支持删除主键，表 %s 需重建（本轮不自动生成）",
					sqltext.Normalize(kind), entry.Table)
				continue
			}
			result.Statements = append(result.Statements, Statement{entry.ID, sql})

		case OpAddForeignKey:
			fk := foreignKeyNamed(sourceTable, entry.Object)
			if fk == nil {
				warn("源表 %s 中找不到外键 %s，跳过创建", entry.Table, entry.Object)
				continue
			}
			result.Statements = append(result.Statements, Statement{entry.ID,
				ddl.AddForeignKey(kind, schema, entry.Table, ddlForeignKey(*fk))})

		case OpDropForeignKey:
			sql := ddl.DropForeignKey(kind, schema, entry.Table, entry.Object)
			if sql == "" {
				warn("%s 不支持删除外键，表 %s 需重建（本轮不自动生成）",
					sqltext.Normalize(kind), entry.Table)
				continue
			}
			result.Statements = append(result.Statements, Statement{entry.ID, sql})

		default:
			warn("未识别的变更类型 %s", entry.Op)
		}
	}
	return result
}

func tableNamed(snap *Snapshot, name string) *Table {
	if snap == nil {
		return nil
	}
	for i := range snap.Tables {
		if fold(snap.Tables[i].Name) == fold(name) {
			return &snap.Tables[i]
		}
	}
	return nil
}

func columnNamed(t *Table, name string) *dbcore.ColumnInfo {
	if t == nil {
		return nil
	}
	for i := range t.Columns {
		if fold(t.Columns[i].Name) == fold(name) {
			return &t.Columns[i]
		}
	}
	return nil
}

func indexNamed(t *Table, name string) *dbcore.IndexInfo {
	if t == nil {
		return nil
	}
	for i := range t.Indexes {
		if fold(t.Indexes[i].Name) == fold(name) {
			return &t.Indexes[i]
		}
	}
	return nil
}

func foreignKeyNamed(t *Table, name string) *dbcore.ForeignKeyInfo {
	if t == nil {
		return nil
	}
	for i := range t.ForeignKeys {
		if fold(t.ForeignKeys[i].Name) == fold(name) {
			return &t.ForeignKeys[i]
		}
	}
	return nil
}

func ddlColumns(columns []dbcore.ColumnInfo) []ddl.Column {
	out := make([]ddl.Column, 0, len(columns))
	for _, c := range columns {
		out = append(out, ddlColumn(c))
	}
	return out
}

func ddlColumn(c dbcore.ColumnInfo) ddl.Column {
	return ddl.Column{
		Name:         c.Name,
		Type:         c.Type,
		Nullable:     c.Nullable,
		DefaultValue: normDefault("", c.Default),
	}
}

// ddlPrimary returns the table's primary key in the form CreateTable expects, so
// a table created by a sync carries the same constraint the source has.
func ddlPrimary(t *Table) []ddl.Index {
	pk := primaryKeyOf(*t)
	if pk == nil {
		return nil
	}
	out := ddlIndex(*pk)
	if strings.TrimSpace(out.Name) == "" {
		// SQLite reports its primary key with no constraint name, and an unnamed
		// CONSTRAINT clause is not renderable. Invent one rather than emit SQL
		// that cannot parse.
		out.Name = "pk_" + t.Name
	}
	return []ddl.Index{out}
}

func ddlIndex(i dbcore.IndexInfo) ddl.Index {
	return ddl.Index{
		Name:    i.Name,
		Unique:  i.Unique,
		Primary: i.Primary,
		Columns: i.Columns,
	}
}

func ddlForeignKey(fk dbcore.ForeignKeyInfo) ddl.ForeignKey {
	return ddl.ForeignKey{
		Name:       fk.Name,
		Columns:    fk.Columns,
		RefSchema:  fk.RefSchema,
		RefTable:   fk.RefTable,
		RefColumns: fk.RefColumns,
		OnDelete:   fk.OnDelete,
		OnUpdate:   fk.OnUpdate,
	}
}
