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
//   - An index's expression key parts, DESC flags, access method and
//     partial-index predicate are collected for display but deliberately not
//     compared. The statement renderer carries only the plain column list, so
//     a plan that tried to rebuild such an index would either report a
//     difference it cannot fix or silently create a *different* index. Such an
//     index is left out of the plan and reported as a warning instead.
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
	// Columns in one round trip when the driver offers the batch capability
	// (K-1): a snapshot over T tables otherwise costs one ListColumns per
	// table. The batch path must not weaken the error contract below — a
	// failed or partial batch falls back to per-table reads, where errors
	// propagate and abort the snapshot rather than degrading it.
	batchCols := map[string][]dbcore.ColumnInfo{}
	batchKnown := false
	if bc, ok := dbcore.AsBatchColumns(drv); ok {
		all, err := bc.BatchColumns(ctx, schema)
		if err != nil {
			// Degrade to the per-table loop; its own errors propagate.
			batchCols, batchKnown = nil, false
		} else {
			for _, c := range all {
				batchCols[c.Table] = append(batchCols[c.Table], c)
			}
			batchKnown = true
		}
	}
	// Foreign keys batch the same way (K-1.2). Unlike columns, an empty group
	// is meaningful here — a table with no foreign keys — so group presence is
	// tracked separately, and only a failed batch falls back to per-table.
	batchFK := map[string][]dbcore.ForeignKeyInfo{}
	batchFKKnown := false
	if bf, ok := dbcore.AsBatchForeignKeys(drv); ok {
		all, err := bf.BatchForeignKeys(ctx, schema)
		if err == nil {
			for _, grp := range all {
				batchFK[grp.Table] = grp.ForeignKeys
			}
			batchFKKnown = true
		}
	}
	for _, found := range tables {
		if found.Type == "view" || found.Type == "materialized_view" {
			continue
		}
		columns, ok := batchCols[found.Name]
		if !batchKnown || !ok || len(columns) == 0 {
			// Per-table path: also the backstop for a table the batch missed
			// (contract says it cannot happen; a real table always has
			// columns). Its error propagates — never a half-read snapshot.
			var err error
			columns, err = drv.ListColumns(ctx, schema, found.Name)
			if err != nil {
				return nil, fmt.Errorf("schemadiff: list columns of %s: %w", found.Name, err)
			}
		}
		indexes, err := drv.ListIndexes(ctx, schema, found.Name)
		if err != nil {
			return nil, fmt.Errorf("schemadiff: list indexes of %s: %w", found.Name, err)
		}
		var foreignKeys []dbcore.ForeignKeyInfo
		if batchFKKnown {
			// Batch path: the per-table group; absent = no foreign keys, a
			// real state, not a batch miss.
			foreignKeys = batchFK[found.Name]
		} else {
			var err error
			foreignKeys, err = drv.ListForeignKeys(ctx, schema, found.Name)
			if err != nil {
				return nil, fmt.Errorf("schemadiff: list foreign keys of %s: %w", found.Name, err)
			}
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
	// One de-dup counter shared by the whole pass: entryID counts how many
	// entries already carry a base ID, and the plan is built in one walk.
	ids := map[string]int{}

	sourceTables := newNameIndex(source.Tables, func(t Table) string { return t.Name })
	targetTables := newNameIndex(target.Tables, func(t Table) string { return t.Name })

	for _, t := range source.Tables {
		other, ok := matchName(sourceTables, targetTables, t.Name)
		if !ok {
			// A table the target lacks is created by one statement carrying its
			// columns and primary key; its secondary indexes and foreign keys
			// are separate entries, because they can only be built once the
			// table exists (and, for a foreign key, once its target does).
			entries = append(entries, Entry{
				ID:     entryID(ids, t.Name, "", OpCreateTable),
				Op:     OpCreateTable,
				Table:  t.Name,
				Reason: fmt.Sprintf("目标库缺少表 %s（%d 列）", t.Name, len(t.Columns)),
				Phase:  PhaseCreateTable,
			})
			empty := Table{Name: t.Name}
			if !opts.IgnoreIndexes {
				entries = append(entries, diffIndexes(ids, t, empty)...)
			}
			if !opts.IgnoreForeignKeys {
				entries = append(entries, diffForeignKeys(ids, t, empty)...)
			}
			continue
		}
		entries = append(entries, diffTable(ids, source.Kind, t, other, opts)...)
	}

	for _, t := range target.Tables {
		if _, ok := matchName(targetTables, sourceTables, t.Name); ok {
			continue
		}
		entries = append(entries, Entry{
			ID:          entryID(ids, t.Name, "", OpDropTable),
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
func diffTable(ids map[string]int, kind string, source, target Table, opts Options) []Entry {
	var entries []Entry

	sourceColumns := newNameIndex(source.Columns, func(c dbcore.ColumnInfo) string { return c.Name })
	targetColumns := newNameIndex(target.Columns, func(c dbcore.ColumnInfo) string { return c.Name })

	for _, c := range source.Columns {
		other, ok := matchName(sourceColumns, targetColumns, c.Name)
		if !ok {
			entries = append(entries, Entry{
				ID:     entryID(ids, source.Name, c.Name, OpAddColumn),
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
				ID:          entryID(ids, source.Name, c.Name, OpAlterColumn),
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
		if _, ok := matchName(targetColumns, sourceColumns, c.Name); ok {
			continue
		}
		entries = append(entries, Entry{
			ID:          entryID(ids, source.Name, c.Name, OpDropColumn),
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
			ID:     entryID(ids, source.Name, sourcePK.Name, OpAddPrimaryKey),
			Op:     OpAddPrimaryKey,
			Table:  source.Name,
			Object: sourcePK.Name,
			To:     strings.Join(sourcePK.Columns, ", "),
			Reason: "目标表缺少主键",
			Phase:  PhaseAddPrimaryKey,
		})
	case sourcePK == nil && targetPK != nil:
		entries = append(entries, Entry{
			ID:          entryID(ids, source.Name, targetPK.Name, OpDropPrimaryKey),
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
				ID:          entryID(ids, source.Name, targetPK.Name, OpDropPrimaryKey),
				Op:          OpDropPrimaryKey,
				Table:       source.Name,
				Object:      targetPK.Name,
				From:        strings.Join(targetPK.Columns, ", "),
				Reason:      "主键列不同，需重建",
				Phase:       PhaseDropPrimaryKey,
				Destructive: true,
			},
			Entry{
				ID:     entryID(ids, source.Name, sourcePK.Name, OpAddPrimaryKey),
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
		entries = append(entries, diffIndexes(ids, source, target)...)
	}
	if !opts.IgnoreForeignKeys {
		entries = append(entries, diffForeignKeys(ids, source, target)...)
	}
	return entries
}

// diffIndexes compares secondary indexes only; a primary key is a table
// constraint and is handled above.
//
// Only the plain column list is compared. An index whose definition carries
// something the statement renderer would drop — an expression key part, a DESC
// flag, a partial-index predicate — is left out of the plan entirely: reporting
// it would ask for a rebuild the renderer cannot carry out, and building it
// anyway would write a different index. Script names those indexes as warnings
// so the gap is visible rather than silent.
func diffIndexes(ids map[string]int, source, target Table) []Entry {
	var entries []Entry
	sourceIdx := newNameIndex(source.Indexes, func(i dbcore.IndexInfo) string { return i.Name })
	targetIdx := newNameIndex(target.Indexes, func(i dbcore.IndexInfo) string { return i.Name })

	for _, i := range source.Indexes {
		if i.Primary || !faithfulIndex(i) {
			continue
		}
		other, ok := matchName(sourceIdx, targetIdx, i.Name)
		if !ok || other.Primary || !faithfulIndex(other) {
			entries = append(entries, Entry{
				ID:     entryID(ids, source.Name, i.Name, OpCreateIndex),
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
					ID:          entryID(ids, source.Name, other.Name, OpDropIndex),
					Op:          OpDropIndex,
					Table:       source.Name,
					Object:      other.Name,
					From:        indexSpec(other),
					Reason:      fmt.Sprintf("索引 %s 定义不同，需重建", i.Name),
					Phase:       PhaseDropIndex,
					Destructive: true,
				},
				Entry{
					ID:     entryID(ids, source.Name, i.Name, OpCreateIndex),
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
		if i.Primary || !faithfulIndex(i) {
			continue
		}
		// A target index is dropped unless a faithful source index matches it.
		// An unfaithful source index (one the renderer cannot rebuild) does not
		// protect the target index: it is left out of the plan and named as a
		// warning instead.
		if other, ok := matchName(targetIdx, sourceIdx, i.Name); ok &&
			!other.Primary && faithfulIndex(other) {
			continue
		}
		entries = append(entries, Entry{
			ID:          entryID(ids, source.Name, i.Name, OpDropIndex),
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
func diffForeignKeys(ids map[string]int, source, target Table) []Entry {
	var entries []Entry
	sourceFK := newNameIndex(source.ForeignKeys, func(fk dbcore.ForeignKeyInfo) string { return fk.Name })
	targetFK := newNameIndex(target.ForeignKeys, func(fk dbcore.ForeignKeyInfo) string { return fk.Name })

	for _, fk := range source.ForeignKeys {
		other, ok := matchName(sourceFK, targetFK, fk.Name)
		if !ok {
			entries = append(entries, Entry{
				ID:     entryID(ids, source.Name, fk.Name, OpAddForeignKey),
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
					ID:          entryID(ids, source.Name, other.Name, OpDropForeignKey),
					Op:          OpDropForeignKey,
					Table:       source.Name,
					Object:      other.Name,
					From:        fkSpec(other),
					Reason:      fmt.Sprintf("外键 %s 定义不同，需重建", fk.Name),
					Phase:       PhaseDropForeignKey,
					Destructive: true,
				},
				Entry{
					ID:     entryID(ids, source.Name, fk.Name, OpAddForeignKey),
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
		if _, ok := matchName(targetFK, sourceFK, fk.Name); ok {
			continue
		}
		entries = append(entries, Entry{
			ID:          entryID(ids, source.Name, fk.Name, OpDropForeignKey),
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
// emits two entries for the same object (drop + add). ids carries the per-base
// counter across one Diff pass: scanning the grown plan for every new entry
// was Θ(n²) in plan size — millions of HasPrefix calls on a schema with
// thousands of columns, inside a user-facing compare click.
func entryID(ids map[string]int, table, object string, op Op) string {
	base := table + "/" + object + "/" + string(op)
	n := ids[base] + 1
	ids[base] = n
	if n == 1 {
		return base
	}
	return fmt.Sprintf("%s#%d", base, n)
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
	if value := defaultValue(c.Default); value != "" {
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

// faithfulIndex reports whether ddl.Index carries every part of this index
// definition. The renderer joins Columns with plain quoting, so it drops three
// things: expression key parts (IndexPart.Expression), DESC flags
// (IndexPart.Desc) and a partial-index predicate (IndexInfo.Predicate). An index
// with any of them would be rebuilt into a *different* index, so the comparison
// leaves it out of the plan and Script reports it instead.
func faithfulIndex(i dbcore.IndexInfo) bool {
	if i.Predicate != "" {
		return false
	}
	for _, p := range i.Parts {
		if p.Expression != "" || p.Desc {
			return false
		}
	}
	return true
}

// unfaithfulIndexWarnings names every secondary index the plan deliberately
// leaves alone, on both sides, so a user staring at an empty plan knows why the
// sync did not touch an index that is visibly there. The message says what the
// index carries rather than just "skipped".
func unfaithfulIndexWarnings(snap *Snapshot) []string {
	if snap == nil {
		return nil
	}
	var out []string
	for _, t := range snap.Tables {
		for _, i := range t.Indexes {
			if i.Primary || faithfulIndex(i) {
				continue
			}
			out = append(out, fmt.Sprintf("%s.%s 含表达式键/DESC/部分索引谓词，同步不重建该索引",
				t.Name, i.Name))
		}
	}
	return out
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

// noDefault is the sentinel normDefault returns for "no default" (nil or a
// reported "NULL"). It is distinct from "" so that an empty-string default
// (`DEFAULT ”`) is not confused with no default — both used to collapse to
// the same empty string after quote-trimming, which made the schema diff miss
// the change and the DDL renderer drop the DEFAULT clause.
const noDefault = "\x00"

// normDefault reduces a column default to a comparable form for schema diff.
// "no default" (nil or "NULL") maps to the noDefault sentinel; everything else
// has surrounding whitespace and one layer of quoting stripped so that
// `'hello'` and `hello` compare equal. The empty-string default `”` strips
// to "" which is distinct from noDefault — the two are no longer conflated.
func normDefault(kind string, value *string) string {
	if value == nil {
		return noDefault
	}
	text := strings.TrimSpace(*value)
	if strings.EqualFold(text, "null") {
		return noDefault
	}
	text = strings.Trim(text, "'")
	return text
}

// defaultValue returns the raw default value for DDL rendering. Unlike
// normDefault it does NOT strip quoting — the DDL generator needs the original
// form to render `DEFAULT ”` (empty string) and `DEFAULT 'hello'` correctly.
// Returns "" for no default (nil or "NULL").
func defaultValue(value *string) string {
	if value == nil {
		return ""
	}
	text := strings.TrimSpace(*value)
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

// nameIndex indexes a snapshot's objects for lookup by name.
//
// Engines disagree on case (Oracle folds unquoted identifiers to upper, MySQL's
// sensitivity is filesystem-dependent), so a source object is matched against a
// target object whose name folds equal. But the index is keyed by the raw name,
// never by the folded name alone: PostgreSQL, SQLite, SQL Server and Oracle all
// allow quoted identifiers that differ only in case, so `"T"` and `"t"` are two
// distinct tables that fold onto one key. Keying a map by the folded name made
// the second write overwrite the first, and with it the shadowed object's
// entire diff — source {T, t} against target {T} produced no entries at all.
type nameIndex[T any] struct {
	items     []T
	byName    map[string]int
	byFold    map[string][]int
	ambiguous map[string]bool // fold keys shared by two or more distinct raw names
}

func newNameIndex[T any](items []T, name func(T) string) nameIndex[T] {
	ix := nameIndex[T]{
		items:     items,
		byName:    make(map[string]int, len(items)),
		byFold:    make(map[string][]int, len(items)),
		ambiguous: map[string]bool{},
	}
	foldNames := make(map[string]map[string]bool, len(items))
	for i, it := range items {
		n := name(it)
		if _, dup := ix.byName[n]; !dup {
			ix.byName[n] = i
		}
		k := fold(n)
		if foldNames[k] == nil {
			foldNames[k] = map[string]bool{}
		}
		foldNames[k][n] = true
		ix.byFold[k] = append(ix.byFold[k], i)
	}
	for k, names := range foldNames {
		if len(names) > 1 {
			ix.ambiguous[k] = true
		}
	}
	return ix
}

// matchName finds the entry in ix corresponding to name. An exact raw-name
// match wins. Otherwise the name is matched case-insensitively only when the
// fold key names exactly one distinct raw name on each side; a fold key shared
// by two or more distinct raw names is ambiguous and reports a miss, so the
// caller emits an entry instead of collapsing one object onto another and
// silently dropping its diff.
func matchName[T any](self, ix nameIndex[T], name string) (T, bool) {
	var zero T
	if i, ok := ix.byName[name]; ok {
		return ix.items[i], true
	}
	k := fold(name)
	if self.ambiguous[k] || ix.ambiguous[k] {
		return zero, false
	}
	cands := ix.byFold[k]
	if len(cands) != 1 {
		return zero, false
	}
	return ix.items[cands[0]], true
}
