// Package schemadiff compares two catalog snapshots and turns what differs into
// an ordered, selectable plan, then renders that plan as the DDL that would
// carry it out. It is the kernel's home for the comparison rules, so a client
// that wants "make B look like A" does not re-derive what counts as a
// difference.
//
// The package is pure: structs in, structs out, no connection and no execution.
// Snapshots come from Collect, which reads them through dbcore.SQLDriver, so
// built-in engines and plugin drivers are indistinguishable here. All DDL text
// is produced by dbcore/ddl — this package decides *what* changes, never *how*
// a statement is spelled.
//
// Two boundaries are deliberate and worth knowing before trusting a plan:
//
//   - Types are compared as normalized text, not as a cross-engine universal
//     type. Comparing PostgreSQL with MySQL will therefore report a real
//     difference as an alter_column for spellings that mean the same thing
//     ("timestamptz" vs "timestamp with time zone"). Same-engine comparisons,
//     which is what a sync is for, are unaffected.
//   - Column order, comments, identity/auto-increment flags and check
//     constraints are not compared. They would need engine-specific DDL that
//     dbcore/ddl deliberately does not invent.
package schemadiff

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/dbcore/dbcore"
)

// Op is one kind of change a plan can carry.
type Op string

const (
	OpCreateTable    Op = "create_table"
	OpDropTable      Op = "drop_table"
	OpAddColumn      Op = "add_column"
	OpDropColumn     Op = "drop_column"
	OpAlterColumn    Op = "alter_column"
	OpCreateIndex    Op = "create_index"
	OpDropIndex      Op = "drop_index"
	OpAddPrimaryKey  Op = "add_primary_key"
	OpDropPrimaryKey Op = "drop_primary_key"
	OpAddForeignKey  Op = "add_foreign_key"
	OpDropForeignKey Op = "drop_foreign_key"
)

// Execution phases. A plan is ordered by phase before it reaches the user, so
// the statement order is the one the engine needs: constraints that block a
// change come off first, tables appear before anything points at them, and
// columns settle before indexes and foreign keys are built over them.
const (
	PhaseDropForeignKey = 10
	PhaseDropIndex      = 20
	PhaseDropPrimaryKey = 25
	PhaseDropTable      = 30
	PhaseCreateTable    = 40
	PhaseDropColumn     = 50
	PhaseAddColumn      = 60
	PhaseAlterColumn    = 70
	PhaseAddPrimaryKey  = 75
	PhaseCreateIndex    = 80
	PhaseAddForeignKey  = 90
)

// Table is one base table of a snapshot: its columns, indexes and the foreign
// keys leaving it.
type Table struct {
	Name        string
	Columns     []dbcore.ColumnInfo
	Indexes     []dbcore.IndexInfo
	ForeignKeys []dbcore.ForeignKeyInfo
}

// Snapshot is one side of a comparison: every base table of one schema, read
// once. Views are not collected — they have no comparable column list and no
// sync target here.
type Snapshot struct {
	// Kind is the engine spelling ("postgresql", "mysql", ...) used for quoting
	// and for the per-engine DDL verbs.
	Kind   string
	Schema string
	Tables []Table
}

// Options narrows what counts as a difference.
type Options struct {
	// IgnoreDefaults drops DEFAULT from the column comparison. Useful when the
	// two sides name the same default differently (nextval(...) sequences).
	IgnoreDefaults bool
	// IgnoreNullability drops NOT NULL from the column comparison.
	IgnoreNullability bool
	// IgnoreIndexes leaves secondary indexes out of the plan entirely.
	IgnoreIndexes bool
	// IgnoreForeignKeys leaves foreign keys out of the plan entirely.
	IgnoreForeignKeys bool
}

// Entry is one change: what it is, where, and what it moves from and to. From
// and To are display strings, not machine input — the statements come from
// Script, which reads the snapshots again.
type Entry struct {
	// ID identifies the entry within one plan: "table/object/op".
	ID          string `json:"id"`
	Op          Op     `json:"op"`
	Table       string `json:"table"`
	Object      string `json:"object"`
	From        string `json:"from"`
	To          string `json:"to"`
	Phase       int    `json:"phase"`
	Reason      string `json:"reason"`
	Destructive bool   `json:"destructive"`
}

