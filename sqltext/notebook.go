// SQL notebooks: cells whose statements can build on each other.
//
// The model is a pipeline, not a graph. A cell may reference an EARLIER cell with
// {{cell_N}} (1-based, matching the cell's position in the notebook), and running
// cell N expands the chain into CTEs:
//
//	cell 1: SELECT id, amount FROM orders WHERE amount > 100
//	cell 2: SELECT count(*) FROM {{cell_1}}
//
//	->  WITH cell_1 AS (SELECT id, amount FROM orders WHERE amount > 100)
//	    SELECT count(*) FROM cell_1
//
// Three rules decide the shape, and each exists because the alternative silently
// produces a statement that does not mean what the notebook shows:
//
//  1. References go BACKWARDS only. A forward or self reference is refused rather
//     than reordered: the notebook's order is the user's statement of intent, and
//     "helpfully" moving cells around would make cell N's result depend on a cell
//     the user has not seen run yet.
//  2. A referenced cell must be a READ. A CTE cannot wrap an UPDATE, so a chain
//     that points at a writing cell is refused by name instead of emitting a
//     statement the engine will reject with a syntax error.
//  3. The cell that RUNS may be a write, but only when it has no references —
//     `WITH … UPDATE` is not portable (Oracle rejects it outright), and a notebook
//     is meant to be the same notebook on every engine.
//
// Only the cells actually referenced (transitively) are inlined. Including every
// earlier cell would make a stale, expensive query part of an unrelated one.
package sqltext

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/dbcore/dbcore"
)

// NotebookCell is one cell of a notebook. Index in the slice is the cell's
// position; the reference {{cell_N}} is 1-based over that slice.
type NotebookCell struct {
	// SQL is the cell's statement.
	SQL string
}

// notebookRef matches {{cell_N}} and nothing else: the syntax is deliberately
// narrow so a literal brace pair in a SQL comment or a JSON expression does not
// become a reference by accident.
var notebookRef = regexp.MustCompile(`\{\{\s*cell_(\d+)\s*\}\}`)

// CellRefName is the CTE name a reference expands to.
func CellRefName(index1Based int) string { return "cell_" + strconv.Itoa(index1Based) }

// ExpandNotebook returns the statement to execute for cells[target] (0-based),
// with every {{cell_N}} reference replaced by a CTE over the referenced cell.
//
// Error messages name the cell and the problem in the user's terms ("cell 3
// references {{cell_5}}, which comes later"), because a notebook that fails to
// compile is a notebook whose author needs to know which cell to fix.
func ExpandNotebook(cells []NotebookCell, target int) (string, error) {
	if target < 0 || target >= len(cells) {
		return "", fmt.Errorf("notebook: cell %d is out of range", target+1)
	}
	targetSQL := strings.TrimSpace(cells[target].SQL)
	if targetSQL == "" {
		return "", fmt.Errorf("notebook: cell %d is empty", target+1)
	}
	// The order CTEs must appear in: dependencies first. Walking depth-first from
	// the target gives that order for a backwards-only graph.
	order := make([]int, 0, target)
	seen := map[int]bool{}
	if err := collectDeps(cells, target, seen, &order); err != nil {
		return "", err
	}

	// A write target cannot carry CTEs (see rule 3).
	if len(order) > 0 && !isReadStatement(targetSQL) {
		return "", fmt.Errorf(
			"notebook: cell %d references other cells but is not a read statement; "+
				"only a SELECT/WITH cell can build on earlier cells", target+1)
	}

	expandedTarget, err := expandRefs(targetSQL, cells, target, len(cells))
	if err != nil {
		return "", err
	}
	if len(order) == 0 {
		return expandedTarget, nil
	}

	var b strings.Builder
	b.WriteString("WITH ")
	for i, idx := range order {
		body, err := expandRefs(strings.TrimSpace(cells[idx].SQL), cells, idx, len(cells))
		if err != nil {
			return "", err
		}
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(CellRefName(idx + 1))
		b.WriteString(" AS (")
		b.WriteString(body)
		b.WriteString(")")
	}
	b.WriteString(" ")
	b.WriteString(expandedTarget)
	return b.String(), nil
}

