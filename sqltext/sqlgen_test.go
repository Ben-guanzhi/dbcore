package sqltext

import (
	"strings"
	"testing"
)

// The package is the one home of SQL text both clients generate; every engine
// must answer for every operation, and the statements must keep the shapes
// the callers (and the engines) expect.

func TestEveryEngineAnswersEveryOperation(t *testing.T) {
	for _, eng := range Engines {
		if _, err := QuoteIdent(eng, "col"); err != nil {
			t.Errorf("%s: QuoteIdent rejected a plain identifier: %v", eng, err)
		}
		if RowLimit(eng, "SELECT * FROM t", 100) == "SELECT * FROM t" {
			t.Errorf("%s: RowLimit left a bare SELECT uncapped", eng)
		}
		trunc, err := TruncateStatement(eng, "t")
		if err != nil {
			t.Errorf("%s: TruncateStatement: %v", eng, err)
		}
		if eng == SQLite && !strings.HasPrefix(trunc, "DELETE") {
			t.Errorf("%s: SQLite must DELETE (no TRUNCATE): %q", eng, trunc)
		}
		if eng != SQLite && !strings.HasPrefix(trunc, "TRUNCATE") {
			t.Errorf("%s: expected TRUNCATE, got %q", eng, trunc)
		}
		if _, err := RenameStatement(eng, "old", "new", "s", "old", "new"); err != nil {
			t.Errorf("%s: RenameStatement: %v", eng, err)
		}
		if _, err := LocalName(eng, "s", "t"); err != nil {
			t.Errorf("%s: LocalName rejected plain names: %v", eng, err)
		}
		if _, err := QualifiedTable(eng, "d", "s", "t"); err != nil {
			t.Errorf("%s: QualifiedTable rejected plain names: %v", eng, err)
		}
		if _, err := FKValuesQuery(eng, "t", "k", "", 10); err != nil {
			t.Errorf("%s: FKValuesQuery: %v", eng, err)
		}
		if !strings.Contains(CreateTableTemplate(eng, ""), "CREATE TABLE") {
			t.Errorf("%s: CreateTableTemplate produced no CREATE TABLE", eng)
		}
	}
}

func TestQuoteIdentPerEngine(t *testing.T) {
	if q, _ := QuoteIdent(MySQL, "a`b"); q != "`a``b`" {
		t.Errorf("mysql backtick doubling: %q", q)
	}
	if q, _ := QuoteIdent(SQLServer, "a]b"); q != "[a]]b]" {
		t.Errorf("sqlserver bracket doubling: %q", q)
	}
	if q, _ := QuoteIdent(Postgres, `a"b`); q != `"a""b"` {
		t.Errorf("standard double-quote doubling: %q", q)
	}
	for _, bad := range []string{"", strings.Repeat("x", 257), "a\x00b", "a\nb"} {
		if _, err := QuoteIdent(MySQL, bad); err == nil {
			t.Errorf("QuoteIdent accepted %q", bad)
		}
	}
	if _, err := QuoteIdent("nope", "a"); err == nil {
		t.Error("QuoteIdent accepted an unknown engine")
	}
}