// Collect reads one schema's base tables with their columns, indexes and
// foreign keys through an already-open driver. Errors propagate rather than
// degrade to an empty list: a half-read snapshot would report every missing
// index or constraint as something to drop, which is worse than failing.
func Collect(ctx context.Context, drv dbcore.SQLDriver, schema string) (*Snapshot, error) {
	tables, err := drv.ListTables(ctx, schema)
	if err != nil {
		return nil, fmt.Errorf("schemadiff: list tables: %w", err)
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })

	snap := &Snapshot{Kind: string(drv.Kind()), Schema: schema, Tables: make([]Table, 0, len(tables))}
	for _, found := range tables {
		if found.Type == "view" || found.Type == "materialized_view" {
			continue
		}
		columns, err := drv.ListColumns(ctx, schema, found.Name)
		if err != nil {
			return nil, fmt.Errorf("schemadiff: list columns of %s: %w", found.Name, err)
		}
		indexes, err := drv.ListIndexes(ctx, schema, found.Name)
		if err != nil {
			return nil, fmt.Errorf("schemadiff: list indexes of %s: %w", found.Name, err)
		}
		foreignKeys, err := drv.ListForeignKeys(ctx, schema, found.Name)
		if err != nil {
			return nil, fmt.Errorf("schemadiff: list foreign keys of %s: %w", found.Name, err)
		}
		sort.Slice(indexes, func(i, j int) bool { return indexes[i].Name < indexes[j].Name })
		sort.Slice(foreignKeys, func(i, j int) bool { return foreignKeys[i].Name < foreignKeys[j].Name })
		snap.Tables = append(snap.Tables, Table{
			Name:        found.Name,
			Columns:     columns,
			Indexes:     indexes,
			ForeignKeys: foreignKeys,
		})
	}
	return snap, nil
}

// Diff answers "what must happen to target for it to look like source". The
// entries come back ordered by execution phase, then by table and object, so
// two runs over unchanged schemas produce byte-identical plans.
func Diff(source, target *Snapshot, opts Options) []Entry {
	var entries []Entry
	if source == nil || target == nil {
		return entries
	}

	sourceTables := map[string]Table{}
	for _, t := range source.Tables {
		sourceTables[fold(t.Name)] = t
	}
	targetTables := map[string]Table{}
	for _, t := range target.Tables {
		targetTables[fold(t.Name)] = t
	}

	for _, t := range source.Tables {
		key := fold(t.Name)
		other, ok := targetTables[key]
		if !ok {
			// A table the target lacks is created by one statement carrying its
			// columns and primary key; its secondary indexes and foreign keys
			// are separate entries, because they can only be built once the
			// table exists (and, for a foreign key, once its target does).
			entries = append(entries, Entry{
				ID:     entryID(entries, t.Name, "", OpCreateTable),
				Op:     OpCreateTable,
				Table:  t.Name,
				Reason: fmt.Sprintf("目标库缺少表 %s（%d 列）", t.Name, len(t.Columns)),
				Phase:  PhaseCreateTable,
			})
			empty := Table{Name: t.Name}
			if !opts.IgnoreIndexes {
				entries = append(entries, diffIndexes(t, empty)...)
			}
			if !opts.IgnoreForeignKeys {
				entries = append(entries, diffForeignKeys(t, empty)...)
			}
			continue
		}
		entries = append(entries, diffTable(source.Kind, t, other, opts)...)
	}

	for _, t := range target.Tables {
		if _, ok := sourceTables[fold(t.Name)]; ok {
			continue
		}
		entries = append(entries, Entry{
			ID:          entryID(entries, t.Name, "", OpDropTable),
			Op:          OpDropTable,
			Table:       t.Name,
			Reason:      fmt.Sprintf("源库没有表 %s", t.Name),
			Phase:       PhaseDropTable,
			Destructive: true,
		})
	}

	sortEntries(entries)
	return entries
}

