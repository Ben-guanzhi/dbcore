package schemadiff

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/sqlite"
	"github.com/dbcore/dbcore/sqltext"
)

func column(name, typ string, nullable bool, def string) dbcore.ColumnInfo {
	c := dbcore.ColumnInfo{Name: name, Type: typ, Nullable: nullable}
	if def != "" {
		c.Default = &def
	}
	return c
}

func index(name string, unique, primary bool, cols ...string) dbcore.IndexInfo {
	return dbcore.IndexInfo{Name: name, Unique: unique, Primary: primary, Columns: cols}
}

func pkIndex(name string, cols ...string) dbcore.IndexInfo {
	return dbcore.IndexInfo{Name: name, Primary: true, Unique: true, Columns: cols}
}

func fk(name string, cols []string, ref string, refCols []string) dbcore.ForeignKeyInfo {
	return dbcore.ForeignKeyInfo{Name: name, Columns: cols, RefTable: ref, RefColumns: refCols}
}

func ops(entries []Entry) []Op {
	out := make([]Op, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Op)
	}
	return out
}

func TestDiffOrdersByExecutionPhase(t *testing.T) {
	source := &Snapshot{Kind: sqltext.Postgres, Schema: "public", Tables: []Table{
		{Name: "author", Columns: []dbcore.ColumnInfo{
			column("id", "bigint", false, ""), column("name", "text", true, ""),
		}, Indexes: []dbcore.IndexInfo{pkIndex("author_pkey", "id")}},
		{Name: "book", Columns: []dbcore.ColumnInfo{
			column("id", "bigint", false, ""), column("author_id", "bigint", true, ""),
			column("title", "text", true, ""),
		}, Indexes: []dbcore.IndexInfo{pkIndex("book_pkey", "id"), index("idx_book_title", false, false, "title")},
			ForeignKeys: []dbcore.ForeignKeyInfo{fk("book_author_fk", []string{"author_id"}, "author", []string{"id"})}},
	}}
	target := &Snapshot{Kind: sqltext.Postgres, Schema: "public", Tables: []Table{
		{Name: "author", Columns: []dbcore.ColumnInfo{column("id", "bigint", false, "")},
			Indexes: []dbcore.IndexInfo{pkIndex("author_pkey", "id")}},
		{Name: "stale", Columns: []dbcore.ColumnInfo{column("id", "bigint", false, "")}},
	}}

	entries := Diff(source, target, Options{})
	got := ops(entries)
	// drop_table (30) runs before create_table (40); the new table's index (80)
	// and foreign key (90) follow it, and the foreign key is last because it
	// points at a table that has to exist first.
	want := []Op{OpDropTable, OpCreateTable, OpAddColumn, OpCreateIndex, OpAddForeignKey}
	if len(got) != len(want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %s, want %s", i, got[i], want[i])
		}
	}

	for i := 1; i < len(entries); i++ {
		if entries[i-1].Phase > entries[i].Phase {
			t.Fatalf("entries not phase-ordered: %s(%d) before %s(%d)",
				entries[i-1].Op, entries[i-1].Phase, entries[i].Op, entries[i].Phase)
		}
	}
	if !entries[0].Destructive || entries[1].Destructive {
		t.Error("dropping a table must be destructive, creating one must not")
	}
}

