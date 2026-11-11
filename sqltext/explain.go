package sqltext

import (
	"fmt"
	"strings"
)

// ExplainPlan is the recipe for one engine's execution plan. Query is the
// statement whose result set IS the plan; Prepare and Cleanup bracket it when
// the engine needs session state around the statement.
//
//   - MySQL / PostgreSQL prefix the statement with EXPLAIN.
//   - SQLite wants the human-readable EXPLAIN QUERY PLAN variant.
//   - SQL Server has no EXPLAIN statement: SHOWPLAN_XML is a session setting,
//     so the original statement runs under Prepare and answers with an XML
//     plan without executing.
//   - Oracle writes the plan to PLAN_TABLE under an EXPLAIN PLAN FOR statement
//     and the plan itself comes back from DBMS_XPLAN.DISPLAY.
//
// Prepare, Query and Cleanup must run on one and the same session when Prepare
// is set; engines without Prepare are stateless and the plan statement can go
// through any pool connection. Nothing in a plan ever executes the statement,
// so no write guard applies.
type ExplainPlan struct {
	Prepare string
	Query   string
	Cleanup string
}

// ExplainQuery returns how dbType explains sqlText. The statement is the
// caller's verbatim text (trailing semicolons stripped); a verb the engine
// cannot plan is the engine's error to give, not ours to guess.
func ExplainQuery(dbType, sqlText string) (ExplainPlan, error) {
	stmt := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(sqlText), ";"))
	if stmt == "" {
		return ExplainPlan{}, fmt.Errorf("explain needs a statement")
	}
	switch Normalize(dbType) {
	case MySQL, Postgres:
		return ExplainPlan{Query: "EXPLAIN " + stmt}, nil
	case SQLite:
		return ExplainPlan{Query: "EXPLAIN QUERY PLAN " + stmt}, nil
	case SQLServer:
		return ExplainPlan{
			Prepare: "SET SHOWPLAN_XML ON",
			Query:   stmt,
			Cleanup: "SET SHOWPLAN_XML OFF",
		}, nil
	case Oracle:
		return ExplainPlan{
			Prepare: "EXPLAIN PLAN FOR " + stmt,
			Query:   "SELECT * FROM TABLE(DBMS_XPLAN.DISPLAY(NULL, NULL, 'ALL'))",
		}, nil
	}
	return ExplainPlan{}, fmt.Errorf("explain is not supported for %s", Normalize(dbType))
}

// ExplainPlanFormat names the machine-readable shape a plan comes back in, which
// decides how a client parses it into a tree.
type ExplainPlanFormat string

const (
	// FormatJSON is a JSON document (PostgreSQL's FORMAT JSON, MySQL's
	// FORMAT=JSON).
	FormatJSON ExplainPlanFormat = "json"
	// FormatRows is a result set whose columns ARE the plan (SQLite's EXPLAIN
	// QUERY PLAN: id/parent/detail).
	FormatRows ExplainPlanFormat = "rows"
	// FormatXML is an XML document (SQL Server's SHOWPLAN_XML).
	FormatXML ExplainPlanFormat = "xml"
)

// ExplainPlanStructured returns the recipe for a STRUCTURED plan — one a client
// can turn into a tree rather than re-parse out of text — plus the payload's
// format. ok is false when the engine only offers a textual plan (Oracle's
// DBMS_XPLAN), in which case the caller shows the text grid instead of pretending
// to have a graph.
//
// This exists beside ExplainQuery because the two answer different questions:
// ExplainQuery produces the plan a human reads, this produces the plan a renderer
// walks. Both are statement SHAPES, which is why they live in the kernel rather
// than in a client's render code.
func ExplainPlanStructured(dbType, sqlText string) (plan ExplainPlan, format ExplainPlanFormat, ok bool, err error) {
	stmt := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(sqlText), ";"))
	if stmt == "" {
		return ExplainPlan{}, "", false, fmt.Errorf("explain needs a statement")
	}
	switch Normalize(dbType) {
	case Postgres:
		// VERBOSE stays off: the plan tree does not consume the extra columns,
		// and they triple the payload.
		return ExplainPlan{Query: "EXPLAIN (FORMAT JSON) " + stmt}, FormatJSON, true, nil
	case MySQL:
		// FORMAT=JSON is available on every MySQL 5.6+ and MariaDB 10.1+, which
		// is the floor this client targets.
		return ExplainPlan{Query: "EXPLAIN FORMAT=JSON " + stmt}, FormatJSON, true, nil
	case SQLite:
		// EXPLAIN QUERY PLAN's four columns (id/parent/notused/detail) are the
		// tree: no parsing of prose required.
		return ExplainPlan{Query: "EXPLAIN QUERY PLAN " + stmt}, FormatRows, true, nil
	case SQLServer:
		return ExplainPlan{
			Prepare: "SET SHOWPLAN_XML ON",
			Query:   stmt,
			Cleanup: "SET SHOWPLAN_XML OFF",
		}, FormatXML, true, nil
	}
	// Oracle answers with DBMS_XPLAN text; the caller keeps the grid.
	return ExplainPlan{}, "", false, nil
}