// diffTable compares two tables that exist on both sides. kind is the engine the
// snapshots came from; it selects how a default is normalized.
func diffTable(kind string, source, target Table, opts Options) []Entry {
	var entries []Entry

	sourceColumns := map[string]dbcore.ColumnInfo{}
	for _, c := range source.Columns {
		sourceColumns[fold(c.Name)] = c
	}
	targetColumns := map[string]dbcore.ColumnInfo{}
	for _, c := range target.Columns {
		targetColumns[fold(c.Name)] = c
	}

	for _, c := range source.Columns {
		other, ok := targetColumns[fold(c.Name)]
		if !ok {
			entries = append(entries, Entry{
				ID:     entryID(entries, source.Name, c.Name, OpAddColumn),
				Op:     OpAddColumn,
				Table:  source.Name,
				Object: c.Name,
				To:     columnSpec(c),
				Reason: fmt.Sprintf("目标表缺少列 %s", c.Name),
				Phase:  PhaseAddColumn,
			})
			continue
		}
		if changed, from, to := columnDiff(kind, other, c, opts); changed {
			entries = append(entries, Entry{
				ID:          entryID(entries, source.Name, c.Name, OpAlterColumn),
				Op:          OpAlterColumn,
				Table:       source.Name,
				Object:      c.Name,
				From:        from,
				To:          to,
				Reason:      fmt.Sprintf("列 %s 定义不同", c.Name),
				Phase:       PhaseAlterColumn,
				Destructive: true, // narrowing a type can lose data; the UI confirms
			})
		}
	}
	for _, c := range target.Columns {
		if _, ok := sourceColumns[fold(c.Name)]; ok {
			continue
		}
		entries = append(entries, Entry{
			ID:          entryID(entries, source.Name, c.Name, OpDropColumn),
			Op:          OpDropColumn,
			Table:       source.Name,
			Object:      c.Name,
			From:        columnSpec(c),
			Reason:      fmt.Sprintf("源表没有列 %s", c.Name),
			Phase:       PhaseDropColumn,
			Destructive: true,
		})
	}

	sourcePK := primaryKeyOf(source)
	targetPK := primaryKeyOf(target)
	switch {
	case sourcePK != nil && targetPK == nil:
		entries = append(entries, Entry{
			ID:     entryID(entries, source.Name, sourcePK.Name, OpAddPrimaryKey),
			Op:     OpAddPrimaryKey,
			Table:  source.Name,
			Object: sourcePK.Name,
			To:     strings.Join(sourcePK.Columns, ", "),
			Reason: "目标表缺少主键",
			Phase:  PhaseAddPrimaryKey,
		})
	case sourcePK == nil && targetPK != nil:
		entries = append(entries, Entry{
			ID:          entryID(entries, source.Name, targetPK.Name, OpDropPrimaryKey),
			Op:          OpDropPrimaryKey,
			Table:       source.Name,
			Object:      targetPK.Name,
			From:        strings.Join(targetPK.Columns, ", "),
			Reason:      "源表没有主键",
			Phase:       PhaseDropPrimaryKey,
			Destructive: true,
		})
	case sourcePK != nil && targetPK != nil && !sameColumns(sourcePK.Columns, targetPK.Columns):
		entries = append(entries,
			Entry{
				ID:          entryID(entries, source.Name, targetPK.Name, OpDropPrimaryKey),
				Op:          OpDropPrimaryKey,
				Table:       source.Name,
				Object:      targetPK.Name,
				From:        strings.Join(targetPK.Columns, ", "),
				Reason:      "主键列不同，需重建",
				Phase:       PhaseDropPrimaryKey,
				Destructive: true,
			},
			Entry{
				ID:     entryID(entries, source.Name, sourcePK.Name, OpAddPrimaryKey),
				Op:     OpAddPrimaryKey,
				Table:  source.Name,
				Object: sourcePK.Name,
				To:     strings.Join(sourcePK.Columns, ", "),
				Reason: "按源表重建主键",
				Phase:  PhaseAddPrimaryKey,
			},
		)
	}

	if !opts.IgnoreIndexes {
		entries = append(entries, diffIndexes(source, target)...)
	}
	if !opts.IgnoreForeignKeys {
		entries = append(entries, diffForeignKeys(source, target)...)
	}
	return entries
}