func TestDiffSeesAColumnChangeAndOffersBothSides(t *testing.T) {
	source := &Snapshot{Kind: sqltext.MySQL, Schema: "app", Tables: []Table{{
		Name:    "t",
		Columns: []dbcore.ColumnInfo{column("a", "varchar(20)", true, "")},
	}}}
	target := &Snapshot{Kind: sqltext.MySQL, Schema: "app", Tables: []Table{{
		Name:    "t",
		Columns: []dbcore.ColumnInfo{column("a", "VARCHAR(10)", false, "")},
	}}}

	entries := Diff(source, target, Options{})
	if len(entries) != 1 || entries[0].Op != OpAlterColumn {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].From == "" || entries[0].To == "" {
		t.Error("an alter must show both sides so the user can judge the narrowing")
	}
	if !entries[0].Destructive {
		t.Error("a type change can lose data; it must be flagged")
	}

	if got := Diff(source, target, Options{IgnoreNullability: true}); len(got) != 1 {
		t.Errorf("with nullability ignored the type change must still show: %+v", got)
	}
	sameType := &Snapshot{Kind: sqltext.MySQL, Schema: "app", Tables: []Table{{
		Name:    "t",
		Columns: []dbcore.ColumnInfo{column("a", "varchar(20)", false, "")},
	}}}
	if got := Diff(source, sameType, Options{IgnoreNullability: true}); len(got) != 0 {
		t.Errorf("nullability-only difference must vanish when ignored: %+v", got)
	}
}

func TestDiffMatchesNamesCaseInsensitively(t *testing.T) {
	// Oracle folds identifiers to upper case; a comparison that treated case as
	// significant would report every column as both added and dropped.
	source := &Snapshot{Kind: sqltext.Oracle, Schema: "APP", Tables: []Table{{
		Name:    "T",
		Columns: []dbcore.ColumnInfo{column("ID", "number", false, "")},
	}}}
	target := &Snapshot{Kind: sqltext.Oracle, Schema: "APP", Tables: []Table{{
		Name:    "t",
		Columns: []dbcore.ColumnInfo{column("id", "number", false, "")},
	}}}
	if got := Diff(source, target, Options{}); len(got) != 0 {
		t.Errorf("case-only name differences must not be changes: %+v", got)
	}
}

func TestDiffRebuildsAnIndexThatChangedShape(t *testing.T) {
	source := &Snapshot{Kind: sqltext.Postgres, Schema: "public", Tables: []Table{{
		Name:    "t",
		Columns: []dbcore.ColumnInfo{column("a", "text", true, "")},
		Indexes: []dbcore.IndexInfo{index("i_t", false, false, "a", "id")},
	}}}
	target := &Snapshot{Kind: sqltext.Postgres, Schema: "public", Tables: []Table{{
		Name:    "t",
		Columns: []dbcore.ColumnInfo{column("a", "text", true, "")},
		Indexes: []dbcore.IndexInfo{index("i_t", false, false, "a")},
	}}}
	got := ops(Diff(source, target, Options{}))
	if len(got) != 2 || got[0] != OpDropIndex || got[1] != OpCreateIndex {
		t.Fatalf("a reshaped index must be dropped then rebuilt, got %v", got)
	}
	if got := Diff(source, target, Options{IgnoreIndexes: true}); len(got) != 0 {
		t.Errorf("IgnoreIndexes must suppress it: %+v", got)
	}
}

// An index carrying a partial-index predicate, an expression key part or a DESC
// flag is collected and displayed, but the DDL renderer joins the plain column
// list only: planning a rebuild would write a *different* index (the predicate
// gone, the expression quoted as an identifier, the sort order flattened). The
// comparison therefore leaves such an index out of the plan and the script names
// it, instead of silently recreating it wrong.
func TestDiffLeavesOutIndexesTheRendererCannotCarry(t *testing.T) {
	pred := index("i_pred", false, false, "a")
	pred.Predicate = "a > 0"
	expr := index("i_expr", false, false, "a")
	expr.Parts = []dbcore.IndexPart{{Expression: "lower(a)"}}
	desc := index("i_desc", false, false, "a")
	desc.Parts = []dbcore.IndexPart{{Column: "a", Desc: true}}

	source := &Snapshot{Kind: sqltext.Postgres, Schema: "public", Tables: []Table{{
		Name:    "t",
		Columns: []dbcore.ColumnInfo{column("a", "text", true, "")},
		Indexes: []dbcore.IndexInfo{pred, expr, desc},
	}}}
	target := &Snapshot{Kind: sqltext.Postgres, Schema: "public", Tables: []Table{{
		Name:    "t",
		Columns: []dbcore.ColumnInfo{column("a", "text", true, "")},
	}}}

	entries := Diff(source, target, Options{})
	for _, e := range entries {
		if e.Op == OpCreateIndex || e.Op == OpDropIndex {
			t.Errorf("an index the renderer cannot carry must not be planned: %+v", e)
		}
	}
	result := Script(source, target, entries)
	if len(result.Statements) != 0 {
		t.Errorf("statements = %+v, want none", result.Statements)
	}
	if len(result.Warnings) != 3 {
		t.Fatalf("warnings = %+v, want one per left-out index", result.Warnings)
	}
	for _, warn := range result.Warnings {
		if !strings.Contains(warn, "同步不重建该索引") {
			t.Errorf("warning does not name the boundary: %q", warn)
		}
	}
}