// The webview used to hand-build these shapes unquoted; the locked spellings
// below are what the editor templates now come from.
func TestLocalAndQualifiedTableShapes(t *testing.T) {
	if n, _ := LocalName(Postgres, "public", "users"); n != `"public"."users"` {
		t.Errorf("postgres local: %q", n)
	}
	if n, _ := LocalName(SQLServer, "dbo", "users"); n != "[dbo].[users]" {
		t.Errorf("sqlserver local: %q", n)
	}
	if n, _ := LocalName(MySQL, "ignored", "users"); n != "`users`" {
		t.Errorf("mysql local ignores schema: %q", n)
	}
	if n, _ := LocalName(SQLite, "", "my users"); n != `"my users"` {
		t.Errorf("sqlite local quotes spaces: %q", n)
	}
	if n, _ := QualifiedTable(SQLServer, "shop", "dbo", "users"); n != "[shop].[dbo].[users]" {
		t.Errorf("sqlserver qualified: %q", n)
	}
	if n, _ := QualifiedTable(MySQL, "shop", "", "users"); n != "`shop`.`users`" {
		t.Errorf("mysql qualified: %q", n)
	}
	if n, _ := QualifiedTable(Postgres, "ignored", "public", "users"); n != `"public"."users"` {
		t.Errorf("postgres qualified: %q", n)
	}
	// A name carrying a quote or statement text must come out neutralised.
	if n, _ := QualifiedTable(Postgres, "d", `s"x`, "t;drop table u--"); n != `"s""x"."t;drop table u--"` {
		t.Errorf("quoting must neutralise embedded quotes: %q", n)
	}
}

func TestQuotedLiteralNationalPrefix(t *testing.T) {
	if got := QuotedLiteral(SQLServer, "a'b"); got != "N'a''b'" {
		t.Errorf("sqlserver literal: %q", got)
	}
	if got := QuotedLiteral(Postgres, "a'b"); got != "'a''b'" {
		t.Errorf("postgres must not take the national prefix: %q", got)
	}
}

func TestQuotedLiteralMySQLEscapesBackslash(t *testing.T) {
	// go-sql-driver's lexer takes '\' inside a string literal as entering
	// escape state, so an unescaped one would swallow the closing quote and run
	// the literal into the statement behind it.
	if got := QuotedLiteral(MySQL, "a\\"); got != `'a\\'` {
		t.Errorf("mysql trailing backslash must not escape the quote: %q", got)
	}
	if got := QuotedLiteral(MySQL, `a'b\c`); got != `'a''b\\c'` {
		t.Errorf("mysql quote and backslash together: %q", got)
	}
	if got := QuotedLiteral(SQLite, "a\\"); got != `'a\'` {
		t.Errorf("sqlite takes the backslash literally: %q", got)
	}
	if got := QuotedLiteral(Postgres, "a\\"); got != `'a\'` {
		t.Errorf("postgres takes the backslash literally: %q", got)
	}
	if got := QuotedLiteral(SQLServer, `a\b`); got != `N'a\b'` {
		t.Errorf("sqlserver takes the backslash literally: %q", got)
	}
}

func TestMySQLLikePatternSurvivesTheLiteralLayer(t *testing.T) {
	// Two escaping layers are active on MySQL: LikePattern escapes for LIKE and
	// QuotedLiteral escapes for the string literal. Only four backslashes in the
	// text make the engine see the two that LIKE needs.
	if got := QuotedLiteral(MySQL, LikePattern(`a\b`)); got != `'%a\\\\b%'` {
		t.Errorf("mysql LIKE backslash: %q", got)
	}
}

func TestRowLimitShapes(t *testing.T) {
	if got := RowLimit(SQLServer, "SELECT * FROM t", 5); got != "SELECT TOP (5) * FROM t" {
		t.Errorf("sqlserver TOP: %q", got)
	}
	// A CTE keeps whatever limit it declared itself: TOP cannot be injected.
	if got := RowLimit(SQLServer, "WITH c AS (SELECT 1) SELECT * FROM c", 5); got != "WITH c AS (SELECT 1) SELECT * FROM c" {
		t.Errorf("sqlserver CTE must run untouched: %q", got)
	}
	if got := RowLimit(Oracle, "SELECT * FROM t", 5); got != "SELECT * FROM (SELECT * FROM t) WHERE ROWNUM <= 5" {
		t.Errorf("oracle ROWNUM wrap: %q", got)
	}
	if got := RowLimit(MySQL, "SELECT * FROM t;", 5); got != "SELECT * FROM t LIMIT 5" {
		t.Errorf("trailing semicolon must go: %q", got)
	}
	for _, capped := range []string{"SELECT * FROM t LIMIT 9", "WITH c AS (SELECT 1 LIMIT 1) SELECT * FROM c", "UPDATE t SET x = 1"} {
		if got := RowLimit(MySQL, capped, 5); got != capped {
			t.Errorf("RowLimit touched %q -> %q", capped, got)
		}
	}
	if got := RowLimit(MySQL, "SELECT * FROM t", 0); got != "SELECT * FROM t" {
		t.Errorf("limit<=0 must not touch the statement: %q", got)
	}
}

