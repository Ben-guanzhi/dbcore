package ddl

import (
	"strings"
	"testing"
)

func TestCreateTableRendersFullScript(t *testing.T) {
	got := CreateTable("postgresql", "public", "users",
		[]Column{
			{Name: "id", Type: "bigserial", Nullable: false},
			{Name: "email", Type: "varchar(255)", Nullable: false, DefaultValue: "''"},
			{Name: "note", Type: "text", Nullable: true},
		},
		nil,
		[]Index{
			{Name: "users_pkey", Primary: true, Columns: []string{"id"}},
			{Name: "users_email_key", Unique: true, Columns: []string{"email"}},
		},
		[]ForeignKey{
			{Name: "orders_user_fk", Columns: []string{"user_id"}, RefSchema: "public", RefTable: "users",
				RefColumns: []string{"id"}, OnDelete: "CASCADE", OnUpdate: "NO ACTION"},
		},
	)

	wants := []string{
		`CREATE TABLE "public"."users" (`,
		`  "id" bigserial NOT NULL`,
		`  "email" varchar(255) DEFAULT '' NOT NULL`,
		`  "note" text,`,
		`  CONSTRAINT "users_pkey" PRIMARY KEY ("id")`,
		"\n);",
		`CREATE UNIQUE INDEX "users_email_key" ON "public"."users" ("email");`,
		`ALTER TABLE "public"."users" ADD CONSTRAINT "orders_user_fk" FOREIGN KEY ("user_id") REFERENCES "public"."users" ("id") ON DELETE CASCADE;`,
	}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("DDL missing %q:\n%s", want, got)
		}
	}
	// NO ACTION is the engine default; spelling it out would be noise.
	if strings.Contains(got, "ON UPDATE") {
		t.Errorf("ON UPDATE NO ACTION should be omitted:\n%s", got)
	}
}

func TestCreateTableQuotesPerEngine(t *testing.T) {
	cases := []struct{ dbType, want string }{
		{"mysql", "CREATE TABLE `app`.`users` ("},
		{"sqlserver", "CREATE TABLE [app].[users] ("},
		{"oracle", `CREATE TABLE "app"."users" (`},
	}
	for _, c := range cases {
		got := CreateTable(c.dbType, "app", "users", []Column{{Name: "id", Type: "INT"}}, nil, nil, nil)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: missing %q:\n%s", c.dbType, c.want, got)
		}
	}
}

func TestCreateTableLeavesSQLiteMainUnqualified(t *testing.T) {
	for _, schema := range []string{"", "main"} {
		got := CreateTable("sqlite", schema, "users", []Column{{Name: "id", Type: "INTEGER"}}, nil, nil, nil)
		if !strings.HasPrefix(got, `CREATE TABLE "users" (`) {
			t.Errorf("schema %q should not be qualified:\n%s", schema, got)
		}
	}
}

func TestCreateTableAppliesFullTypeOverride(t *testing.T) {
	got := CreateTable("sqlserver", "dbo", "t",
		[]Column{{Name: "name", Type: "varchar", Nullable: false}}, // bare from the column list
		map[string]string{"name": "varchar(50)"},                   // full from FullColumnTypesQuery
		nil, nil)
	if !strings.Contains(got, `[name] varchar(50) NOT NULL`) {
		t.Errorf("full type override not applied:\n%s", got)
	}
}

func TestCreateTableSkipsUnusableSecondaryIndexes(t *testing.T) {
	got := CreateTable("postgresql", "public", "t",
		[]Column{{Name: "id", Type: "int"}},
		nil,
		[]Index{
			{Name: "empty_key", Columns: nil},
			{Name: "t_pkey", Primary: true, Columns: []string{"id"}},
		},
		nil)
	if strings.Contains(got, "CREATE INDEX") || strings.Contains(got, "empty_key") {
		t.Errorf("column-less or primary index should not render a CREATE INDEX:\n%s", got)
	}
}

func TestNormalizeDefault(t *testing.T) {
	cases := []struct {
		dbType, value, want string
	}{
		{"sqlserver", "((0))", "0"},               // nested parens unwrap fully
		{"sqlserver", "(getdate())", "getdate()"}, // inner call's parens survive
		{"sqlserver", "N'x'", "N'x'"},             // nothing to unwrap
		{"mysql", "(0)", "(0)"},                   // only SQL Server wraps defaults
		{"postgresql", "nextval('s')", "nextval('s')"},
	}
	for _, c := range cases {
		if got := NormalizeDefault(c.dbType, c.value); got != c.want {
			t.Errorf("NormalizeDefault(%q, %q) = %q, want %q", c.dbType, c.value, got, c.want)
		}
	}
}
