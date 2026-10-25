package sqltext

import (
	"strings"
	"testing"
)

// View definitions bind positionally on SQLite/MySQL/PostgreSQL (each in its
// own spelling) and inline a literal on SQL Server/Oracle.
func TestViewDefinitionQueryShapes(t *testing.T) {
	cases := []struct {
		dbType string
		schema string
		want   []string
		binds  int
	}{
		{"sqlite", "main", []string{"sqlite_master", "type = 'view'", "name = ?"}, 1},
		{"mysql", "app", []string{"information_schema.VIEWS", "TABLE_SCHEMA = ?", "TABLE_NAME = ?"}, 2},
		{"postgresql", "public", []string{"pg_views", "schemaname = $1", "viewname = $2"}, 2},
		{"sqlserver", "sales", []string{"OBJECT_DEFINITION", "'sales.orders_v'"}, 0},
		{"oracle", "app", []string{"ALL_VIEWS", "OWNER = 'APP'", "VIEW_NAME = 'ORDERS_V'"}, 0},
	}
	for _, c := range cases {
		q, args, err := ViewDefinitionQuery(c.dbType, c.schema, "orders_v")
		if err != nil {
			t.Fatalf("%s: %v", c.dbType, err)
		}
		for _, want := range c.want {
			if !strings.Contains(q, want) {
				t.Errorf("%s: query misses %q:\n%s", c.dbType, want, q)
			}
		}
		if len(args) != c.binds {
			t.Errorf("%s: binds = %v, want %d", c.dbType, args, c.binds)
		}
	}
	if _, _, err := ViewDefinitionQuery("redis", "", "v"); err == nil {
		t.Fatal("unknown engine must be refused")
	}
}

func TestComposeViewDDL(t *testing.T) {
	// A complete CREATE VIEW comes back unchanged.
	sqlite := "CREATE VIEW v AS SELECT 1"
	if got := ComposeViewDDL("sqlite", "main", "v", sqlite); got != sqlite {
		t.Errorf("sqlite DDL = %q, want passthrough", got)
	}
	// A stored SELECT body gets the engine-correct prefix and qualified name.
	pg := ComposeViewDDL("postgresql", "public", "v", " SELECT 1;")
	if !strings.HasPrefix(pg, `CREATE OR REPLACE VIEW "public"."v" AS`) || !strings.Contains(pg, "SELECT 1;") {
		t.Errorf("pg DDL = %q", pg)
	}
	mysql := ComposeViewDDL("mysql", "app", "v", "select 1")
	if !strings.HasPrefix(mysql, "CREATE OR REPLACE VIEW `app`.`v` AS") {
		t.Errorf("mysql DDL = %q", mysql)
	}
	if got := ComposeViewDDL("postgresql", "public", "v", "   "); got != "" {
		t.Errorf("empty body = %q, want empty", got)
	}
}

func TestCheckConstraintsQueryPerEngine(t *testing.T) {
	if _, _, err := CheckConstraintsQuery("sqlite", "main", "t"); err == nil {
		t.Fatal("sqlite has no check catalog; want an error")
	}
	q, args, err := CheckConstraintsQuery("mysql", "app", "t")
	if err != nil || len(args) != 2 || !strings.Contains(q, "CHECK_CONSTRAINTS") {
		t.Fatalf("mysql: %v %q %v", err, q, args)
	}
	q, args, err = CheckConstraintsQuery("postgresql", "public", "t")
	if err != nil || len(args) != 2 || !strings.Contains(q, "pg_get_expr") || !strings.Contains(q, "$2") {
		t.Fatalf("pg: %v %q %v", err, q, args)
	}
	q, args, err = CheckConstraintsQuery("sqlserver", "dbo", "t")
	if err != nil || len(args) != 0 || !strings.Contains(q, "'dbo.t'") || !strings.Contains(q, "sys.check_constraints") {
		t.Fatalf("mssql: %v %q %v", err, q, args)
	}
	q, args, err = CheckConstraintsQuery("oracle", "app", "t")
	if err != nil || len(args) != 0 || !strings.Contains(q, "OWNER = 'APP'") ||
		!strings.Contains(q, "SEARCH_CONDITION") || !strings.Contains(q, "IS NOT NULL") {
		t.Fatalf("oracle: %v %q %v", err, q, args)
	}
	if _, _, err := CheckConstraintsQuery("redis", "", "t"); err == nil {
		t.Fatal("unknown engine must be refused")
	}
}

func TestSequencesQueryPerEngine(t *testing.T) {
	for _, dbType := range []string{"sqlite", "mysql"} {
		if _, _, err := SequencesQuery(dbType, "x"); err == nil {
			t.Fatalf("%s has no sequences; want an error", dbType)
		}
	}
	q, args, err := SequencesQuery("postgresql", "public")
	if err != nil || len(args) != 1 || !strings.Contains(q, "pg_sequences") || !strings.Contains(q, "$1") {
		t.Fatalf("pg: %v %q %v", err, q, args)
	}
	q, args, err = SequencesQuery("sqlserver", "dbo")
	if err != nil || len(args) != 0 || !strings.Contains(q, "'dbo'") || !strings.Contains(q, "sys.sequences") {
		t.Fatalf("mssql: %v %q %v", err, q, args)
	}
	q, args, err = SequencesQuery("oracle", "app")
	if err != nil || len(args) != 0 || !strings.Contains(q, "ALL_SEQUENCES") ||
		!strings.Contains(q, "ISEQ$") {
		t.Fatalf("oracle: %v %q %v", err, q, args)
	}
}

func TestRoutinesQueryPerEngine(t *testing.T) {
	if _, _, err := RoutinesQuery("sqlite", "main"); err == nil {
		t.Fatal("sqlite has no routines; want an error")
	}
	q, args, err := RoutinesQuery("mysql", "app")
	if err != nil || len(args) != 1 || !strings.Contains(q, "information_schema.ROUTINES") {
		t.Fatalf("mysql: %v %q %v", err, q, args)
	}
	q, args, err = RoutinesQuery("postgresql", "public")
	if err != nil || len(args) != 1 || !strings.Contains(q, "prokind") || !strings.Contains(q, "$1") {
		t.Fatalf("pg: %v %q %v", err, q, args)
	}
	q, args, err = RoutinesQuery("sqlserver", "dbo")
	if err != nil || len(args) != 0 || !strings.Contains(q, "is_ms_shipped = 0") {
		t.Fatalf("mssql: %v %q %v", err, q, args)
	}
	q, args, err = RoutinesQuery("oracle", "app")
	if err != nil || len(args) != 0 || !strings.Contains(q, "ALL_OBJECTS") || !strings.Contains(q, "'APP'") {
		t.Fatalf("oracle: %v %q %v", err, q, args)
	}
}