// The boundary is per index, not global: a plain index still plans and renders
// while an expression or partial one beside it is only named.
func TestDiffStillPlansTheIndexesItCanRender(t *testing.T) {
	plain := index("i_plain", false, false, "a")
	pred := index("i_pred", false, false, "a")
	pred.Predicate = "a > 0"
	source := &Snapshot{Kind: sqltext.Postgres, Schema: "public", Tables: []Table{{
		Name:    "t",
		Columns: []dbcore.ColumnInfo{column("a", "text", true, "")},
		Indexes: []dbcore.IndexInfo{plain, pred},
	}}}
	target := &Snapshot{Kind: sqltext.Postgres, Schema: "public", Tables: []Table{{
		Name:    "t",
		Columns: []dbcore.ColumnInfo{column("a", "text", true, "")},
	}}}

	entries := Diff(source, target, Options{})
	if len(entries) != 1 || entries[0].Op != OpCreateIndex || entries[0].Object != "i_plain" {
		t.Fatalf("entries = %+v, want one create for i_plain", entries)
	}
	result := Script(source, target, entries)
	if len(result.Statements) != 1 || !strings.Contains(result.Statements[0].SQL, "CREATE INDEX") {
		t.Fatalf("statements = %+v, want the plain index", result.Statements)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "i_pred") {
		t.Fatalf("warnings = %+v, want the left-out index named", result.Warnings)
	}
}

func TestScriptRendersTheSameChangePerEngine(t *testing.T) {
	source := &Snapshot{Kind: sqltext.Postgres, Schema: "public", Tables: []Table{{
		Name:    "t",
		Columns: []dbcore.ColumnInfo{column("id", "bigint", false, ""), column("a", "text", true, "")},
		Indexes: []dbcore.IndexInfo{pkIndex("t_pkey", "id")},
	}}}

	cases := []struct{ kind, wantCreate, wantAlter string }{
		{sqltext.Postgres, `CREATE TABLE "public"."t" (`, `ALTER TABLE "public"."t" ALTER COLUMN "a" TYPE text`},
		{sqltext.MySQL, "CREATE TABLE `public`.`t` (", "ALTER TABLE `public`.`t` MODIFY COLUMN `a` text"},
		{sqltext.SQLServer, "CREATE TABLE [public].[t] (", "ALTER TABLE [public].[t] ALTER COLUMN [a] text"},
	}
	for _, c := range cases {
		target := &Snapshot{Kind: c.kind, Schema: "public", Tables: []Table{{
			Name:    "t",
			Columns: []dbcore.ColumnInfo{column("id", "bigint", false, ""), column("a", "varchar(10)", true, "")},
			Indexes: []dbcore.IndexInfo{pkIndex("t_pkey", "id")},
		}}}
		entries := Diff(source, target, Options{})
		if len(entries) != 1 || entries[0].Op != OpAlterColumn {
			t.Fatalf("%s: entries = %+v", c.kind, entries)
		}
		result := Script(source, target, entries)
		if len(result.Statements) != 1 {
			t.Fatalf("%s: statements = %+v (%v)", c.kind, result.Statements, result.Warnings)
		}
		if got := result.Statements[0].SQL; !contains(got, c.wantAlter) {
			t.Errorf("%s: alter = %q, want it to contain %q", c.kind, got, c.wantAlter)
		}
		if result.Statements[0].EntryID != entries[0].ID {
			t.Errorf("%s: statement not tied to its entry", c.kind)
		}
	}

	// A table the target lacks is created with its primary key in one statement.
	target := &Snapshot{Kind: sqltext.Postgres, Schema: "public"}
	entries := Diff(source, target, Options{})
	result := Script(source, target, entries)
	if len(result.Statements) != 1 {
		t.Fatalf("statements = %+v (%v)", result.Statements, result.Warnings)
	}
	if got := result.Statements[0].SQL; !contains(got, `CREATE TABLE "public"."t" (`) ||
		!contains(got, `PRIMARY KEY ("id")`) {
		t.Errorf("create = %q", got)
	}
}