// diffIndexes compares secondary indexes only; a primary key is a table
// constraint and is handled above.
func diffIndexes(source, target Table) []Entry {
	var entries []Entry
	sourceIdx := map[string]dbcore.IndexInfo{}
	for _, i := range source.Indexes {
		if i.Primary {
			continue
		}
		sourceIdx[fold(i.Name)] = i
	}
	targetIdx := map[string]dbcore.IndexInfo{}
	for _, i := range target.Indexes {
		if i.Primary {
			continue
		}
		targetIdx[fold(i.Name)] = i
	}

	for _, i := range source.Indexes {
		if i.Primary {
			continue
		}
		other, ok := targetIdx[fold(i.Name)]
		if !ok {
			entries = append(entries, Entry{
				ID:     entryID(entries, source.Name, i.Name, OpCreateIndex),
				Op:     OpCreateIndex,
				Table:  source.Name,
				Object: i.Name,
				To:     indexSpec(i),
				Reason: fmt.Sprintf("目标表缺少索引 %s", i.Name),
				Phase:  PhaseCreateIndex,
			})
			continue
		}
		if other.Unique != i.Unique || !sameColumns(other.Columns, i.Columns) {
			entries = append(entries,
				Entry{
					ID:          entryID(entries, source.Name, other.Name, OpDropIndex),
					Op:          OpDropIndex,
					Table:       source.Name,
					Object:      other.Name,
					From:        indexSpec(other),
					Reason:      fmt.Sprintf("索引 %s 定义不同，需重建", i.Name),
					Phase:       PhaseDropIndex,
					Destructive: true,
				},
				Entry{
					ID:     entryID(entries, source.Name, i.Name, OpCreateIndex),
					Op:     OpCreateIndex,
					Table:  source.Name,
					Object: i.Name,
					To:     indexSpec(i),
					Reason: fmt.Sprintf("按源表重建索引 %s", i.Name),
					Phase:  PhaseCreateIndex,
				},
			)
		}
	}
	for _, i := range target.Indexes {
		if i.Primary {
			continue
		}
		if _, ok := sourceIdx[fold(i.Name)]; ok {
			continue
		}
		entries = append(entries, Entry{
			ID:          entryID(entries, source.Name, i.Name, OpDropIndex),
			Op:          OpDropIndex,
			Table:       source.Name,
			Object:      i.Name,
			From:        indexSpec(i),
			Reason:      fmt.Sprintf("源表没有索引 %s", i.Name),
			Phase:       PhaseDropIndex,
			Destructive: true,
		})
	}
	return entries
}

// diffForeignKeys compares foreign keys by constraint name, which is their
// identity in every engine's catalog.
func diffForeignKeys(source, target Table) []Entry {
	var entries []Entry
	sourceFK := map[string]dbcore.ForeignKeyInfo{}
	for _, fk := range source.ForeignKeys {
		sourceFK[fold(fk.Name)] = fk
	}
	targetFK := map[string]dbcore.ForeignKeyInfo{}
	for _, fk := range target.ForeignKeys {
		targetFK[fold(fk.Name)] = fk
	}

	for _, fk := range source.ForeignKeys {
		other, ok := targetFK[fold(fk.Name)]
		if !ok {
			entries = append(entries, Entry{
				ID:     entryID(entries, source.Name, fk.Name, OpAddForeignKey),
				Op:     OpAddForeignKey,
				Table:  source.Name,
				Object: fk.Name,
				To:     fkSpec(fk),
				Reason: fmt.Sprintf("目标表缺少外键 %s", fk.Name),
				Phase:  PhaseAddForeignKey,
			})
			continue
		}
		if fkSpec(other) != fkSpec(fk) {
			entries = append(entries,
				Entry{
					ID:          entryID(entries, source.Name, other.Name, OpDropForeignKey),
					Op:          OpDropForeignKey,
					Table:       source.Name,
					Object:      other.Name,
					From:        fkSpec(other),
					Reason:      fmt.Sprintf("外键 %s 定义不同，需重建", fk.Name),
					Phase:       PhaseDropForeignKey,
					Destructive: true,
				},
				Entry{
					ID:     entryID(entries, source.Name, fk.Name, OpAddForeignKey),
					Op:     OpAddForeignKey,
					Table:  source.Name,
					Object: fk.Name,
					To:     fkSpec(fk),
					Reason: fmt.Sprintf("按源表重建外键 %s", fk.Name),
					Phase:  PhaseAddForeignKey,
				},
			)
		}
	}
	for _, fk := range target.ForeignKeys {
		if _, ok := sourceFK[fold(fk.Name)]; ok {
			continue
		}
		entries = append(entries, Entry{
			ID:          entryID(entries, source.Name, fk.Name, OpDropForeignKey),
			Op:          OpDropForeignKey,
			Table:       source.Name,
			Object:      fk.Name,
			From:        fkSpec(fk),
			Reason:      fmt.Sprintf("源表没有外键 %s", fk.Name),
			Phase:       PhaseDropForeignKey,
			Destructive: true,
		})
	}
	return entries
}

