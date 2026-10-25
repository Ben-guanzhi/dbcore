package sqlite

import (
	"context"
	"testing"

	"github.com/dbcore/dbcore"
)

// The CREATE INDEX parser is the only way to recover an expression part's text
// and a partial index's predicate, so pin the shapes it must handle.
func TestSplitCreateIndex(t *testing.T) {
	cases := []struct {
		name     string
		sql      string
		wantPart []string
		wantPred string
	}{
		{
			name:     "plain columns",
			sql:      `CREATE INDEX ix ON t (a, b)`,
			wantPart: []string{"a", "b"},
		},
		{
			name:     "expression with direction and a partial predicate",
			sql:      `CREATE INDEX ix_expr ON t (lower(name) DESC, qty) WHERE qty > 1`,
			wantPart: []string{"lower(name) DESC", "qty"},
			wantPred: "qty > 1",
		},
		{
			name:     "quoted identifier containing a comma",
			sql:      `CREATE INDEX ix ON t ("weird,column", b)`,
			wantPart: []string{`"weird,column"`, "b"},
		},
		{
			name:     "function call with its own commas",
			sql:      `CREATE INDEX ix ON t (substr(name, 1, 2))`,
			wantPart: []string{"substr(name, 1, 2)"},
		},
		{
			name:     "where inside a string literal is not the predicate",
			sql:      `CREATE INDEX ix ON t (a) WHERE a <> 'x WHERE y'`,
			wantPart: []string{"a"},
			wantPred: `a <> 'x WHERE y'`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parts, pred, ok := splitCreateIndex(c.sql)
			if !ok {
				t.Fatalf("parse failed for %q", c.sql)
			}
			if len(parts) != len(c.wantPart) {
				t.Fatalf("parts = %v, want %v", parts, c.wantPart)
			}
			for i := range parts {
				if parts[i] != c.wantPart[i] {
					t.Errorf("part %d = %q, want %q", i, parts[i], c.wantPart[i])
				}
			}
			if pred != c.wantPred {
				t.Errorf("predicate = %q, want %q", pred, c.wantPred)
			}
		})
	}
}

func TestStripIndexDirection(t *testing.T) {
	cases := map[string]string{
		"lower(name) DESC": "lower(name)",
		"lower(name) asc":  "lower(name)",
		"qty":              "qty",
		`"desc"`:           `"desc"`,
	}
	for in, want := range cases {
		if got := stripIndexDirection(in); got != want {
			t.Errorf("stripIndexDirection(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestListIndexesExpressionAndPartial pins the metadata SQLite only exposes
// through the stored statement: the expression text, the DESC flag and the
// partial-index WHERE clause.
func TestListIndexesExpressionAndPartial(t *testing.T) {
	ctx := context.Background()
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	for _, ddl := range []string{
		`CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT, qty INTEGER)`,
		`CREATE INDEX ix_expr ON t (lower(name) DESC, qty) WHERE qty > 1`,
		`CREATE INDEX ix_plain ON t (qty)`,
	} {
		if _, err := d.Execute(ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}

	indexes, err := d.ListIndexes(ctx, "main", "t")
	if err != nil {
		t.Fatalf("ListIndexes: %v", err)
	}
	byName := map[string]dbcore.IndexInfo{}
	for _, idx := range indexes {
		byName[idx.Name] = idx
	}

	expr, ok := byName["ix_expr"]
	if !ok {
		t.Fatalf("ix_expr missing from %+v", indexes)
	}
	if expr.Method != "btree" {
		t.Errorf("method = %q, want btree", expr.Method)
	}
	if expr.Predicate != "qty > 1" {
		t.Errorf("predicate = %q, want %q", expr.Predicate, "qty > 1")
	}
	if len(expr.Parts) != 2 {
		t.Fatalf("parts = %+v, want 2", expr.Parts)
	}
	if expr.Parts[0].Expression != "lower(name)" || !expr.Parts[0].Desc {
		t.Errorf("part 0 = %+v, want lower(name) DESC", expr.Parts[0])
	}
	if expr.Parts[1].Column != "qty" || expr.Parts[1].Desc {
		t.Errorf("part 1 = %+v, want qty ASC", expr.Parts[1])
	}
	if len(expr.Columns) != 1 || expr.Columns[0] != "qty" {
		t.Errorf("columns = %v, want [qty] (expressions stay out of Columns)", expr.Columns)
	}

	plain := byName["ix_plain"]
	if plain.Predicate != "" {
		t.Errorf("plain index predicate = %q, want empty", plain.Predicate)
	}
	if len(plain.Parts) != 1 || plain.Parts[0].Column != "qty" {
		t.Errorf("plain parts = %+v", plain.Parts)
	}
}
