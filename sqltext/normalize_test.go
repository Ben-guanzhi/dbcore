package sqltext

import "testing"

func TestNormalizeAliases(t *testing.T) {
	cases := map[string]string{
		"mysql":        MySQL,
		"mariadb":      MySQL,
		"postgres":     Postgres,
		"postgresql":   Postgres,
		"mssql":        SQLServer,
		"sqlserver":    SQLServer,
		"sqlite":       SQLite,
		"oracle":       Oracle,
		" PostgreSQL ": Postgres,
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestKindSpelling(t *testing.T) {
	if got := Kind("postgresql"); got != "postgres" {
		t.Errorf(`Kind("postgresql") = %q, want "postgres"`, got)
	}
	if got := Kind("sqlserver"); got != "mssql" {
		t.Errorf(`Kind("sqlserver") = %q, want "mssql"`, got)
	}
	if got := Kind("mariadb"); got != "mysql" {
		t.Errorf(`Kind("mariadb") = %q, want "mysql"`, got)
	}
}

func TestDialectForAliasesAgree(t *testing.T) {
	pg, ok := DialectFor("postgres")
	if !ok {
		t.Fatal("postgres dialect missing")
	}
	pgql, ok := DialectFor("postgresql")
	if !ok || pgql != pg {
		t.Fatal("postgres and postgresql must resolve to the same dialect")
	}
	if q, err := QuoteIdent("postgres", "a"); err != nil || q != `"a"` {
		t.Fatalf(`QuoteIdent("postgres","a") = %q, %v`, q, err)
	}
	if q, err := QuoteIdent("mssql", "a"); err != nil || q != "[a]" {
		t.Fatalf(`QuoteIdent("mssql","a") = %q, %v`, q, err)
	}
}
