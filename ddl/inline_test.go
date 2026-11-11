package ddl

import (
	"strings"
	"testing"
)

// W5: CreateTableInline puts the foreign keys inside the CREATE body, which is
// the only form SQLite accepts (it has no ALTER TABLE ... ADD CONSTRAINT).
func TestCreateTableInlinePlacesForeignKeysInTheBody(t *testing.T) {
	cols := []Column{{Name: "id", Type: "INTEGER", Nullable: false}}
	idx := []Index{{Name: "pk_orders", Primary: true, Unique: true, Columns: []string{"id"}}}
	fks := []ForeignKey{{
		Name: "fk_orders_customer", Columns: []string{"id"},
		RefTable: "customers", RefColumns: []string{"id"},
	}}

	inline := CreateTableInline("sqlite", "", "orders", cols, nil, idx, fks)
	if strings.Contains(inline, "ALTER TABLE") {
		t.Fatalf("inline form must not emit ALTER TABLE: %s", inline)
	}
	if !strings.Contains(inline, "FOREIGN KEY") {
		t.Fatalf("the constraint is missing: %s", inline)
	}
	// The constraint must sit inside the parenthesis, before the closing one.
	body := inline[:strings.Index(inline, "\n);")]
	if !strings.Contains(body, "FOREIGN KEY") {
		t.Fatalf("the constraint landed outside the CREATE body: %s", inline)
	}

	// The trailing-ALTER form is still what CreateTable produces, so the two
	// remain distinguishable by the caller.
	alter := CreateTable("sqlite", "", "orders", cols, nil, idx, fks)
	if !strings.Contains(alter, "ALTER TABLE") {
		t.Fatalf("CreateTable should keep the ALTER form: %s", alter)
	}
}

// The inline renderer must share the action whitelist: an unknown ON DELETE
// value is dropped rather than concatenated into the statement.
func TestCreateTableInlineDropsUnknownActions(t *testing.T) {
	got := CreateTableInline("sqlite", "", "t",
		[]Column{{Name: "id", Type: "INTEGER"}}, nil, nil,
		[]ForeignKey{{Name: "fk_x", Columns: []string{"id"}, RefTable: "u",
			RefColumns: []string{"id"}, OnDelete: "DROP TABLE users; --"}})
	if strings.Contains(got, "DROP TABLE") {
		t.Fatalf("an unknown action must not reach the SQL: %s", got)
	}
}

// The two renderers must agree on everything except where the constraint lives:
// a divergence would mean a copy built with one form produces a different table
// than the other.
func TestCreateTableInlineMatchesCreateTableOtherwise(t *testing.T) {
	cols := []Column{
		{Name: "id", Type: "INTEGER", Nullable: false},
		{Name: "note", Type: "TEXT", Nullable: true, DefaultValue: "''"},
	}
	idx := []Index{
		{Name: "pk_t", Primary: true, Unique: true, Columns: []string{"id"}},
		{Name: "idx_note", Unique: true, Columns: []string{"note"}},
	}
	fks := []ForeignKey{{Name: "fk_t_ref", Columns: []string{"id"}, RefTable: "u", RefColumns: []string{"id"}}}

	trailing := CreateTable("sqlite", "", "t", cols, nil, idx, fks)
	inline := CreateTableInline("sqlite", "", "t", cols, nil, idx, fks)

	// Both carry the secondary index.
	for _, script := range []string{trailing, inline} {
		if !strings.Contains(script, "CREATE UNIQUE INDEX") {
			t.Fatalf("missing index: %s", script)
		}
		if !strings.Contains(script, "pk_t") {
			t.Fatalf("missing primary key: %s", script)
		}
	}
	// The two renders must carry the same table shape: same columns, same
	// primary key, same secondary index. Comparing the strings line-by-line
	// would only measure comma placement, so this asserts the four facts that
	// actually matter for a copy.
	for _, script := range []string{trailing, inline} {
		for _, want := range []string{
			`"id" INTEGER NOT NULL`,
			`"note" TEXT DEFAULT ''`,
			`CONSTRAINT "pk_t" PRIMARY KEY ("id")`,
			`CREATE UNIQUE INDEX "idx_note" ON "t" ("note")`,
		} {
			if !strings.Contains(script, want) {
				t.Fatalf("render is missing %q:\n%s", want, script)
			}
		}
	}
	// And the difference is exactly where the constraint lives.
	if strings.Contains(trailing, "REFERENCES") && strings.Contains(trailing[:strings.Index(trailing, "\n);")], "REFERENCES") {
		t.Fatalf("CreateTable should keep the constraint outside the body:\n%s", trailing)
	}
	if !strings.Contains(inline[:strings.Index(inline, "\n);")], "REFERENCES") {
		t.Fatalf("CreateTableInline should put the constraint inside the body:\n%s", inline)
	}
}
