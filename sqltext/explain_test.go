package sqltext

import (
	"strings"
	"testing"
)

func TestExplainQuery(t *testing.T) {
	cases := []struct {
		dbType string
		sql    string
		plan   ExplainPlan
	}{
		{"mysql", "SELECT * FROM t", ExplainPlan{Query: "EXPLAIN SELECT * FROM t"}},
		{"mariadb", "SELECT 1;", ExplainPlan{Query: "EXPLAIN SELECT 1"}},
		{"postgresql", "SELECT * FROM t", ExplainPlan{Query: "EXPLAIN SELECT * FROM t"}},
		{"pg", "DELETE FROM t WHERE id = 1", ExplainPlan{Query: "EXPLAIN DELETE FROM t WHERE id = 1"}},
		{"sqlite", "SELECT * FROM t", ExplainPlan{Query: "EXPLAIN QUERY PLAN SELECT * FROM t"}},
		{
			"sqlserver",
			"SELECT * FROM dbo.t",
			ExplainPlan{Prepare: "SET SHOWPLAN_XML ON", Query: "SELECT * FROM dbo.t", Cleanup: "SET SHOWPLAN_XML OFF"},
		},
		{
			"mssql",
			"UPDATE dbo.t SET a = 1",
			ExplainPlan{
				Prepare: "SET SHOWPLAN_XML ON",
				Query:   "UPDATE dbo.t SET a = 1",
				Cleanup: "SET SHOWPLAN_XML OFF",
			},
		},
		{
			"oracle",
			"SELECT * FROM t",
			ExplainPlan{
				Prepare: "EXPLAIN PLAN FOR SELECT * FROM t",
				Query:   "SELECT * FROM TABLE(DBMS_XPLAN.DISPLAY(NULL, NULL, 'ALL'))",
			},
		},
	}
	for _, c := range cases {
		plan, err := ExplainQuery(c.dbType, c.sql)
		if err != nil {
			t.Errorf("%s: %v", c.dbType, err)
			continue
		}
		if plan != c.plan {
			t.Errorf("%s: got %+v, want %+v", c.dbType, plan, c.plan)
		}
	}
}

func TestExplainQueryErrors(t *testing.T) {
	for _, sql := range []string{"", "  ", ";", ";;;"} {
		if _, err := ExplainQuery("mysql", sql); err == nil {
			t.Errorf("empty statement %q: want error, got nil", sql)
		}
	}
	if _, err := ExplainQuery("redis", "SELECT 1"); err == nil {
		t.Error("unknown engine: want error, got nil")
	}
}

func TestExplainQueryDMIsAReasonNotAMystery(t *testing.T) {
	// DM8 has no statement-shaped plan (live-verified: "EXPLAIN PLAN FOR" is
	// a syntax error, DBMS_XPLAN.DISPLAY is unresolved, and disql's plain
	// EXPLAIN is a client command a driver cannot run). The error must say
	// that, so a client shows an actionable message instead of the server's
	// "Error 9005: 非查询SQL语句".
	_, err := ExplainQuery("dm", "SELECT * FROM t")
	if err == nil {
		t.Fatal("dm: want an error, got a plan")
	}
	for _, want := range []string{"dm8", "disql", "V$SQL_PLAN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("dm error %q does not mention %q", err, want)
		}
	}
	// The dameng spellings normalize to the same answer.
	_, err2 := ExplainQuery("dameng", "SELECT * FROM t")
	if err2 == nil || !strings.Contains(err2.Error(), "disql") {
		t.Errorf("dameng: %v", err2)
	}
}
