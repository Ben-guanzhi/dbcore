package drivertest

// The catalog-detail half of the shared contract (P3-8) and the extra-metadata
// half (P3-4): indexes with kinds/columns/parts, foreign keys, CreateTableSQL,
// BrowseRows sort+filter, and the objects beyond tables — views, CHECK
// constraints, sequences and routines.

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/dbcore/dbcore"
)

// runCatalogDetailContract asserts the methods the suite used to leave
// unasserted: ListIndexes, ListForeignKeys, CreateTableSQL and BrowseRows with
// Sort+Filter.
func (h Harness) runCatalogDetailContract(t *testing.T, ctx context.Context, drv dbcore.SQLTxDriver, table string) {
	t.Helper()
	fold := h.foldName
	fx := fixturesFor(h.kind())

	// The child fixture gets its own one-column FK target: the shared contract
	// table has no unique column, and every engine but SQLite requires one.
	fkParent := fold(fmt.Sprintf("dbcore_ct_parent_%d", rand.Int63()))
	child := fold(fmt.Sprintf("dbcore_ct_child_%d", rand.Int63()))
	fkName := fold(fmt.Sprintf("dbcore_ct_fk_%d", rand.Int63()))
	uxName := fold(fmt.Sprintf("dbcore_ct_ux_%d", rand.Int63()))
	ixName := fold(fmt.Sprintf("dbcore_ct_ix_%d", rand.Int63()))

	if _, err := drv.Execute(ctx, fkParentDDL(h.kind(), fkParent)); err != nil {
		t.Fatalf("provision FK parent: %v", err)
	}
	defer func() {
		_, _ = drv.Execute(ctx, DropTableSQL(h.kind(), fkParent))
	}()
	for _, stmt := range fx.childTable(child, fkParent, fkName, uxName, ixName) {
		if _, err := drv.Execute(ctx, stmt); err != nil {
			t.Fatalf("provision child table: %v\n  %s", err, stmt)
		}
	}
	defer func() {
		_, _ = drv.Execute(ctx, DropTableSQL(h.kind(), child))
	}()

	t.Run("catalog_indexes", func(t *testing.T) {
		indexes, err := drv.ListIndexes(ctx, h.Schema, child)
		if err != nil {
			t.Fatalf("ListIndexes: %v", err)
		}
		byName := make(map[string]dbcore.IndexInfo, len(indexes))
		for _, idx := range indexes {
			byName[strings.ToLower(idx.Name)] = idx
		}
		ux, ok := byName[strings.ToLower(uxName)]
		if !ok {
			t.Fatalf("unique index %q missing from %+v", uxName, indexes)
		}
		if !ux.Unique || ux.Kind != dbcore.IndexKindUnique || ux.Primary {
			t.Errorf("unique index = %+v, want unique/non-primary", ux)
		}
		if len(ux.Columns) != 1 || !strings.EqualFold(ux.Columns[0], "tag") {
			t.Errorf("unique index columns = %v, want [tag]", ux.Columns)
		}
		if ux.Method == "" {
			t.Errorf("unique index %q reports no access method", ux.Name)
		}
		if len(ux.Parts) != 1 || !strings.EqualFold(ux.Parts[0].Column, "tag") || ux.Parts[0].Desc {
			t.Errorf("unique index parts = %+v, want one ASC tag part", ux.Parts)
		}

		ix, ok := byName[strings.ToLower(ixName)]
		if !ok {
			t.Fatalf("plain index %q missing from %+v", ixName, indexes)
		}
		if ix.Unique || ix.Kind != dbcore.IndexKindIndex {
			t.Errorf("plain index = %+v, want non-unique index", ix)
		}
		if len(ix.Columns) != 2 || !strings.EqualFold(ix.Columns[0], "pid") || !strings.EqualFold(ix.Columns[1], "tag") {
			t.Errorf("plain index columns = %v, want [pid tag] in key order", ix.Columns)
		}
		if len(ix.Parts) != 2 || ix.Parts[0].Desc || ix.Parts[1].Desc {
			t.Errorf("plain index parts = %+v, want two ASC parts", ix.Parts)
		}
		// Any index a driver flags as primary must also carry the primary kind,
		// and vice versa: consumers switch on Kind.
		for _, idx := range indexes {
			if idx.Primary && idx.Kind != dbcore.IndexKindPrimary {
				t.Errorf("index %q: Primary=true but Kind=%q", idx.Name, idx.Kind)
			}
		}
	})

	t.Run("catalog_foreign_keys", func(t *testing.T) {
		fks, err := drv.ListForeignKeys(ctx, h.Schema, child)
		if err != nil {
			t.Fatalf("ListForeignKeys: %v", err)
		}
		if len(fks) != 1 {
			t.Fatalf("foreign keys = %+v, want exactly 1", fks)
		}
		fk := fks[0]
		if len(fk.Columns) != 1 || !strings.EqualFold(fk.Columns[0], "pid") {
			t.Errorf("fk columns = %v, want [pid]", fk.Columns)
		}
		if !strings.EqualFold(fk.RefTable, fkParent) {
			t.Errorf("fk RefTable = %q, want %q", fk.RefTable, fkParent)
		}
		if len(fk.RefColumns) != 1 || !strings.EqualFold(fk.RefColumns[0], "id") {
			t.Errorf("fk RefColumns = %v, want [id]", fk.RefColumns)
		}
		if fk.OnDelete != dbcore.FKActionCascade {
			t.Errorf("fk OnDelete = %q, want %q", fk.OnDelete, dbcore.FKActionCascade)
		}
		if fk.OnUpdate != "" && fk.OnUpdate != dbcore.FKActionNoAction {
			t.Errorf("fk OnUpdate = %q, want empty or NO ACTION", fk.OnUpdate)
		}
		// SQLite stores no constraint names (the driver synthesizes one), so
		// the name is only asserted where the engine keeps it.
		if h.kind() != dbcore.KindSQLite && !strings.EqualFold(fk.Name, fkName) {
			t.Errorf("fk name = %q, want %q", fk.Name, fkName)
		}
		if fk.Name == "" {
			t.Errorf("fk has no name at all")
		}
	})

	t.Run("create_table_sql", func(t *testing.T) {
		ddl, err := drv.CreateTableSQL(ctx, h.Schema, table)
		if err != nil {
			t.Fatalf("CreateTableSQL: %v", err)
		}
		for _, want := range []string{table, "id", "name"} {
			if !containsFold(ddl, want) {
				t.Errorf("CreateTableSQL output lacks %q:\n%s", want, ddl)
			}
		}
	})

	t.Run("browse_sort_filter", func(t *testing.T) {
		for i := 0; i < 5; i++ {
			if _, err := drv.Execute(ctx,
				fmt.Sprintf("INSERT INTO %s(id,name) VALUES (%s,%s)", table, h.Placeholder(1), h.Placeholder(2)),
				dbcore.Value{Kind: dbcore.ValInt, I: 1000 + int64(i)},
				dbcore.Value{Kind: dbcore.ValText, S: fmt.Sprintf("srt%d", i)},
			); err != nil {
				t.Fatalf("seed sort/filter row: %v", err)
			}
		}
		req := &dbcore.TableRowsRequest{
			Page:     1,
			PageSize: 2,
			Sort:     []dbcore.SortKey{{Column: h.foldCol("id"), Desc: true}},
			Filter: []dbcore.FilterExpr{
				{Column: h.foldCol("name"), Op: dbcore.FilterLike, Value: dbcore.Value{Kind: dbcore.ValText, S: "srt%"}},
				{Column: h.foldCol("id"), Op: dbcore.FilterGte, Value: dbcore.Value{Kind: dbcore.ValInt, I: 0}},
			},
		}
		res, err := drv.BrowseRows(ctx, h.Schema, table, req)
		if err != nil {
			t.Fatalf("BrowseRows: %v", err)
		}
		if len(res.Rows) != 2 {
			t.Fatalf("page rows = %d, want 2", len(res.Rows))
		}
		if got := intCells(res.Rows, 0); len(got) != 2 || got[0] != 1004 || got[1] != 1003 {
			t.Fatalf("page ids = %v, want [1004 1003] (DESC by id)", got)
		}
		// The count must respect the filter: the table holds other rows too.
		if !res.TotalFound {
			t.Errorf("TotalFound = false on a readable table")
		}
		if res.Total != 5 {
			t.Errorf("Total = %d, want 5 (filtered, not the whole table)", res.Total)
		}
		// Page 2 continues after the same filter.
		req.Page = 2
		res2, err := drv.BrowseRows(ctx, h.Schema, table, req)
		if err != nil {
			t.Fatalf("BrowseRows page 2: %v", err)
		}
		if got := intCells(res2.Rows, 0); len(got) != 2 || got[0] != 1002 || got[1] != 1001 {
			t.Fatalf("page 2 ids = %v, want [1002 1001]", got)
		}
	})
}