func TestScriptWarnsInsteadOfGuessing(t *testing.T) {
	// SQLite cannot alter a column, drop a primary key or drop a foreign key.
	// Emitting a plausible-looking statement would fail at execution time, so
	// the plan says what it cannot do.
	source := &Snapshot{Kind: sqltext.SQLite, Schema: "main", Tables: []Table{{
		Name:    "t",
		Columns: []dbcore.ColumnInfo{column("a", "varchar(3)", true, "")},
	}}}
	if got := Diff(source, source, Options{}); len(got) != 0 {
		t.Fatalf("identical snapshots must produce no entries: %+v", got)
	}

	target := &Snapshot{Kind: sqltext.SQLite, Schema: "main", Tables: []Table{{
		Name:        "t",
		Columns:     []dbcore.ColumnInfo{column("a", "text", false, "")},
		Indexes:     []dbcore.IndexInfo{pkIndex("pk_t", "a")},
		ForeignKeys: []dbcore.ForeignKeyInfo{fk("gone", []string{"a"}, "other", []string{"id"})},
	}}}
	entries := Diff(source, target, Options{})
	result := Script(source, target, entries)
	if len(result.Statements) != 0 {
		t.Errorf("sqlite must not render an ALTER COLUMN: %+v", result.Statements)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("the skipped change must be reported, not swallowed")
	}
}

func TestScriptSkipsEntriesItCannotResolve(t *testing.T) {
	source := &Snapshot{Kind: sqltext.Postgres, Schema: "public", Tables: []Table{{
		Name: "t", Columns: []dbcore.ColumnInfo{column("a", "text", true, "")},
	}}}
	target := &Snapshot{Kind: sqltext.Postgres, Schema: "public"}
	entries := []Entry{{ID: "t/ghost/add_column", Op: OpAddColumn, Table: "t", Object: "ghost", Phase: PhaseAddColumn}}
	result := Script(source, target, entries)
	if len(result.Statements) != 0 || len(result.Warnings) != 1 {
		t.Fatalf("statements = %+v warnings = %+v", result.Statements, result.Warnings)
	}
}

