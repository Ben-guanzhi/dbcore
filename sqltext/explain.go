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
