package sqltext

import "testing"

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