// runExtraMetadataContract asserts the P3-4 surface: view DDL, CHECK
// constraints, sequences and routines. A concept the engine lacks must answer
// ErrNotSupported (not an empty list), and a concept it has must come back.
func (h Harness) runExtraMetadataContract(t *testing.T, ctx context.Context, drv dbcore.SQLTxDriver, table string) {
	t.Helper()
	x, ok := dbcore.AsExtraMetadata(drv)
	if !ok {
		t.Skip("driver does not implement ExtraMetadataReader")
	}
	fold := h.foldName
	fx := fixturesFor(h.kind())

	dropAll := func(stmts []string) {
		for _, s := range stmts {
			_, _ = drv.Execute(ctx, s)
		}
	}
	create := func(kind string, stmts []string) {
		for _, s := range stmts {
			if _, err := drv.Execute(ctx, s); err != nil {
				t.Fatalf("provision %s: %v\n  %s", kind, err, s)
			}
		}
	}

	t.Run("extra_view_definition", func(t *testing.T) {
		view := fold(fmt.Sprintf("dbcore_ct_v_%d", rand.Int63()))
		create("view", []string{fx.view(view, table)})
		defer dropAll(fx.dropView(view))

		ddl, err := x.ViewDefinition(ctx, h.Schema, view)
		if err != nil {
			t.Fatalf("ViewDefinition: %v", err)
		}
		upper := strings.ToUpper(strings.TrimSpace(ddl))
		if !strings.HasPrefix(upper, "CREATE") {
			t.Errorf("view definition is not DDL: %q", ddl)
		}
		if !strings.Contains(upper, "SELECT") || !containsFold(ddl, table) {
			t.Errorf("view definition does not select from %q:\n%s", table, ddl)
		}
	})

	t.Run("extra_check_constraints", func(t *testing.T) {
		if fx.checkTable == nil {
			if _, err := x.CheckConstraints(ctx, h.Schema, table); !errors.Is(err, dbcore.ErrNotSupported) {
				t.Fatalf("CheckConstraints = %v, want ErrNotSupported", err)
			}
			return
		}
		ct := fold(fmt.Sprintf("dbcore_ct_ck_%d", rand.Int63()))
		ckName := fold(fmt.Sprintf("dbcore_ct_ckn_%d", rand.Int63()))
		create("check table", fx.checkTable(ct, ckName))
		defer dropAll([]string{DropTableSQL(h.kind(), ct)})

		got, err := x.CheckConstraints(ctx, h.Schema, ct)
		if err != nil {
			t.Fatalf("CheckConstraints: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("check constraints = %+v, want exactly 1", got)
		}
		if !strings.EqualFold(got[0].Name, ckName) {
			t.Errorf("check name = %q, want %q", got[0].Name, ckName)
		}
		if !containsFold(got[0].Expression, "qty") || !containsFold(got[0].Expression, "0") {
			t.Errorf("check expression = %q, want the qty > 0 predicate", got[0].Expression)
		}
	})

	t.Run("extra_sequences", func(t *testing.T) {
		if fx.sequence == nil {
			if _, err := x.Sequences(ctx, h.Schema); !errors.Is(err, dbcore.ErrNotSupported) {
				t.Fatalf("Sequences = %v, want ErrNotSupported", err)
			}
			return
		}
		seq := fold(fmt.Sprintf("dbcore_ct_seq_%d", rand.Int63()))
		create("sequence", fx.sequence(seq))
		defer dropAll(fx.dropSeq(seq))

		got, err := x.Sequences(ctx, h.Schema)
		if err != nil {
			t.Fatalf("Sequences: %v", err)
		}
		found := false
		for _, s := range got {
			if !strings.EqualFold(s.Name, seq) {
				continue
			}
			found = true
			if s.Start != 5 || s.Increment != 3 {
				t.Errorf("sequence %q = start %d increment %d, want 5/3", s.Name, s.Start, s.Increment)
			}
		}
		if !found {
			t.Fatalf("sequence %q missing from %+v", seq, got)
		}
	})

	t.Run("extra_routines", func(t *testing.T) {
		if fx.routine == nil {
			if _, err := x.Routines(ctx, h.Schema); !errors.Is(err, dbcore.ErrNotSupported) {
				t.Fatalf("Routines = %v, want ErrNotSupported", err)
			}
			return
		}
		routine := fold(fmt.Sprintf("dbcore_ct_rt_%d", rand.Int63()))
		create("routine", fx.routine(routine))
		defer dropAll(fx.dropRoutine(routine))

		got, err := x.Routines(ctx, h.Schema)
		if err != nil {
			t.Fatalf("Routines: %v", err)
		}
		found := false
		for _, r := range got {
			if !strings.EqualFold(r.Name, routine) {
				continue
			}
			found = true
			if r.Kind != fx.routineKind {
				t.Errorf("routine %q kind = %q, want %q", r.Name, r.Kind, fx.routineKind)
			}
		}
		if !found {
			t.Fatalf("routine %q missing from %+v", routine, got)
		}
	})
}

// kind returns the harness's engine, defaulting to the portable SQLite shape
// so a Harness built without Kind still runs the full suite.
func (h Harness) kind() dbcore.DatabaseKind {
	if h.Kind == "" {
		return dbcore.KindSQLite
	}
	return h.Kind
}

// foldName maps a generated fixture name to the engine's stored form.
func (h Harness) foldName(name string) string {
	if h.TableName == nil {
		return name
	}
	return h.TableName(name)
}

// foldCol maps a column name to the engine's stored form.
func (h Harness) foldCol(name string) string {
	if h.ColumnFold == nil {
		return name
	}
	return h.ColumnFold(name)
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

// intCells reads the integer value of column col from every row.
func intCells(rows [][]dbcore.Value, col int) []int64 {
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		if col >= len(r) {
			continue
		}
		n, ok := dbcore.IntFrom(r[col])
		if !ok {
			return nil
		}
		out = append(out, n)
	}
	return out
}
