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

// TestAlterColumnOracleDefaultOnly pins the default-only shape on Oracle:
// CanAlterColumnDefault("oracle") is true, so a default-only change must emit
// a MODIFY with the new default — the pre-fix branch fell through to an empty
// result, a silent no-op. SQL Server is excluded by the predicate (a default
// is a named constraint there); the gap it reports names that, see
// TestAlterColumnDefaultOnlyEveryEngine.
func TestAlterColumnOracleDefaultOnly(t *testing.T) {
	from := Column{Name: "status", Type: "VARCHAR2(10)", Nullable: true, DefaultValue: "'A'"}
	to := Column{Name: "status", Type: "VARCHAR2(10)", Nullable: true, DefaultValue: "'B'"}
	got, gap := AlterColumn("oracle", "", "t", from, to)
	want := []string{`ALTER TABLE "t" MODIFY ("status" DEFAULT 'B');`}
	if !equalStrings(got, want) {
		t.Fatalf("oracle default-only = %v, want %v", got, want)
	}
	if gap != "" {
		t.Errorf("oracle can express a default, gap = %q, want none", gap)
	}

	// Dropping the default entirely.
	drop, dropGap := AlterColumn("oracle", "", "t", from, Column{Name: "status", Type: "VARCHAR2(10)", Nullable: true})
	if len(drop) != 1 || drop[0] != "ALTER TABLE \"t\" MODIFY (\"status\" DEFAULT NULL);" {
		t.Fatalf("oracle default drop = %v", drop)
	}
	if dropGap != "" {
		t.Errorf("oracle default drop gap = %q, want none", dropGap)
	}

	// A combined type+default change keeps the whole-column MODIFY, which
	// restates the default via columnDef.
	mixed, mixedGap := AlterColumn("oracle", "", "t", from, Column{Name: "status", Type: "VARCHAR2(20)", Nullable: true, DefaultValue: "'B'"})
	if len(mixed) != 1 || !strings.Contains(mixed[0], "VARCHAR2(20)") || !strings.Contains(mixed[0], "DEFAULT 'B'") {
		t.Fatalf("oracle mixed = %v", mixed)
	}
	if mixedGap != "" {
		t.Errorf("oracle mixed gap = %q, want none", mixedGap)
	}
}

// TestAlterColumnDefaultOnlyEveryEngine is the per-engine contract: a
// default-only change either comes out as the engine's own statement (PostgreSQL,
// MySQL, Oracle) or as a named gap (SQL Server stores a default as a named
// constraint; SQLite cannot alter a column at all). An empty statement list with
// no reason is the silent no-op this test exists to keep out.
func TestAlterColumnDefaultOnlyEveryEngine(t *testing.T) {
	from := Column{Name: "status", Type: "VARCHAR(10)", Nullable: true, DefaultValue: "'A'"}
	toSet := Column{Name: "status", Type: "VARCHAR(10)", Nullable: true, DefaultValue: "'B'"}
	toDrop := Column{Name: "status", Type: "VARCHAR(10)", Nullable: true}

	const sqlServerGap = "sqlserver 把默认值存为命名约束，无法原地替换，列 status 的默认值改动未生成语句（需手工处理）"
	const sqliteGap = "sqlite 不支持原地修改列（需重建整个表）"

	cases := []struct {
		engine      string
		wantSet     []string
		wantSetGap  string
		wantDrop    []string
		wantDropGap string
	}{
		{
			engine:   "postgres",
			wantSet:  []string{`ALTER TABLE "public"."t" ALTER COLUMN "status" SET DEFAULT 'B';`},
			wantDrop: []string{`ALTER TABLE "public"."t" ALTER COLUMN "status" DROP DEFAULT;`},
		},
		{
			engine:   "mysql",
			wantSet:  []string{"ALTER TABLE `public`.`t` MODIFY COLUMN `status` VARCHAR(10) DEFAULT 'B';"},
			wantDrop: []string{"ALTER TABLE `public`.`t` MODIFY COLUMN `status` VARCHAR(10);"},
		},
		{
			engine:   "oracle",
			wantSet:  []string{`ALTER TABLE "public"."t" MODIFY ("status" DEFAULT 'B');`},
			wantDrop: []string{`ALTER TABLE "public"."t" MODIFY ("status" DEFAULT NULL);`},
		},
		{engine: "sqlserver", wantSetGap: sqlServerGap, wantDropGap: sqlServerGap},
		{engine: "sqlite", wantSetGap: sqliteGap, wantDropGap: sqliteGap},
	}
	for _, tc := range cases {
		gotSet, gapSet := AlterColumn(tc.engine, "public", "t", from, toSet)
		if !equalStrings(gotSet, tc.wantSet) {
			t.Errorf("%s default-only set = %v, want %v", tc.engine, gotSet, tc.wantSet)
		}
		if gapSet != tc.wantSetGap {
			t.Errorf("%s default-only set gap = %q, want %q", tc.engine, gapSet, tc.wantSetGap)
		}
		gotDrop, gapDrop := AlterColumn(tc.engine, "public", "t", from, toDrop)
		if !equalStrings(gotDrop, tc.wantDrop) {
			t.Errorf("%s default drop = %v, want %v", tc.engine, gotDrop, tc.wantDrop)
		}
		if gapDrop != tc.wantDropGap {
			t.Errorf("%s default drop gap = %q, want %q", tc.engine, gapDrop, tc.wantDropGap)
		}
	}
}

// TestAlterColumnReportsAPartialDefaultChange pins the combined shape that is
// easier to get wrong than a default-only change: SQL Server can change the
// type in place but cannot carry the default with it, so the renderer must emit
// the usable statement and name the half it dropped rather than silently
// leaving the default behind.
func TestAlterColumnReportsAPartialDefaultChange(t *testing.T) {
	from := Column{Name: "status", Type: "VARCHAR(10)", Nullable: true, DefaultValue: "'A'"}
	to := Column{Name: "status", Type: "VARCHAR(20)", Nullable: true, DefaultValue: "'B'"}

	got, gap := AlterColumn("sqlserver", "public", "t", from, to)
	if len(got) != 1 || !strings.Contains(got[0], "VARCHAR(20)") {
		t.Fatalf("sqlserver type change = %v, want the type clause", got)
	}
	if gap == "" {
		t.Fatal("a skipped default change must be named")
	}
	if !strings.Contains(gap, "默认值") {
		t.Errorf("gap = %q, want it to name the skipped default", gap)
	}

	// The engines that carry a default through a type change must not warn.
	for _, engine := range []string{"oracle", "mysql"} {
		if _, otherGap := AlterColumn(engine, "public", "t", from, to); otherGap != "" {
			t.Errorf("%s type+default gap = %q, want none", engine, otherGap)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
