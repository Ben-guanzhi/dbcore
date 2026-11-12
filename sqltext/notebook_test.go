package sqltext

import (
	"strings"
	"testing"
)

// cell builds one notebook cell.
func cell(sql string) NotebookCell { return NotebookCell{SQL: sql} }

func TestExpandNotebookWithoutRefsIsTheCellItself(t *testing.T) {
	got, err := ExpandNotebook([]NotebookCell{cell("SELECT 1")}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got != "SELECT 1" {
		t.Fatalf("got %q", got)
	}
}

func TestExpandNotebookWrapsAnEarlierCell(t *testing.T) {
	cells := []NotebookCell{
		cell("SELECT id, amount FROM orders WHERE amount > 100;"),
		cell("SELECT count(*) FROM {{cell_1}}"),
	}
	got, err := ExpandNotebook(cells, 1)
	if err != nil {
		t.Fatal(err)
	}
	// One CTE, named for the cell it came from, with the trailing ';' removed —
	// a semicolon inside WITH … AS ( … ) is a syntax error.
	want := "WITH cell_1 AS (SELECT id, amount FROM orders WHERE amount > 100) " +
		"SELECT count(*) FROM cell_1"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestExpandNotebookIsTransitiveAndOrdered(t *testing.T) {
	// cell_3 → cell_2 → cell_1: the CTEs must come out deepest-first, or cell_2's
	// body would reference a name that is not defined yet.
	cells := []NotebookCell{
		cell("SELECT 1 AS a"),
		cell("SELECT a FROM {{cell_1}}"),
		cell("SELECT a FROM {{cell_2}}"),
	}
	got, err := ExpandNotebook(cells, 2)
	if err != nil {
		t.Fatal(err)
	}
	i1 := strings.Index(got, "cell_1 AS (")
	i2 := strings.Index(got, "cell_2 AS (")
	if i1 < 0 || i2 < 0 {
		t.Fatalf("both CTEs must be present:\n%s", got)
	}
	if i1 > i2 {
		t.Fatalf("cell_1 must be defined before cell_2:\n%s", got)
	}
	if !strings.HasPrefix(got, "WITH ") {
		t.Fatalf("expected a WITH prefix:\n%s", got)
	}
	if strings.Count(got, "SELECT a FROM cell_2") != 1 {
		t.Fatalf("the target statement is missing:\n%s", got)
	}
}

func TestExpandNotebookInlinesOnlyReferencedCells(t *testing.T) {
	// An unreferenced earlier cell must stay out: pulling it in would attach a
	// stale, possibly expensive query to an unrelated one.
	cells := []NotebookCell{
		cell("SELECT 1 AS expensive"),
		cell("SELECT 2 AS unrelated"),
		cell("SELECT expensive FROM {{cell_1}}"),
	}
	got, err := ExpandNotebook(cells, 2)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "cell_2") {
		t.Fatalf("cell_2 is not referenced but was inlined:\n%s", got)
	}
	if !strings.Contains(got, "cell_1 AS (") {
		t.Fatalf("cell_1 is referenced and must be inlined:\n%s", got)
	}
}

func TestExpandNotebookDeduplicatesRepeatedRefs(t *testing.T) {
	cells := []NotebookCell{
		cell("SELECT 1 AS a"),
		cell("SELECT * FROM {{cell_1}} x JOIN {{cell_1}} y ON x.a = y.a"),
	}
	got, err := ExpandNotebook(cells, 1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got, "cell_1 AS (") != 1 {
		t.Fatalf("a repeated reference must produce one CTE:\n%s", got)
	}
	if strings.Count(got, "cell_1 x") != 1 {
		t.Fatalf("both textual references must be rewritten:\n%s", got)
	}
}

func TestExpandNotebookRefusesForwardAndSelfReferences(t *testing.T) {
	// Rule 1: order is the user's intent. Reordering to satisfy a forward reference
	// would run a cell the user has not seen executed.
	if _, err := ExpandNotebook([]NotebookCell{
		cell("SELECT * FROM {{cell_2}}"),
		cell("SELECT 1"),
	}, 0); err == nil || !strings.Contains(err.Error(), "comes later") {
		t.Fatalf("forward ref: %v", err)
	}
	if _, err := ExpandNotebook([]NotebookCell{
		cell("SELECT * FROM {{cell_1}}"),
	}, 0); err == nil || !strings.Contains(err.Error(), "itself") {
		t.Fatalf("self ref: %v", err)
	}
}

func TestExpandNotebookRefusesWritingDependency(t *testing.T) {
	// Rule 2: a CTE cannot wrap an UPDATE, so this must fail with the cell named
	// instead of emitting a statement the server rejects with a syntax error.
	_, err := ExpandNotebook([]NotebookCell{
		cell("UPDATE t SET a = 1"),
		cell("SELECT * FROM {{cell_1}}"),
	}, 1)
	if err == nil || !strings.Contains(err.Error(), "not a read") {
		t.Fatalf("err = %v", err)
	}
}

func TestExpandNotebookRefusesAWriteThatReferences(t *testing.T) {
	// Rule 3: WITH … UPDATE is not portable (Oracle refuses it), so a write cell
	// may not build on other cells at all.
	_, err := ExpandNotebook([]NotebookCell{
		cell("SELECT 1 AS a"),
		cell("UPDATE t SET a = (SELECT a FROM {{cell_1}})"),
	}, 1)
	if err == nil || !strings.Contains(err.Error(), "not a read statement") {
		t.Fatalf("err = %v", err)
	}
}

func TestExpandNotebookAllowsAWriteWithNoReferences(t *testing.T) {
	got, err := ExpandNotebook([]NotebookCell{cell("UPDATE t SET a = 1")}, 0)
	if err != nil {
		t.Fatalf("a standalone write is a normal cell: %v", err)
	}
	if got != "UPDATE t SET a = 1" {
		t.Fatalf("got %q", got)
	}
}

func TestExpandNotebookRefusesEmptyAndOutOfRange(t *testing.T) {
	if _, err := ExpandNotebook([]NotebookCell{cell("  ")}, 0); err == nil {
		t.Fatal("an empty cell has nothing to run")
	}
	if _, err := ExpandNotebook([]NotebookCell{cell("SELECT 1")}, 3); err == nil {
		t.Fatal("an out-of-range index must be refused")
	}
	if _, err := ExpandNotebook([]NotebookCell{cell("SELECT 1"), cell("SELECT * FROM {{cell_1}}")}, 1); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	// A reference to a cell that exists but is empty is named as such.
	if _, err := ExpandNotebook([]NotebookCell{cell("  "), cell("SELECT * FROM {{cell_1}}")}, 1); err == nil ||
		!strings.Contains(err.Error(), "which is empty") {
		t.Fatalf("err = %v", err)
	}
}

func TestExpandNotebookToleratesWhitespaceInsideBraces(t *testing.T) {
	cells := []NotebookCell{cell("SELECT 1 AS a"), cell("SELECT a FROM {{ cell_1 }}")}
	got, err := ExpandNotebook(cells, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "FROM cell_1") {
		t.Fatalf("got %q", got)
	}
}

func TestExpandNotebookIgnoresNonCellBraces(t *testing.T) {
	// The syntax is narrow on purpose: a template placeholder or a JSON path in the
	// SQL must not be mistaken for a cell reference.
	got, err := ExpandNotebook([]NotebookCell{cell(`SELECT '{"a":1}' AS doc, "{{ name }}" AS tpl`)}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "{{ name }}") {
		t.Fatalf("a non-cell brace pair was rewritten: %q", got)
	}
}

func TestCellReferencesListsNumbersOnce(t *testing.T) {
	got := CellReferences("SELECT * FROM {{cell_2}} a JOIN {{cell_1}} b ON 1=1 JOIN {{cell_2}} c ON 1=1")
	want := []int{2, 1}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestExpandNotebookReadsThroughLeadingComments(t *testing.T) {
	// A commented cell is still a read; the comment must not make it look like a
	// write (which would refuse a perfectly good dependency).
	_, err := ExpandNotebook([]NotebookCell{
		cell("-- nightly aggregate\nSELECT 1 AS a"),
		cell("SELECT a FROM {{cell_1}}"),
	}, 1)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
}
