package dbcore

import "testing"

func TestIsReadQuery(t *testing.T) {
	cases := []struct {
		query string
		want  bool
	}{
		// read-keyword prefixes
		{"SELECT 1", true},
		{"  select * from t", true},
		{"WITH cte AS (SELECT 1) SELECT * FROM cte", true},
		{"PRAGMA table_info(t)", true},
		{"SHOW DATABASES", true},
		{"EXPLAIN ANALYZE SELECT 1", true},
		{"VALUES ROW(1)", true},
		{"TABLE t", true},
		{"DESC t", true},
		{"describe t", true},
		{"(SELECT 1) UNION (SELECT 2)", true},
		// mutations
		{"INSERT INTO t VALUES (1)", false},
		{"UPDATE t SET a = 1", false},
		{"delete from t where id = 1", false},
		{"CREATE TABLE t (a int)", false},
		{"DROP TABLE t", false},
		{"", false},
		// RETURNING routes mutations to the query path
		{"INSERT INTO t (a) VALUES (1) RETURNING id", true},
		{"UPDATE t SET a = 1 RETURNING *", true},
		{"DELETE FROM t WHERE id = 1 RETURNING id", true},
		{"INSERT INTO t (a) VALUES (1) returning id", true},
		// 'returning' inside a literal / comment / identifier is not a clause
		{"UPDATE t SET note = 'returning'", false},
		{"UPDATE t SET note = 'x''returning y'", false},
		{"UPDATE t SET note = \"returning\"", false},
		{"UPDATE t SET note = `returning`", false},
		{"UPDATE t SET note = [returning]", false},
		{"-- returning\nUPDATE t SET a = 1", false},
		{"/* returning */ UPDATE t SET a = 1", false},
		{"UPDATE t SET a = 1 -- trailing returning comment", false},
		{"UPDATE returning_tbl SET a = 1", false},
		{"UPDATE t SET a = 1 WHERE b IN (SELECT c FROM returning_log)", false},
		// a value ending in a backslash must not swallow the closing quote
		{"UPDATE t SET note = 'trailing\\'", false},
		// the backslash escapes the quote, so "returning" stays inside the
		// literal and the unterminated quote swallows the rest of the statement
		{"UPDATE t SET note = 'returning\\'", false},
	}
	for _, c := range cases {
		if got := IsReadQuery(c.query); got != c.want {
			t.Errorf("IsReadQuery(%q) = %v, want %v", c.query, got, c.want)
		}
	}
}

func TestHasReturningClause(t *testing.T) {
	cases := []struct {
		sql  string
		want bool
	}{
		{"INSERT INTO t VALUES (1) RETURNING id", true},
		{"UPDATE t SET note = 'returning'", false},
		{"UPDATE t SET note = 'x''RETURNING'", false},
		{"UPDATE t SET note = \"RETURNING\"", false},
		{"UPDATE t SET note = `RETURNING`", false},
		{"UPDATE t SET note = [RETURNING]", false},
		{"-- RETURNING\nDELETE FROM t", false},
		{"/* RETURNING */ DELETE FROM t", false},
		{"DELETE FROM t WHERE id = 1 -- returning", false},
		{"DELETE FROM t WHERE id = 1 /* returning */", false},
		{"UPDATE returning_log SET a = 1", false},
		{"UPDATE t SET a = 1 WHERE b = 'x' AND c = 'y' RETURNING a", true},
		{"", false},
		{"SELECT 'unterminated", false},
	}
	for _, c := range cases {
		if got := HasReturningClause(c.sql); got != c.want {
			t.Errorf("HasReturningClause(%q) = %v, want %v", c.sql, got, c.want)
		}
	}
}