// columnDiff reports whether two columns differ in a way the options still
// care about, with the two sides rendered for display.
func columnDiff(kind string, from, to dbcore.ColumnInfo, opts Options) (bool, string, string) {
	typeChanged := normType(from.Type) != normType(to.Type)
	nullChanged := !opts.IgnoreNullability && from.Nullable != to.Nullable
	defaultChanged := !opts.IgnoreDefaults && normDefault(kind, from.Default) != normDefault(kind, to.Default)
	if !typeChanged && !nullChanged && !defaultChanged {
		return false, "", ""
	}
	return true, columnSpec(from), columnSpec(to)
}

func sortEntries(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Phase != entries[j].Phase {
			return entries[i].Phase < entries[j].Phase
		}
		if entries[i].Table != entries[j].Table {
			return entries[i].Table < entries[j].Table
		}
		if entries[i].Object != entries[j].Object {
			return entries[i].Object < entries[j].Object
		}
		return entries[i].Op < entries[j].Op
	})
}

// entryID builds a stable identifier and de-duplicates it, because a rebuild
// emits two entries for the same object (drop + add).
func entryID(existing []Entry, table, object string, op Op) string {
	base := table + "/" + object + "/" + string(op)
	count := 0
	for _, e := range existing {
		if strings.HasPrefix(e.ID, base) {
			count++
		}
	}
	if count == 0 {
		return base
	}
	return fmt.Sprintf("%s#%d", base, count+1)
}

func primaryKeyOf(t Table) *dbcore.IndexInfo {
	for i := range t.Indexes {
		if t.Indexes[i].Primary {
			pk := t.Indexes[i]
			return &pk
		}
	}
	return nil
}

func columnSpec(c dbcore.ColumnInfo) string {
	spec := c.Type
	if !c.Nullable {
		spec += " NOT NULL"
	} else {
		spec += " NULL"
	}
	if value := normDefault("", c.Default); value != "" {
		spec += " DEFAULT " + value
	}
	return spec
}

func indexSpec(i dbcore.IndexInfo) string {
	if i.Unique {
		return "UNIQUE (" + strings.Join(i.Columns, ", ") + ")"
	}
	return "(" + strings.Join(i.Columns, ", ") + ")"
}

func fkSpec(fk dbcore.ForeignKeyInfo) string {
	spec := fmt.Sprintf("(%s) -> %s(%s)",
		strings.Join(fk.Columns, ", "), fk.RefTable, strings.Join(fk.RefColumns, ", "))
	if fk.OnDelete != "" {
		spec += " ON DELETE " + fk.OnDelete
	}
	if fk.OnUpdate != "" {
		spec += " ON UPDATE " + fk.OnUpdate
	}
	return spec
}

// normDefault reduces a column default to a comparable form: "no default" and a
// reported "NULL" both mean none, and the surrounding whitespace and quoting an
// engine adds must not read as a difference.
func normDefault(kind string, value *string) string {
	if value == nil {
		return ""
	}
	text := strings.TrimSpace(*value)
	text = strings.Trim(text, "'")
	if strings.EqualFold(text, "null") {
		return ""
	}
	return text
}

func normType(t string) string {
	return strings.ToLower(strings.Join(strings.Fields(t), " "))
}

func sameColumns(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !strings.EqualFold(a[i], b[i]) {
			return false
		}
	}
	return true
}

// fold is the identity key for catalog names. Engines disagree on case
// (Oracle folds to upper, MySQL's is filesystem-dependent), so a comparison that
// treated case as significant would report differences that do not exist.
func fold(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
