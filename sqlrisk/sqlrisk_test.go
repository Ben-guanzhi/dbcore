package sqlrisk

import (
	"strings"
	"testing"
)

func TestAssessReadsAreRead(t *testing.T) {
	for _, sql := range []string{
		"SELECT * FROM t LIMIT 10",
		"  select id from t where id = 1 limit 1",
		"WITH x AS (SELECT 1) SELECT * FROM x LIMIT 5",
		"SHOW TABLES",
		"EXPLAIN SELECT 1",
		"DESCRIBE t",
	} {
		r := Assess(sql)
		if r.Level != LevelRead {
			t.Fatalf("%q → %s (%s)", sql, r.Level, r.Summary)
		}
	}
}

func TestAssessUnboundedReadIsFlagged(t *testing.T) {
	// Not a write risk, but a query that can pull a whole table is worth saying
	// out loud on the card.
	r := Assess("SELECT * FROM orders")
	if r.Level != LevelRead {
		t.Fatalf("level = %s", r.Level)
	}
	if !r.UnboundedScan {
		t.Fatal("a SELECT with no LIMIT should be flagged as unbounded")
	}
	if len(r.Reasons) == 0 {
		t.Fatal("the flag needs a reason a user can read")
	}
	// The bounded form is silent.
	if bounded := Assess("SELECT * FROM orders LIMIT 100"); bounded.UnboundedScan {
		t.Fatal("a LIMITed read must not be flagged")
	}
}

func TestAssessRecognisesRowLimitSpellings(t *testing.T) {
	for _, sql := range []string{
		"SELECT * FROM t LIMIT 10",
		"SELECT TOP 10 * FROM t",
		"SELECT * FROM t FETCH FIRST 10 ROWS ONLY",
		"SELECT * FROM t WHERE ROWNUM <= 10",
	} {
		if Assess(sql).UnboundedScan {
			t.Fatalf("%q should count as bounded", sql)
		}
	}
}

func TestAssessInsertIsWrite(t *testing.T) {
	r := Assess("INSERT INTO t(a) VALUES (1)")
	if r.Level != LevelWrite {
		t.Fatalf("level = %s", r.Level)
	}
	// The reason must make clear it does not touch existing rows: that is the
	// difference between this and an unguarded UPDATE.
	if len(r.Reasons) == 0 || !strings.Contains(r.Reasons[0], "不会改动已有行") {
		t.Fatalf("reasons = %v", r.Reasons)
	}
}

func TestAssessUpdateWithWhereIsWrite(t *testing.T) {
	r := Assess("UPDATE t SET a = 1 WHERE id = 5")
	if r.Level != LevelWrite {
		t.Fatalf("level = %s", r.Level)
	}
}

func TestAssessUpdateWithoutWhereIsDestructive(t *testing.T) {
	// The case the card exists for.
	r := Assess("UPDATE t SET a = 1")
	if r.Level != LevelDestructive {
		t.Fatalf("level = %s", r.Level)
	}
	if !strings.Contains(r.Summary, "整表") {
		t.Fatalf("summary = %q", r.Summary)
	}
	// Two reasons: what it does, and what to do instead. A card that only says
	// "dangerous" leaves the user stuck.
	if len(r.Reasons) < 2 {
		t.Fatalf("reasons = %v", r.Reasons)
	}
	var advises bool
	for _, reason := range r.Reasons {
		if strings.Contains(reason, "SELECT") {
			advises = true
		}
	}
	if !advises {
		t.Fatalf("reasons should suggest checking the range first: %v", r.Reasons)
	}
}

func TestAssessDeleteWithoutWhereIsDestructive(t *testing.T) {
	r := Assess("DELETE FROM t")
	if r.Level != LevelDestructive || !strings.Contains(r.Summary, "DELETE") {
		t.Fatalf("risk = %+v", r)
	}
}

func TestAssessDDLIsDestructive(t *testing.T) {
	for _, sql := range []string{"DROP TABLE t", "TRUNCATE TABLE t", "ALTER TABLE t ADD COLUMN a INT"} {
		r := Assess(sql)
		if r.Level != LevelDestructive {
			t.Fatalf("%q → %s", sql, r.Level)
		}
		if !strings.Contains(r.Summary, "结构变更") {
			t.Fatalf("%q summary = %q", sql, r.Summary)
		}
	}
}

func TestAssessIgnoresWhereInsideLiterals(t *testing.T) {
	// "UPDATE t SET note = 'WHERE is not here'" has no real WHERE clause. A naive
	// substring scan would call it safe — and the statement would rewrite the
	// table.
	r := Assess("UPDATE t SET note = 'WHERE is not here'")
	if r.Level != LevelDestructive {
		t.Fatalf("level = %s (%s)", r.Level, r.Summary)
	}
}

func TestAssessIgnoresCommentedOutWhere(t *testing.T) {
	r := Assess("UPDATE t SET a = 1 -- WHERE id = 1")
	if r.Level != LevelDestructive {
		t.Fatalf("a commented-out WHERE must not count: %+v", r)
	}
}

func TestAssessLeadingCommentDoesNotHideTheVerb(t *testing.T) {
	r := Assess("-- cleanup\nDELETE FROM t")
	if r.Level != LevelDestructive {
		t.Fatalf("risk = %+v", r)
	}
}

func TestAssessEmptyIsRead(t *testing.T) {
	if r := Assess("   \n\t "); r.Level != LevelRead {
		t.Fatalf("risk = %+v", r)
	}
}

func TestAssessUnknownWriteIsConservative(t *testing.T) {
	// A statement that is not a read and not a known verb must not be reported as
	// safe: the card's job is to be unsure out loud.
	r := Assess("GRANT SELECT ON t TO bob")
	if r.Level == LevelRead {
		t.Fatalf("an unrecognised write must not be labelled read: %+v", r)
	}
}
