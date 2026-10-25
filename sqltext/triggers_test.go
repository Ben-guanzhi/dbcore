package sqltext

import (
	"strings"
	"testing"
)

// Every engine answers the two questions; the table filter rides in the query
// itself (inline literal for SQL Server/Oracle, positional bind elsewhere).
func TestTriggersQueryByEngine(t *testing.T) {
	cases := []struct {
		dbType      string
		wantQuoted  string // the table literal, already in the SQL
		wantBinds   int    // positional args expected
	}{
		{"sqlite", "?", 1},
		{"mysql", "?", 2},
		{"postgresql", "?", 2},
		{"sqlserver", "'orders'", 0},
		{"oracle", "'ORDERS'", 0},
	}
	for _, c := range cases {
		q, args, err := TriggersQuery(c.dbType, "public", "orders")
		if err != nil {
			t.Fatalf("%s: %v", c.dbType, err)
		}
		if !strings.Contains(q, c.wantQuoted) {
			t.Fatalf("%s: query %q misses %q", c.dbType, q, c.wantQuoted)
		}
		if len(args) != c.wantBinds {
			t.Fatalf("%s: args = %v, want %d binds", c.dbType, args, c.wantBinds)
		}
	}
	if _, _, err := TriggersQuery("redis", "x", "y"); err == nil {
		t.Fatal("unknown engine must be refused")
	}
}

// Definitions: SQLite/MSSQL return the DDL directly; MySQL uses SHOW CREATE;
// PG reconstructs from pg_get_triggerdef; Oracle concatenates description+body.
func TestTriggerDefinitionQueryByEngine(t *testing.T) {
	q, _, err := TriggerDefinitionQuery("sqlite", "", "audit_insert")
	if err != nil || !strings.Contains(q, "sqlite_master") {
		t.Fatalf("sqlite: %v %q", err, q)
	}
	q, args, err := TriggerDefinitionQuery("postgresql", "public", "audit_insert")
	if err != nil || !strings.Contains(q, "pg_get_triggerdef") || len(args) != 1 {
		t.Fatalf("pg: %v %q %v", err, q, args)
	}
	q, _, err = TriggerDefinitionQuery("mysql", "public", "audit_insert")
	if err != nil || !strings.HasPrefix(q, "SHOW CREATE TRIGGER `audit_insert`") {
		t.Fatalf("mysql: %v %q", err, q)
	}
	q, _, err = TriggerDefinitionQuery("sqlserver", "", "audit_insert")
	if err != nil || !strings.Contains(q, "OBJECT_DEFINITION") || !strings.Contains(q, "'audit_insert'") {
		t.Fatalf("mssql: %v %q", err, q)
	}
	q, _, err = TriggerDefinitionQuery("oracle", "", "AUDIT_INSERT")
	if err != nil || !strings.Contains(q, "TRIGGER_BODY") || !strings.Contains(q, "'AUDIT_INSERT'") {
		t.Fatalf("oracle: %v %q", err, q)
	}
}