// collectDeps appends the referenced cell indices in dependency order (deepest
// first), validating each reference as it goes.
func collectDeps(cells []NotebookCell, target int, seen map[int]bool, order *[]int) error {
	for _, m := range notebookRef.FindAllStringSubmatch(strings.TrimSpace(cells[target].SQL), -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil || n < 1 {
			return fmt.Errorf("notebook: cell %d has an invalid reference %q", target+1, m[0])
		}
		ref := n - 1
		if ref == target {
			return fmt.Errorf("notebook: cell %d references itself", target+1)
		}
		if ref > target {
			return fmt.Errorf("notebook: cell %d references {{cell_%d}}, which comes later; "+
				"a notebook only builds on earlier cells", target+1, n)
		}
		if ref >= len(cells) {
			return fmt.Errorf("notebook: cell %d references {{cell_%d}}, which does not exist",
				target+1, n)
		}
		if strings.TrimSpace(cells[ref].SQL) == "" {
			return fmt.Errorf("notebook: cell %d references {{cell_%d}}, which is empty", target+1, n)
		}
		if !isReadStatement(strings.TrimSpace(cells[ref].SQL)) {
			return fmt.Errorf("notebook: cell %d references {{cell_%d}}, which is not a read "+
				"statement; only SELECT/WITH results can be reused", target+1, n)
		}
		if seen[ref] {
			continue
		}
		seen[ref] = true
		if err := collectDeps(cells, ref, seen, order); err != nil {
			return err
		}
		*order = append(*order, ref)
	}
	return nil
}

// expandRefs replaces the references in one cell's SQL with their CTE names.
func expandRefs(sqlText string, cells []NotebookCell, self, total int) (string, error) {
	var firstErr error
	out := notebookRef.ReplaceAllStringFunc(sqlText, func(match string) string {
		sub := notebookRef.FindStringSubmatch(match)
		n, err := strconv.Atoi(sub[1])
		if err != nil || n < 1 || n > total {
			firstErr = fmt.Errorf("notebook: cell %d references {{cell_%s}}, which does not exist",
				self+1, sub[1])
			return match
		}
		return CellRefName(n)
	})
	if firstErr != nil {
		return "", firstErr
	}
	return strings.TrimSpace(strings.TrimRight(strings.TrimSpace(out), ";")), nil
}

// CellReferences returns the 1-based cell numbers one cell's SQL mentions, in the
// order they appear and without duplicates. The UI uses it to show "depends on"
// without re-implementing the syntax.
func CellReferences(sqlText string) []int {
	seen := map[int]bool{}
	var out []int
	for _, m := range notebookRef.FindAllStringSubmatch(sqlText, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil || n < 1 || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// isReadStatement reports whether the statement is a read, using the kernel's own
// rule so the notebook and the write guard cannot disagree about what "a read" is.
func isReadStatement(sqlText string) bool {
	trimmed := strings.TrimSpace(stripLeadingCommentsOnly(sqlText))
	if trimmed == "" {
		return false
	}
	return dbcore.IsReadQuery(trimmed)
}

// stripLeadingCommentsOnly drops the leading whitespace/comments dbcore's reader
// would skip anyway, so "the notebook says this is a read" and "the guard will let
// it run" look at the same first keyword.
func stripLeadingCommentsOnly(s string) string {
	for {
		s = strings.TrimLeft(s, " \t\r\n")
		switch {
		case strings.HasPrefix(s, "--"):
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = s[i+1:]
				continue
			}
			return ""
		case strings.HasPrefix(s, "/*"):
			i := strings.Index(s, "*/")
			if i < 0 {
				return ""
			}
			s = s[i+2:]
			continue
		}
		return s
	}
}