// TestCollectAndDiffOnRealSQLite drives the whole rule layer against two real
// SQLite files: read both, plan the sync, run it, then read the target again and
// assert the plan is empty. Foreign keys are excluded because SQLite can only
// create them as part of CREATE TABLE, which a sync of an existing table cannot
// do — TestScriptWarnsInsteadOfGuessing covers that boundary.
func TestCollectAndDiffOnRealSQLite(t *testing.T) {
	dir := t.TempDir()
	open := func(name string) *sqlite.SQLiteDriver {
		drv, err := sqlite.Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		t.Cleanup(func() { _ = drv.Close() })
		return drv
	}
	exec := func(drv *sqlite.SQLiteDriver, statements ...string) {
		for _, s := range statements {
			if _, err := drv.Execute(context.Background(), s); err != nil {
				t.Fatalf("exec %q: %v", s, err)
			}
		}
	}

	source := open("source.db")
	exec(source,
		`CREATE TABLE author (id integer primary key, name text)`,
		`CREATE TABLE book (id integer primary key, title text, author_id integer)`,
		`CREATE INDEX idx_book_title ON book (title)`,
	)
	target := open("target.db")
	exec(target,
		`CREATE TABLE author (id integer primary key)`,
		`CREATE TABLE stale (id integer primary key)`,
	)

	ctx := context.Background()
	snapshotSource, err := Collect(ctx, source, "main")
	if err != nil {
		t.Fatalf("collect source: %v", err)
	}
	snapshotTarget, err := Collect(ctx, target, "main")
	if err != nil {
		t.Fatalf("collect target: %v", err)
	}
	if len(snapshotSource.Tables) != 2 {
		t.Fatalf("source tables = %+v", snapshotSource.Tables)
	}

	entries := Diff(snapshotSource, snapshotTarget, Options{IgnoreForeignKeys: true})
	if len(entries) == 0 {
		t.Fatal("two different files must produce a plan")
	}
	for _, e := range entries {
		if e.Op == OpAlterColumn {
			t.Errorf("unexpected alter on %s.%s — the fixtures must stay inside what SQLite can apply",
				e.Table, e.Object)
		}
	}

	result := Script(snapshotSource, snapshotTarget, entries)
	for _, st := range result.Statements {
		if _, err := target.Execute(ctx, st.SQL); err != nil {
			t.Fatalf("apply %q: %v", st.SQL, err)
		}
	}

	after, err := Collect(ctx, target, "main")
	if err != nil {
		t.Fatalf("re-collect: %v", err)
	}
	if remaining := Diff(snapshotSource, after, Options{IgnoreForeignKeys: true}); len(remaining) != 0 {
		t.Fatalf("sync did not converge, remaining: %+v", remaining)
	}
}

// TestCollectAndDiffLeavesPartialIndexAlone pins the boundary against a real
// engine: SQLite reports an expression key part and a WHERE predicate through
// ListIndexes, but the statement renderer cannot carry them, so a sync must leave
// the index alone and say so rather than rebuild it without them. The second
// comparison must be empty — the boundary is not a source of drift.
func TestCollectAndDiffLeavesPartialIndexAlone(t *testing.T) {
	dir := t.TempDir()
	open := func(name string) *sqlite.SQLiteDriver {
		drv, err := sqlite.Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		t.Cleanup(func() { _ = drv.Close() })
		return drv
	}
	exec := func(drv *sqlite.SQLiteDriver, statements ...string) {
		for _, s := range statements {
			if _, err := drv.Execute(context.Background(), s); err != nil {
				t.Fatalf("exec %q: %v", s, err)
			}
		}
	}

	source := open("partial_source.db")
	exec(source,
		`CREATE TABLE book (id integer primary key, title text)`,
		`CREATE INDEX idx_plain ON book (title)`,
		`CREATE INDEX idx_partial ON book (lower(title) DESC) WHERE title <> ''`,
	)
	target := open("partial_target.db")

	ctx := context.Background()
	snapshotSource, err := Collect(ctx, source, "main")
	if err != nil {
		t.Fatalf("collect source: %v", err)
	}
	snapshotTarget, err := Collect(ctx, target, "main")
	if err != nil {
		t.Fatalf("collect target: %v", err)
	}

	// The partial index is collected with both its expression part and its
	// predicate — the data the boundary is about is really there.
	partial := indexNamed(tableNamed(snapshotSource, "book"), "idx_partial")
	if partial == nil {
		t.Fatal("idx_partial was not collected")
	}
	if partial.Predicate == "" || len(partial.Parts) == 0 || partial.Parts[0].Expression == "" {
		t.Fatalf("idx_partial = %+v, want an expression part and a predicate", partial)
	}

	entries := Diff(snapshotSource, snapshotTarget, Options{})
	var created []string
	for _, e := range entries {
		if e.Op == OpCreateIndex {
			created = append(created, e.Object)
		}
	}
	if len(created) != 1 || created[0] != "idx_plain" {
		t.Fatalf("created = %v, want only idx_plain", created)
	}

	result := Script(snapshotSource, snapshotTarget, entries)
	var indexStatements []string
	for _, st := range result.Statements {
		if strings.Contains(st.SQL, "CREATE INDEX") {
			indexStatements = append(indexStatements, st.SQL)
		}
	}
	if len(indexStatements) != 1 || !strings.Contains(indexStatements[0], "idx_plain") {
		t.Fatalf("index statements = %+v, want the plain index only", result.Statements)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("the left-out partial index must be reported, not swallowed")
	}
	var named bool
	for _, w := range result.Warnings {
		if strings.Contains(w, "idx_partial") {
			named = true
		}
	}
	if !named {
		t.Errorf("warnings = %+v, want idx_partial named", result.Warnings)
	}

	for _, st := range result.Statements {
		if _, err := target.Execute(ctx, st.SQL); err != nil {
			t.Fatalf("apply %q: %v", st.SQL, err)
		}
	}

	after, err := Collect(ctx, target, "main")
	if err != nil {
		t.Fatalf("re-collect: %v", err)
	}
	remaining := Diff(snapshotSource, after, Options{})
	if len(remaining) != 0 {
		t.Fatalf("a second comparison must be empty: %+v", remaining)
	}
	// And the sync did not invent the partial index behind the boundary.
	if got := indexNamed(tableNamed(after, "book"), "idx_partial"); got != nil {
		t.Errorf("idx_partial was rebuilt without its predicate: %+v", got)
	}
}