func TestRenameStatementShapes(t *testing.T) {
	if got, _ := RenameStatement(MySQL, "`db`.`a`", "`db`.`b`", "", "a", "b"); got != "RENAME TABLE `db`.`a` TO `db`.`b`" {
		t.Errorf("mysql rename: %q", got)
	}
	want := "EXEC sp_rename N'dbo.a', N'b'"
	if got, _ := RenameStatement(SQLServer, "[a]", "[b]", "", "a", "b"); got != want {
		t.Errorf("sqlserver rename: %q, want %q", got, want)
	}
	if got, _ := RenameStatement(SQLServer, "[a]", "[b]", "sales", "a", "b"); got != "EXEC sp_rename N'sales.a', N'b'" {
		t.Errorf("sqlserver rename honours the schema: %q", got)
	}
	if got, _ := RenameStatement(Postgres, `"a"`, `"b"`, "", "a", "b"); got != `ALTER TABLE "a" RENAME TO "b"` {
		t.Errorf("postgres rename: %q", got)
	}
}

func TestFKValuesQuery(t *testing.T) {
	if _, err := FKValuesQuery(MySQL, "t", "k", "", 0); err == nil {
		t.Error("FKValuesQuery must reject limit<=0")
	}
	got, _ := FKValuesQuery(MySQL, "`r`", "k", "label", 10)
	if got != "SELECT DISTINCT `k`, `label` FROM `r` ORDER BY `k` LIMIT 10" {
		t.Errorf("mysql fk values: %q", got)
	}
	got, _ = FKValuesQuery(Oracle, "R", "K", "", 10)
	if !strings.Contains(got, "OFFSET 0 ROWS FETCH NEXT 10 ROWS ONLY") {
		t.Errorf("oracle fk values: %q", got)
	}
}

func TestNamespaceDefaults(t *testing.T) {
	if CatalogSchema(MySQL, "db1", "") != "db1" {
		t.Error("mysql's schema layer is the database")
	}
	if CatalogSchema(Postgres, "db1", "") != "public" {
		t.Error("postgres default schema")
	}
	if CatalogSchema(SQLServer, "db1", "") != "dbo" {
		t.Error("sqlserver default schema")
	}
	if DefaultTableSchema(Postgres, "") != "public" || DefaultTableSchema(Postgres, "sales") != "sales" {
		t.Error("postgres DefaultTableSchema")
	}
	if DefaultTableSchema(SQLite, "") != "" {
		t.Error("sqlite has no default namespace to fill")
	}
	// The starter template writes the default namespace for engines that
	// qualify tables by schema.
	if !strings.Contains(CreateTableTemplate(Postgres, ""), `"public"."new_table"`) {
		t.Error("postgres template must qualify with the default schema")
	}
	if strings.Contains(CreateTableTemplate(MySQL, ""), "public") {
		t.Error("mysql template must name the table bare")
	}
}

func TestLikePatternEscapesWildcards(t *testing.T) {
	if got := LikePattern(`a_b%c\d`); got != `%a\_b\%c\\d%` {
		t.Errorf("LikePattern: %q", got)
	}
}

