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

// TestQuoteIdentLoose locks the validity-guard-free variant: it must use each
// dialect's own delimiters even for names QuoteIdent refuses, and normalise
// driver aliases the same way every other sqltext entry point does.
func TestQuoteIdentLoose(t *testing.T) {
	cases := []struct{ driver, name, want string }{
		{MySQL, "a`b", "`a``b`"},
		{MySQL, "", "``"},
		{MySQL, strings.Repeat("x", 300), "`" + strings.Repeat("x", 300) + "`"},
		{MySQL, "a\x00b", "`a\x00b`"},
		{SQLServer, "a]b", "[a]]b]"},
		{SQLServer, "a\nb", "[a\nb]"},
		{Postgres, `a"b`, `"a""b"`},
		{SQLite, `a"b`, `"a""b"`},
		{Oracle, `a"b`, `"a""b"`},
		{"nope", `a"b`, `"a""b"`},
		// Aliases must resolve to the canonical dialect, not the ANSI fallback.
		{"mariadb", "a`b", "`a``b`"},
		{"mssql", "a]b", "[a]]b]"},
		{"postgres", `a"b`, `"a""b"`},
	}
	for _, c := range cases {
		if got := QuoteIdentLoose(c.driver, c.name); got != c.want {
			t.Errorf("QuoteIdentLoose(%q, %q) = %q, want %q", c.driver, c.name, got, c.want)
		}
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
	// A WITH statement wraps in a derived table and gets the TOP cap: T-SQL
	// accepts the CTE head inside a subquery, and leaving it untouched was
	// an unbounded read.
	if got := RowLimit(SQLServer, "WITH c AS (SELECT 1) SELECT * FROM c", 5); got != "SELECT TOP (5) * FROM (WITH c AS (SELECT 1) SELECT * FROM c) AS limited" {
		t.Errorf("sqlserver WITH wrap: %q", got)
	}
	// A data-modifying CTE cannot be wrapped in a derived table: untouched.
	if got := RowLimit(SQLServer, "WITH c AS (SELECT 1) INSERT INTO t SELECT * FROM c", 5); got != "WITH c AS (SELECT 1) INSERT INTO t SELECT * FROM c" {
		t.Errorf("sqlserver DML CTE must run untouched: %q", got)
	}
	// A WITH that carries its own TOP keeps it (ownLimitKind still applies).
	if got := RowLimit(SQLServer, "WITH c AS (SELECT 1) SELECT TOP 9 * FROM c", 5); got != "WITH c AS (SELECT 1) SELECT TOP 9 * FROM c" {
		t.Errorf("sqlserver CTE own limit must survive: %q", got)
	}
	if got := RowLimit(Oracle, "SELECT * FROM t", 5); got != "SELECT * FROM (SELECT * FROM t) WHERE ROWNUM <= 5" {
		t.Errorf("oracle ROWNUM wrap: %q", got)
	}
	if got := RowLimit(MySQL, "SELECT * FROM t;", 5); got != "SELECT * FROM t LIMIT 5" {
		t.Errorf("trailing semicolon must go: %q", got)
	}
	// A data-modifying CTE must not take a trailing LIMIT: MySQL and
	// PostgreSQL accept the spelling, and the cap would silently truncate
	// the write. The SQL Server arm has had this guard since wave 16; the
	// LIMIT dialects were missing it until wave 40.
	for _, engine := range []string{MySQL, Postgres, SQLite} {
		if got := RowLimit(engine, "WITH c AS (SELECT 1) INSERT INTO t SELECT * FROM c", 5); got != "WITH c AS (SELECT 1) INSERT INTO t SELECT * FROM c" {
			t.Errorf("%s DML CTE must run untouched: %q", engine, got)
		}
		if got := RowLimit(engine, "WITH c AS (SELECT 1) SELECT * FROM c", 5); got != "WITH c AS (SELECT 1) SELECT * FROM c LIMIT 5" {
			t.Errorf("%s read CTE should cap: %q", engine, got)
		}
	}
	// A LIMIT buried in a CTE caps the CTE, not the rows the caller sees, so
	// the outer statement still needs the cap. This case used to be pinned as
	// untouched because ownLimitKind's predecessor matched the word "limit"
	// anywhere in the statement.
	if got := RowLimit(MySQL, "WITH c AS (SELECT 1 LIMIT 1) SELECT * FROM c", 5); got != "WITH c AS (SELECT 1 LIMIT 1) SELECT * FROM c LIMIT 5" {
		t.Errorf("a CTE-internal LIMIT must not suppress the outer cap: %q", got)
	}
	// Statements that already cap their own rows stay untouched.
	for _, capped := range []string{"SELECT * FROM t LIMIT 9", "UPDATE t SET x = 1"} {
		if got := RowLimit(MySQL, capped, 5); got != capped {
			t.Errorf("RowLimit touched %q -> %q", capped, got)
		}
	}
	if got := RowLimit(MySQL, "SELECT * FROM t", 0); got != "SELECT * FROM t" {
		t.Errorf("limit<=0 must not touch the statement: %q", got)
	}
}

// TestRowLimitIgnoresLeadingTrivia pins the fix for the row cap silently
// no-oping on statements that IsReadQuery classifies as reads. A leading
// comment or a surrounding parenthesis used to defeat the SELECT prefix test
// (the cap was skipped) while IsReadQuery — which strips trivia first — routed
// the same statement into the capped branch: the two functions disagreed on the
// same input and the user's configured ResultLimit never applied.
func TestRowLimitIgnoresLeadingTrivia(t *testing.T) {
	const cap = " LIMIT 5"
	for _, engine := range []string{MySQL, Postgres, SQLite} {
		cases := []struct{ name, sql, want string }{
			{"line-comment", "-- note\nSELECT * FROM t", "-- note\nSELECT * FROM t" + cap},
			{"block-comment", "/* hint */ SELECT * FROM t", "/* hint */ SELECT * FROM t" + cap},
			{"oracle-hint", "/*+ MAX_EXECUTION_TIME(1000) */ SELECT * FROM t", "/*+ MAX_EXECUTION_TIME(1000) */ SELECT * FROM t" + cap},
			{"leading-paren", "(SELECT * FROM t)", "(SELECT * FROM t)" + cap},
			{"leading-semicolon", ";SELECT * FROM t", ";SELECT * FROM t" + cap},
			{"plain", "SELECT * FROM t", "SELECT * FROM t" + cap},
		}
		for _, tc := range cases {
			if got := RowLimit(engine, tc.sql, 5); got != tc.want {
				t.Errorf("%s %s: got %q, want %q", engine, tc.name, got, tc.want)
			}
		}
	}
	// The comment must be preserved in the output: only classification strips
	// it, the cap is appended to the statement the user wrote.
	if got := RowLimit(Oracle, "/*+ hint */ SELECT * FROM t", 5); got != "SELECT * FROM (/*+ hint */ SELECT * FROM t) WHERE ROWNUM <= 5" {
		t.Errorf("oracle must wrap the original text: %q", got)
	}
	// SQL Server spells the cap inline as TOP (n), so the statement has to be
	// rewritten rather than appended to. The rewrite must insert TOP after the
	// SELECT the trivia scan found, not slice trimmed at offset 6: the latter
	// assumed SELECT starts the string and produced `SELECT TOP (5)e\nSELECT`
	// for a leading line comment, `SELECT TOP (5)t */ SELECT` for a block
	// comment and `SELECT TOP (5)T * FROM t)` for a leading parenthesis.
	sqlServer := []struct{ name, sql, want string }{
		{"line-comment", "-- note\nSELECT * FROM t", "-- note\nSELECT TOP (5) * FROM t"},
		{"block-comment", "/* hint */ SELECT * FROM t", "/* hint */ SELECT TOP (5) * FROM t"},
		{"oracle-hint", "/*+ MAX_EXECUTION_TIME(1000) */ SELECT * FROM t", "/*+ MAX_EXECUTION_TIME(1000) */ SELECT TOP (5) * FROM t"},
		{"leading-paren", "(SELECT * FROM t)", "(SELECT TOP (5) * FROM t)"},
		{"leading-semicolon", ";SELECT * FROM t", ";SELECT TOP (5) * FROM t"},
		{"plain", "SELECT * FROM t", "SELECT TOP (5) * FROM t"},
		{"lowercase", "select * from t", "SELECT TOP (5) * from t"},
		{"lowercase-comment", "/* x */ select 1", "/* x */ SELECT TOP (5) 1"},
	}
	for _, tc := range sqlServer {
		if got := RowLimit(SQLServer, tc.sql, 5); got != tc.want {
			t.Errorf("sqlserver %s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestRowLimitWrappingArmsStripLeadingSeparator pins that the wrapping
// dialects (ROWNUM, TOP ... FROM (...)) drop a leading statement separator
// before putting the text inside parentheses. Appending a LIMIT can keep a
// leading `;` because it stays at the front of the statement, but a wrapping
// dialect would emit `SELECT * FROM (;SELECT ...)` / `... FROM (;WITH ...)`,
// neither of which parses. Comments are not separators and survive.
func TestRowLimitWrappingArmsStripLeadingSeparator(t *testing.T) {
	cases := []struct{ engine, sql, want string }{
		{Oracle, ";SELECT * FROM t", "SELECT * FROM (SELECT * FROM t) WHERE ROWNUM <= 5"},
		{Oracle, ";WITH c AS (SELECT 1) SELECT * FROM c", "SELECT * FROM (WITH c AS (SELECT 1) SELECT * FROM c) WHERE ROWNUM <= 5"},
		{Oracle, "/* hint */ SELECT * FROM t", "SELECT * FROM (/* hint */ SELECT * FROM t) WHERE ROWNUM <= 5"},
		{SQLServer, ";WITH c AS (SELECT 1) SELECT * FROM c", "SELECT TOP (5) * FROM (WITH c AS (SELECT 1) SELECT * FROM c) AS limited"},
	}
	for _, tc := range cases {
		if got := RowLimit(tc.engine, tc.sql, 5); got != tc.want {
			t.Errorf("%s %q: got %q, want %q", tc.engine, tc.sql, got, tc.want)
		}
	}
}

// TestRowLimitDepthAwareOwnLimit pins that a row limit only counts at paren
// depth 0, and that OFFSET alone is not a row limit. The predecessor matched
// any of (top|limit|offset|fetch|rownum) as a substring anywhere in the
// statement, so it skipped the cap for a nested LIMIT, for an OFFSET with no
// LIMIT, and for a column aliased top.
func TestRowLimitDepthAwareOwnLimit(t *testing.T) {
	for _, engine := range []string{MySQL, Postgres, SQLite} {
		cases := []struct {
			name string
			sql  string
			cap  bool // true = expect the cap to be applied
		}{
			{"nested-subquery-limit", "SELECT * FROM big WHERE id IN (SELECT id FROM small LIMIT 5)", true},
			{"offset-only", "SELECT * FROM items OFFSET 10", true},
			{"alias-top", "SELECT * FROM items AS top", true},
			{"alias-limit", "SELECT * FROM items AS limit", true},
			{"own-limit", "SELECT * FROM items LIMIT 10", false},
			{"own-limit-with-offset", "SELECT * FROM items LIMIT 10 OFFSET 5", false},
			{"limit-all", "SELECT * FROM items LIMIT ALL", true},
			{"limit-param", "SELECT * FROM items LIMIT ?", false},
		}
		for _, tc := range cases {
			got := RowLimit(engine, tc.sql, 50)
			want := tc.sql
			if tc.cap {
				want = tc.sql + " LIMIT 50"
			}
			if got != want {
				t.Errorf("%s %s: got %q, want %q", engine, tc.name, got, want)
			}
		}
	}
	// SQL Server spells the cap TOP (n); a nested LIMIT must not suppress it.
	if got := RowLimit(SQLServer, "SELECT * FROM big WHERE id IN (SELECT id FROM small LIMIT 5)", 5); got != "SELECT TOP (5) * FROM big WHERE id IN (SELECT id FROM small LIMIT 5)" {
		t.Errorf("sqlserver nested LIMIT must not suppress the TOP cap: %q", got)
	}
	if got := RowLimit(SQLServer, "SELECT * FROM items OFFSET 10", 5); got != "SELECT TOP (5) * FROM items OFFSET 10" {
		t.Errorf("sqlserver OFFSET-only must be capped: %q", got)
	}
	// A bare "top" identifier is not a TOP clause.
	if got := RowLimit(SQLServer, "SELECT * FROM items AS top", 5); got != "SELECT TOP (5) * FROM items AS top" {
		t.Errorf("sqlserver alias top must be capped: %q", got)
	}
	if got := RowLimit(SQLServer, "SELECT TOP (9) * FROM items", 5); got != "SELECT TOP (9) * FROM items" {
		t.Errorf("sqlserver own TOP must survive: %q", got)
	}
	// N1 回归：别名 [a]]LIMIT 5] 是标识符 a]LIMIT 5（quoteIdentSQLServer
	// 把 "]" 转义成 "]]"）。ownLimitKind 停在第一个 ] 曾把其中的 LIMIT 5
	// 当成语句自带的行限制，封顶被静默跳过。
	if got := RowLimit(SQLServer, "SELECT * FROM items AS [a]]LIMIT 5]", 5); got != "SELECT TOP (5) * FROM items AS [a]]LIMIT 5]" {
		t.Errorf("sqlserver bracket alias with ]] escape must be capped: %q", got)
	}
	// CTE 名 [a]]DELETE]（真名 a]DELETE）是读语句，必须包 TOP。
	if got := RowLimit(SQLServer, "WITH [a]]DELETE] AS (SELECT 1) SELECT * FROM big", 5); got != "SELECT TOP (5) * FROM (WITH [a]]DELETE] AS (SELECT 1) SELECT * FROM big) AS limited" {
		t.Errorf("sqlserver CTE name with ]] escape must be wrapped: %q", got)
	}
}

// TestCteOuterIsReadStepsOverNonWordBytes pins the scanner's stepping: at
// depth 0 every byte must advance the index. A revision of cteOuterIsRead
// stalled on the space after WITH (the word scan consumed nothing and the
// index never moved), which hung RowLimit on every SQL Server WITH
// statement. The shapes below cover the stall points and the neighbours.
func TestCteOuterIsReadStepsOverNonWordBytes(t *testing.T) {
	cases := []struct {
		sql  string
		want bool
	}{
		{"WITH c AS (SELECT 1) SELECT * FROM c", true},
		{"with c as (select 1) select * from c", true}, // lowercase keywords
		{"WITH c AS (SELECT 1) INSERT INTO t SELECT * FROM c", false},
		{"WITH c AS (SELECT 1) UPDATE t SET x = 1", false},
		{"WITH c AS (SELECT 1) DELETE FROM t", false},
		{"WITH c AS (SELECT 1) MERGE INTO t USING c ON 1=1", false},
		{"WITH c AS (SELECT 1)\nSELECT * FROM c", true}, // newline at depth 0
		{"WITH c AS (SELECT 1) -- comment\nSELECT * FROM c", true},
		{"WITH x AS (SELECT 1), y AS (SELECT 2) SELECT * FROM x, y", true},
		{"WITH x AS (SELECT 1 /* INSERT */) SELECT * FROM x", true},
		{"WITH x AS (SELECT 'DELETE') SELECT * FROM x", true}, // keyword in string, depth 1
		{"WITH x AS (SELECT 1) /* INSERT", false},             // unclosed block comment
		// String literals: parens and keywords inside quotes must not move
		// the depth counter or match keywords. Pre-fix, '(' in a literal
		// lifted depth and RowLimit silently stopped wrapping the WITH —
		// an unbounded read on exactly the statements the cap exists for.
		{"WITH c AS (SELECT '(' AS x FROM t) SELECT * FROM c", true},
		{"WITH c AS (SELECT ')' AS x FROM t) SELECT * FROM c", true},
		{"WITH c AS (SELECT 'it''s ( fine') SELECT * FROM c", true},
		{"WITH c AS (SELECT 'INSERT (') SELECT * FROM c", true},
		{"WITH c AS (SELECT 1) SELECT * FROM c WHERE x = '(' AND y = 'INSERT'", true},
		// F1 回归：引用标识符里的 DML 关键字不得在 depth 0 被匹配。
		// 此前 cteOuterIsRead 不跳过 "、`、[，WITH "delete" AS (...) 的
		// "delete" 在 depth 0 命中 case "DELETE" → 返回 false → 三个方言
		// 静默不加 LIMIT/TOP/ROWNUM → 大表全量扫描。
		{"WITH \"delete\" AS (SELECT 1) SELECT * FROM big", true},
		{"WITH \"insert\" AS (SELECT 1) SELECT * FROM big", true},
		{"WITH [delete] AS (SELECT 1) SELECT * FROM big", true},
		{"WITH [update] AS (SELECT 1) SELECT * FROM big", true},
		{"WITH `delete` AS (SELECT 1) SELECT * FROM big", true},
		{"WITH `insert` AS (SELECT 1) SELECT * FROM big", true},
		// 引用标识符里的括号不得移动 depth 计数器
		{"WITH \"a(b\" AS (SELECT 1) SELECT * FROM big", true},
		// 引用标识符后紧跟 DML 关键字：关键字在引用外，应返回 false
		{"WITH \"c\" AS (SELECT 1) DELETE FROM big", false},
		// MySQL 反斜杠转义：\' 不应提前闭合字符串字面量
		{"WITH c AS (SELECT 'a\\'b' AS x) SELECT * FROM c", true},
		{"WITH c AS (SELECT 'a\\'b') SELECT * FROM c", true},
		// N1 回归：SQL Server 的 ]] 是字面 ] 的转义（quoteIdentSQLServer
		// 生成侧做 "]"→"]]")，读取侧必须对称。停在第一个 ] 会把标识符内
		// 的关键字当成 depth-0 词：CTE 名 [a]]DELETE]（真名 a]DELETE）曾
		// 被误判"非读"而漏掉封顶；[a]]SELECT] 覆盖在数据修改 CTE 上曾被
		// 误判"读"，包出的派生表无法执行。
		{"WITH [a]]DELETE] AS (SELECT 1) SELECT * FROM big", true},
		{"WITH [a]]UPDATE] AS (SELECT 1) SELECT * FROM big", true},
		{"WITH [a]]INSERT] AS (SELECT 1) SELECT * FROM big", true},
		{"WITH [a]]MERGE] AS (SELECT 1) SELECT * FROM big", true},
		{"WITH [a]]SELECT] AS (INSERT INTO t VALUES (1)) SELECT 1", true},
		// 不含关键字的 ]] 转义名与真实 DML 关键字的负例（防过修）。
		{"WITH [x]]y] AS (SELECT 1) SELECT * FROM big", true},
		{"WITH [a]]b] AS (SELECT 1) DELETE FROM big", false},
	}
	for _, tc := range cases {
		if got := cteOuterIsRead(tc.sql); got != tc.want {
			t.Errorf("cteOuterIsRead(%q) = %v, want %v", tc.sql, got, tc.want)
		}
	}
}

// TestRowLimitParenWrappedWith pins the WITH branch's verdict running on the
// trivia-stripped text: a statement wrapped in leading parentheses keeps its
// outer keyword at paren depth 2 in the raw scan, which reported "no statement
// at depth 0" and left every paren-wrapped read uncapped. The appending
// dialects cap them; the wrap dialects (SQL Server, Oracle) deliberately keep
// the raw scan, because their cap shapes add parens of their own and a
// paren-wrapped WITH has not been shown legal inside them.
func TestRowLimitParenWrappedWith(t *testing.T) {
	wrapped := "((WITH c AS (SELECT 1) SELECT * FROM c))"
	for _, engine := range []string{MySQL, Postgres, SQLite} {
		want := wrapped + " LIMIT 5"
		if got := RowLimit(engine, wrapped, 5); got != want {
			t.Errorf("%s paren-wrapped read WITH must be capped: got %q", engine, got)
		}
	}
	// A data-modifying CTE stays uncapped in every spelling — the outer
	// keyword after the paren strip is the write, and capping would truncate it.
	for _, sql := range []string{
		"((WITH c AS (SELECT 1) INSERT INTO t SELECT * FROM c))",
		"(WITH c AS (SELECT 1) INSERT INTO t SELECT * FROM c)",
	} {
		if got := RowLimit(MySQL, sql, 5); got != sql {
			t.Errorf("paren-wrapped DML CTE must stay uncapped: got %q", got)
		}
	}
	// The wrap dialects keep the raw scan: conservative on a shape their cap
	// has not been proven to carry.
	if got := RowLimit(SQLServer, wrapped, 5); got != wrapped {
		t.Errorf("sqlserver paren-wrapped WITH stays uncapped: got %q", got)
	}
	if got := RowLimit(Oracle, wrapped, 5); got != wrapped {
		t.Errorf("oracle paren-wrapped WITH stays uncapped: got %q", got)
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

// Callers persist their own engine spellings (catalog_test pins "postgresql"
// as a real Connection.Type): a raw-spelling switch rendered an empty template
// or an unsupported error for those. Alias spellings must reach the same arm.
func TestDialectFunctionsAcceptAliasSpellings(t *testing.T) {
	aliasCases := []struct {
		alias, canonical string
	}{
		{"postgresql", Postgres},
		{"pg", Postgres},
		{"mssql", SQLServer},
		{"sql server", SQLServer},
		{"mariadb", MySQL},
	}
	for _, c := range aliasCases {
		if got := CreateTableTemplate(c.alias, ""); got != CreateTableTemplate(c.canonical, "") {
			t.Errorf("CreateTableTemplate(%q) != CreateTableTemplate(%q)", c.alias, c.canonical)
		}
		if _, err := RenameStatement(c.alias, "old", "new", "s", "old", "new"); err != nil {
			t.Errorf("RenameStatement(%q): %v", c.alias, err)
		}
	}
	// 一个真实场景锚点: "postgresql" 的新建表模板必须真的产出 PG 方言。
	if got := CreateTableTemplate("postgresql", ""); got != CreateTableTemplate(Postgres, "") {
		t.Errorf("postgresql template drifted from Postgres: %q", got)
	}
}

func TestFKValuesQuery(t *testing.T) {
	if _, err := FKValuesQuery(MySQL, "t", "k", "", 0); err == nil {
		t.Error("FKValuesQuery must reject limit<=0")
	}
	got, _ := FKValuesQuery(MySQL, "`r`", "k", "label", 10)
	if got != "SELECT DISTINCT `k`, `label` FROM `r` WHERE `k` IS NOT NULL ORDER BY `k` LIMIT 10" {
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

// TypeIsNumeric must answer identically to NewColumnType().IsNumeric() — it is
// the allocation-free face of the same fold, and a hot loop (a 10 000-row
// export asking the same column's type once per row) relies on the two never
// disagreeing.
func TestTypeIsNumericMatchesColumnType(t *testing.T) {
	types := []string{
		"integer", "int", "BIGINT", "smallint", "INT8", "SERIAL", "BIGSERIAL",
		"ROWID", "bigint(20) unsigned", "bigint unsigned", "UNSIGNED BIGINT",
		"unsigned big int", "int unsigned", "big int", "tinyint zerofill",
		" Int ", "decimal(10,2)", "numeric", "float8", "double precision",
		"money", "year",
		"varchar(64)", "text", "date", "timestamp", "uuid", "bool", "json",
		"numeric_unsigned", "char(16)", "", "unsigned", "zerofill",
		"unsigned bigint foo", "POINT", "INTERVAL DAY TO SECOND",
	}
	for _, typ := range types {
		want := NewColumnType(typ).IsNumeric()
		if got := TypeIsNumeric(typ); got != want {
			t.Errorf("TypeIsNumeric(%q) = %v, want %v (NewColumnType().IsNumeric())", typ, got, want)
		}
	}
	if allocs := testing.AllocsPerRun(100, func() {
		TypeIsNumeric("decimal(10,2)")
	}); allocs > 0 {
		t.Errorf("TypeIsNumeric allocated %v times for a lowercase type; "+
			"the hot-path fold must be allocation-free", allocs)
	}
}

// The same declared type arrives with its modifiers in different positions and
// spellings: information_schema.COLUMN_TYPE says "bigint unsigned",
// go-sql-driver's DatabaseTypeName says "UNSIGNED BIGINT", and a hand-written
// DDL may say "BIG INT UNSIGNED ZEROFILL". All of them must fold to one base
// and be answered identically by IsNumeric, IsSeekable and IsInteger — the
// fold used to leave "unsigned big int" and "bigint unsigned" in different
// buckets, so the same column was numeric in one path and text in another.
func TestColumnTypeModifierFoldIsUniform(t *testing.T) {
	// "bigint" and "big int" are two spellings of one MySQL type; both are in
	// the set, so they may fold to different base strings. What must hold is
	// that every spelling of a type answers the three predicates identically.
	groups := [][]string{
		{"bigint", "bigint unsigned", "UNSIGNED BIGINT", "unsigned big int",
			"BIGINT UNSIGNED", "bigint unsigned zerofill"},
		{"big int", "BIG INT", "BIG INT UNSIGNED", "big int unsigned"},
		{"int", "int unsigned", "UNSIGNED INT", "INT UNSIGNED", "INT(11) UNSIGNED"},
		{"tinyint", "tinyint unsigned", "UNSIGNED TINYINT"},
		{"smallint", "smallint unsigned", "UNSIGNED SMALLINT"},
		{"mediumint", "mediumint unsigned", "UNSIGNED MEDIUMINT"},
	}
	for _, group := range groups {
		wantNumeric, wantInteger, wantSeek := NewColumnType(group[0]).IsNumeric(),
			NewColumnType(group[0]).IsInteger(), NewColumnType(group[0]).IsSeekable()
		for _, spelling := range group {
			ct := NewColumnType(spelling)
			if ct.IsNumeric() != wantNumeric || ct.IsInteger() != wantInteger || ct.IsSeekable() != wantSeek {
				t.Errorf("%q: got numeric=%v integer=%v seekable=%v, want %v/%v/%v",
					spelling, ct.IsNumeric(), ct.IsInteger(), ct.IsSeekable(),
					wantNumeric, wantInteger, wantSeek)
			}
			if !ct.IsNumeric() {
				t.Errorf("%q: an integer type must read as numeric", spelling)
			}
			if ct.IsInteger() != ct.IsSeekable() {
				t.Errorf("%q: IsInteger=%v but IsSeekable=%v; the integer half of the gate must imply the seek gate",
					spelling, ct.IsInteger(), ct.IsSeekable())
			}
		}
	}
	// Modifier words must not be mistaken for a type: a column named after one
	// of them is a string, not a number.
	for _, notNumeric := range []string{"unsigned", "zerofill", "unsigned bigint foo"} {
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
		"ROWID", "big int", "bigint(20) unsigned", "UNSIGNED BIGINT",
		"UNSIGNED INT", "unsigned big int", "bigint unsigned", "int unsigned",
		" Int ", "  MEDIUMINT  ", "tinyint zerofill",
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

func TestPlaceholder(t *testing.T) {
	cases := []struct {
		dbType string
		idx    int
		want   string
	}{
		{"mysql", 1, "?"},
		{"mariadb", 3, "?"}, // alias resolves to the mysql dialect
		{"sqlite", 2, "?"},
		{"postgres", 1, "$1"},
		{"pg", 4, "$4"},
		{"mssql", 1, "@p1"},
		{"sqlserver", 2, "@p2"},
		{"oracle", 1, ":1"},
		{"unknown-engine", 5, "?"}, // database/sql default
	}
	for _, c := range cases {
		if got := Placeholder(c.dbType, c.idx); got != c.want {
			t.Errorf("Placeholder(%q, %d) = %q, want %q", c.dbType, c.idx, got, c.want)
		}
	}
}