// serializeEntries flattens a plan to a comparable string so two plans can be
// compared byte for byte (K-F3 determinism contract).
func serializeEntries(entries []Entry) string {
	b, err := json.Marshal(entries)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// shuffleTables reorders a snapshot's tables, columns, indexes and foreign
// keys with a fixed Fisher-Yates seed, on a deep copy: a plan must not depend
// on the order the snapshot walked the schema in. Go map iteration order is
// random, so any path that leaks it (or an array order that sortEntries'
// stable tie-break merely preserves) shows up as a byte difference here.
func shuffleTables(snap *Snapshot, seed int64) *Snapshot {
	next := func(lo, hi int) int {
		seed = (seed*1103515245 + 12345) & 0x7fffffff
		return lo + int(seed%int64(hi-lo+1))
	}
	shuffle := func(n int, swap func(i, j int)) {
		for i := n - 1; i > 0; i-- {
			j := next(0, i)
			swap(i, j)
		}
	}
	out := *snap
	out.Tables = make([]Table, len(snap.Tables))
	for i, t := range snap.Tables {
		nt := t
		nt.Columns = make([]dbcore.ColumnInfo, len(t.Columns))
		copy(nt.Columns, t.Columns)
		nt.Indexes = make([]dbcore.IndexInfo, len(t.Indexes))
		copy(nt.Indexes, t.Indexes)
		nt.ForeignKeys = make([]dbcore.ForeignKeyInfo, len(t.ForeignKeys))
		copy(nt.ForeignKeys, t.ForeignKeys)
		out.Tables[i] = nt
	}
	shuffle(len(out.Tables), func(i, j int) { out.Tables[i], out.Tables[j] = out.Tables[j], out.Tables[i] })
	for i := range out.Tables {
		t := &out.Tables[i]
		shuffle(len(t.Columns), func(a, b int) { t.Columns[a], t.Columns[b] = t.Columns[b], t.Columns[a] })
		shuffle(len(t.Indexes), func(a, b int) { t.Indexes[a], t.Indexes[b] = t.Indexes[b], t.Indexes[a] })
		shuffle(len(t.ForeignKeys), func(a, b int) { t.ForeignKeys[a], t.ForeignKeys[b] = t.ForeignKeys[b], t.ForeignKeys[a] })
	}
	return &out
}

// TestDiffDeterminismAcrossInputOrder is the K-F3 contract for the compare
// plan: the same schema pair always yields the byte-identical plan, no matter
// what order the snapshot walked tables in. sortEntries orders by
// (phase, table, object, op), but SliceStable preserves generation order for
// ties — generation order is the snapshot's Tables order — so the plan would
// drift if any entry could tie. The corpus below spans every phase
// (drop/create/alter/add column/create index/add fk) to make a tie reachable.
func TestDiffDeterminismAcrossInputOrder(t *testing.T) {
	source := &Snapshot{Kind: sqltext.Postgres, Schema: "public", Tables: []Table{
		{Name: "author", Columns: []dbcore.ColumnInfo{
			column("id", "bigint", false, ""), column("name", "text", true, ""),
		}, Indexes: []dbcore.IndexInfo{pkIndex("author_pkey", "id")}},
		{Name: "book", Columns: []dbcore.ColumnInfo{
			column("id", "bigint", false, ""), column("author_id", "bigint", true, ""),
			column("title", "text", true, ""),
		}, Indexes: []dbcore.IndexInfo{
			pkIndex("book_pkey", "id"), index("idx_book_title", false, false, "title"),
		}, ForeignKeys: []dbcore.ForeignKeyInfo{fk("book_author_fk", []string{"author_id"}, "author", []string{"id"})}},
		{Name: "contract", Columns: []dbcore.ColumnInfo{
			column("id", "bigint", false, ""), column("amount", "numeric", true, "0"),
		}, Indexes: []dbcore.IndexInfo{pkIndex("contract_pkey", "id")}},
	}}
	target := &Snapshot{Kind: sqltext.Postgres, Schema: "public", Tables: []Table{
		{Name: "author", Columns: []dbcore.ColumnInfo{column("id", "bigint", false, "")},
			Indexes: []dbcore.IndexInfo{pkIndex("author_pkey", "id")}},
		{Name: "book", Columns: []dbcore.ColumnInfo{
			column("id", "bigint", false, ""), column("author_id", "bigint", true, ""),
			column("title", "text", true, "'untitled'"), // default change → alter column
		}, Indexes: []dbcore.IndexInfo{
			pkIndex("book_pkey", "id"), index("idx_book_author", false, false, "author_id"),
		}, ForeignKeys: []dbcore.ForeignKeyInfo{fk("book_author_fk", []string{"author_id"}, "author", []string{"id"})}},
		{Name: "contract", Columns: []dbcore.ColumnInfo{
			column("id", "bigint", false, ""), column("amount", "numeric", false, ""), // nullability + default change
		}, Indexes: []dbcore.IndexInfo{pkIndex("contract_pkey", "id")}},
		{Name: "invoice", Columns: []dbcore.ColumnInfo{column("id", "bigint", false, "")},
			Indexes: []dbcore.IndexInfo{pkIndex("invoice_pkey", "id")}}, // only in target → dropped
	}}

	reference := serializeEntries(Diff(source, target, Options{}))
	if len(reference) == 0 || reference == "[]" {
		t.Fatal("corpus produced no plan")
	}
	// Same input twice: byte-identical.
	if got := serializeEntries(Diff(source, target, Options{})); got != reference {
		t.Error("plan drifted between two identical runs")
	}
	// Shuffled input: the plan must be byte-identical to the reference.
	for _, seed := range []int64{1, 7, 42, 1234, 99999} {
		got := serializeEntries(Diff(shuffleTables(source, seed), shuffleTables(target, seed), Options{}))
		if got != reference {
			t.Errorf("plan drifted for seed %d: the plan must not depend on snapshot order", seed)
		}
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