func TestColumnTypeNumeric(t *testing.T) {
	for _, numeric := range []string{
		"int", "INT", "bigint", "numeric(10,2)", "decimal(12, 4)", "float8",
		"double precision", "money", "binary_double", "year",
		"int unsigned", "INT(11) UNSIGNED", "decimal(5,2) unsigned",
	} {
		if !NewColumnType(numeric).IsNumeric() {
			t.Errorf("%q must read as numeric", numeric)
		}
	}
	for _, notNumeric := range []string{
		"varchar(64)", "text", "date", "timestamp", "uuid", "bool", "json",
		"numeric_unsigned", "char(16)", "",
	} {
		if NewColumnType(notNumeric).IsNumeric() {
			t.Errorf("%q must not read as numeric", notNumeric)
		}
	}
}

// IsDate and IsNumeric are the two predicates an exporter keys off, so the
// sets must not overlap: a type in both would silently render as a number,
// because the exporter asks IsNumeric first and never reaches IsDate.
func TestColumnTypeDate(t *testing.T) {
	for _, dateType := range []string{
		"date", "DATE", "datetime", "DATETIME", "datetime2", "smalldatetime",
		"timestamp", "TIMESTAMP", "timestamp(6)", "timestamptz",
		"timestamp with time zone", "timestamp without time zone",
	} {
		if !NewColumnType(dateType).IsDate() {
			t.Errorf("%q must read as a date", dateType)
		}
	}
	// A bare "time" is deliberately not a date: it has no day to sort on, so a
	// "time" cell is text rather than a date Excel would date-stamp.
	for _, notDate := range []string{
		"varchar(64)", "text", "int", "numeric(10,2)", "uuid", "json",
		"time", "time(6)", "chars", "latest", "",
	} {
		if NewColumnType(notDate).IsDate() {
			t.Errorf("%q must not read as a date", notDate)
		}
	}
	// Exhaustive rather than a spot list: the two sets are closed, so the overlap
	// check can cover every entry instead of a sample that could be forgotten.
	for name := range numericTypeNames {
		if dateTypeNames[name] || strings.HasPrefix(name, "timestamp") {
			t.Errorf("%q reads as both numeric and date", name)
		}
	}
	for name := range dateTypeNames {
		if numericTypeNames[name] {
			t.Errorf("%q reads as both date and numeric", name)
		}
	}
}

// The seek gate has two halves because the cursor encodes them differently:
// integers bind as numbers, text as strings. Both halves and the refusal
// itself answer from the constructor's fold, so one declared spelling lands in
// the same bucket as its bare form.
func TestColumnTypeSeekKinds(t *testing.T) {
	for _, integer := range []string{
		"INTEGER", "int", "BIGINT", "smallint", "INT8", "SERIAL", "BIGSERIAL",
		"ROWID", "bigint(20) unsigned", "UNSIGNED BIG INT", " Int ",
	} {
		ct := NewColumnType(integer)
		if !ct.IsSeekable() || !ct.IsInteger() {
			t.Errorf("%q: want a seekable integer, got seekable=%v integer=%v",
				integer, ct.IsSeekable(), ct.IsInteger())
		}
	}
	for _, text := range []string{
		"TEXT", "varchar(255)", "CHAR(8)", "NVARCHAR(16)", "VARCHAR2(30)",
		"CLOB", "longtext",
	} {
		ct := NewColumnType(text)
		if !ct.IsSeekable() || ct.IsInteger() {
			t.Errorf("%q: want seekable text, got seekable=%v integer=%v",
				text, ct.IsSeekable(), ct.IsInteger())
		}
	}
	// POINT ends in "INT" and a decimal is a number, but neither is a cursor:
	// the gate itself must refuse them before the split is ever asked.
	for _, outside := range []string{
		"POINT", "REAL", "FLOAT", "DECIMAL(10,2)", "DATE", "BLOB", "UUID", "",
	} {
		ct := NewColumnType(outside)
		if ct.IsSeekable() || ct.IsInteger() {
			t.Errorf("%q: want outside the gate, got seekable=%v integer=%v",
				outside, ct.IsSeekable(), ct.IsInteger())
		}
	}
}
