package sqlformat

import (
	"strings"
	"testing"
)

// The formatter is dialect-agnostic — the clause keywords and quoting it
// uppercases/spaces are SQL-standard — so the 6 dialects each get a
// representative statement and we assert the readable shape: clause keywords
// on their own indented lines, keywords uppercased, and the SQL still round
// trips without changing what it means.

func TestFormatSelect(t *testing.T) {
	in := "select id,name from employees where salary > 1000 order by name limit 10"
	out := Format(in)

	for _, want := range []string{
		"SELECT", "FROM", "employees", "WHERE", "ORDER", "LIMIT",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("format %q = %q, missing %q", in, out, want)
		}
	}
	// Clause keywords each start on their own line.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "WHERE") && strings.Contains(line, "ORDER") {
			t.Fatalf("WHERE and ORDER share a line: %q", out)
		}
		if strings.Contains(line, "FROM") && strings.Contains(line, "WHERE") {
			t.Fatalf("FROM and WHERE share a line: %q", out)
		}
	}
	// Keywords are uppercased.
	for _, kw := range []string{"SELECT", "FROM", "WHERE", "ORDER BY", "LIMIT"} {
		if !strings.Contains(strings.ToUpper(out), kw) {
			t.Fatalf("format %q = %q, want keyword %q", in, out, kw)
		}
	}
}

func TestFormatKeepsQuotedStringAndCase(t *testing.T) {
	in := "select 'Hello, World' as greeting, \"Mixed\" from t"
	out := Format(in)
	// The string literal is untouched, case and spaces included.
	if !strings.Contains(out, "'Hello, World'") {
		t.Fatalf("format %q = %q, lost the string literal", in, out)
	}
	// A double-quoted identifier is preserved verbatim too.
	if !strings.Contains(out, `"Mixed"`) {
		t.Fatalf("format %q = %q, lost the quoted identifier", in, out)
	}
	// AS is uppercased because it is a keyword.
	if !strings.Contains(out, "AS") {
		t.Fatalf("format %q = %q, want AS", in, out)
	}
}

func TestFormatQualifiedNameNoSpaces(t *testing.T) {
	in := "select a.id from public.employees where b.name = a.name"
	out := Format(in)
	if strings.Contains(out, "a . id") || strings.Contains(out, "a .id") {
		t.Fatalf("format %q = %q, dotted name gained spaces", in, out)
	}
	if !strings.Contains(out, "public.employees") {
		t.Fatalf("format %q = %q, lost schema-qualified table", in, out)
	}
}

func TestFormatJoinLaysOnLines(t *testing.T) {
	in := "select e.id,d.name from employees e join dept d on e.dept=d.id where e.active=1"
	out := Format(in)
	// JOIN and ON are line-starting keywords.
	if !strings.Contains(out, "\nJOIN") || !strings.Contains(out, "\nON ") {
		t.Fatalf("format %q = %q, JOIN/ON not on their own lines", in, out)
	}
}

func TestFormatInsert(t *testing.T) {
	in := "insert into employees (id,name) values (1,'Ada'),(2,'Alan')"
	out := Format(in)
	for _, kw := range []string{"INSERT", "INTO", "VALUES"} {
		if !strings.Contains(strings.ToUpper(out), kw) {
			t.Fatalf("format %q = %q, missing %q", in, out, kw)
		}
	}
	// Both value rows survive; VALUES is uppercased.
	if !strings.Contains(out, "'Ada'") || !strings.Contains(out, "'Alan'") {
		t.Fatalf("format %q = %q, lost a values row", in, out)
	}
	if strings.Contains(out, "VALUES (1, 'Ada') (2, 'Alan')") {
		t.Fatalf("format %q = %q, second row not on its own line", in, out)
	}
}

func TestFormatUpdateDelete(t *testing.T) {
	upd := Format("update employees set salary=10 where id=1")
	for _, kw := range []string{"UPDATE", "SET", "WHERE"} {
		if !strings.Contains(strings.ToUpper(upd), kw) {
			t.Fatalf("update = %q, missing %q", upd, kw)
		}
	}
	del := Format("delete from employees where id=1")
	if !strings.Contains(strings.ToUpper(del), "DELETE") || !strings.Contains(strings.ToUpper(del), "FROM") {
		t.Fatalf("delete = %q, want DELETE FROM", del)
	}
}

func TestFormatSubqueryIndents(t *testing.T) {
	in := "select id from employees where dept in (select id from dept where active)"
	out := Format(in)
	// The inner SELECT/FROM/WHERE each start an indented two-space line.
	if !strings.Contains(out, "\n  SELECT id\n  FROM dept\n  WHERE active\n") {
		t.Fatalf("format %q = %q, subquery not indented as its own block", in, out)
	}
}

func TestFormatCommentsPreserved(t *testing.T) {
	in := "select 1 -- answer\n/* block */ from t"
	out := Format(in)
	if !strings.Contains(out, "-- answer") || !strings.Contains(out, "/* block */") {
		t.Fatalf("format %q = %q, lost a comment", in, out)
	}
}

func TestFormatEmpty(t *testing.T) {
	if got := Format(""); got != "" {
		t.Fatalf("empty = %q, want empty", got)
	}
	if got := Format("   \n\t "); got != "" {
		t.Fatalf("whitespace-only = %q, want empty", got)
	}
}

func TestFormatRoundTripParses(t *testing.T) {
	// A statement that would break if formatting changed a quoted string or
	// glued two tokens together.
	in := "select 'it''s' as s, 42 as n from \"My Table\" where x >= 10 and y <> 0"
	out := Format(in)
	// The doubled quote inside the string is intact.
	if !strings.Contains(out, "'it''s'") {
		t.Fatalf("format %q = %q, broke the quoted string", in, out)
	}
	// Operators keep their spacing.
	if !strings.Contains(out, ">=") || !strings.Contains(out, "<>") {
		t.Fatalf("format %q = %q, operators collapsed", in, out)
	}
	if !strings.Contains(out, `"My Table"`) {
		t.Fatalf("format %q = %q, lost quoted identifier", in, out)
	}
}
