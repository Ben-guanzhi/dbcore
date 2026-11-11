package sqltext

import (
	"strings"
	"testing"
)

// indexColumns is how many values the readers of IndexesQuery scan per row:
// name, kind, is-unique, is-primary, column. Both TablePro clients declare five
// destinations - the root repo's tableIndexes scans five, and the Fyne client
// keeps the same shape in its dialect table - so a statement with fewer fails
// every table on that engine. The count is asserted here rather than discovered
// against a server.
const indexColumns = 5

func TestIndexesQuerySelectsWhatItsCallersScan(t *testing.T) {
	cases := []struct {
		dbType         string
		database       string
		schema         string
		wantEmptyQuery bool
	}{
		{MySQL, "zs", "zs", false},
		{Postgres, "zs", "public", false},
		{SQLServer, "zs", "dbo", false},
		{Oracle, "zs", "SCOTT", false},
		// Pragmas cannot be joined, so SQLite is answered by the caller.
		{SQLite, "own", "", true},
	}
	for _, c := range cases {
		query, args, err := IndexesQuery(c.dbType, c.database, c.schema, "orders")
		if err != nil {
			t.Fatalf("%s: %v", c.dbType, err)
		}
		if c.wantEmptyQuery {
			if query != "" {
				t.Errorf("%s: SQLite is the caller's to read, got %s", c.dbType, query)
			}
			continue
		}
		list := selectList(t, c.dbType, query)
		if got := len(splitTopLevel(list)); got != indexColumns {
			t.Errorf("%s selects %d columns, its callers scan %d: %s", c.dbType, got, indexColumns, query)
		}
		// A statement that binds the table by placeholder must also hand the
		// caller the values for them; the ones that inline a literal bind none.
		wantArgs := strings.Count(query, "?") + strings.Count(query, "$")
		if len(args) != wantArgs {
			t.Errorf("%s binds %d args but the statement has %d placeholders: %s", c.dbType, len(args), wantArgs, query)
		}
	}

	if _, _, err := IndexesQuery("access", "", "", "t"); err == nil {
		t.Error("an engine with no index catalog must be refused, not answered with an empty statement")
	}
}

// The MySQL catalog names the opposite of what the callers scan, so that branch
// has to derive the flag: NON_UNIQUE=1 is a plain index, and passing it through
// reports every plain index as UNIQUE.
func TestIndexesQueryAnswersIsUniqueNotNonUnique(t *testing.T) {
	for _, dbType := range []string{MySQL, Postgres, SQLServer, Oracle} {
		query, _, err := IndexesQuery(dbType, "zs", "dbo", "orders")
		if err != nil {
			t.Fatalf("%s: %v", dbType, err)
		}
		columns := splitTopLevel(selectList(t, dbType, query))
		uniqueness := strings.Join(strings.Fields(columns[2]), " ")
		if !strings.Contains(uniqueness, "CASE") && !strings.Contains(uniqueness, "CAST") {
			t.Errorf("%s: the uniqueness column is the bare flag %q, want an expression", dbType, uniqueness)
		}
		if strings.Contains(strings.ToUpper(uniqueness), "NON_UNIQUE = 1") {
			t.Errorf("%s: uniqueness reads %q, which is inverted", dbType, uniqueness)
		}
	}

	query, _, err := IndexesQuery(MySQL, "zs", "zs", "orders")
	if err != nil {
		t.Fatalf("mysql: %v", err)
	}
	if !strings.Contains(query, "CASE WHEN NON_UNIQUE = 0 THEN 1 ELSE 0 END") {
		t.Errorf("mysql reports %q, want the flag derived from NON_UNIQUE = 0", query)
	}
}

// The two shapes this guard exists to refuse, written out so the checks above
// are seen to have teeth: a server is not needed to show that a four-column
// statement fails the scan and that NON_UNIQUE names the opposite flag.
// db_ 前缀的 9 个系统角色必须按精确名单排除，而不是 NOT LIKE 'db[_]%'——
// 否则 db_admin 这类用户 schema 会被误藏（2026-10-25 修复）。
func TestSchemasQueryExcludesSystemRolesByNameNotPrefix(t *testing.T) {
	q, err := SchemasQuery(SQLServer, "db")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(q, "db[_]") {
		t.Errorf("SchemasQuery 不应再用前缀过滤 db_ schema:\n%s", q)
	}
	for _, role := range []string{
		"db_owner", "db_accessadmin", "db_securityadmin", "db_ddladmin",
		"db_backupoperator", "db_datareader", "db_datawriter",
		"db_denydatareader", "db_denydatawriter",
	} {
		if !strings.Contains(q, "'"+role+"'") {
			t.Errorf("SchemasQuery 缺系统角色 %s 的精确排除:\n%s", role, q)
		}
	}
}

func TestTheIndexShapeGuardRefusesTheShapesItGuardsAgainst(t *testing.T) {
	old := `SELECT INDEX_NAME, INDEX_TYPE, NON_UNIQUE, COLUMN_NAME
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`
	columns := splitTopLevel(selectList(t, "mysql (old)", old))
	if len(columns) != 4 {
		t.Fatalf("the old statement counted as %d columns, want 4: %v", len(columns), columns)
	}
	if got := len(splitTopLevel(selectList(t, "mysql", mustQuery(t, MySQL)))); got != indexColumns {
		t.Errorf("the shipped statement counted %d, want %d", got, indexColumns)
	}
	if bare := strings.Join(strings.Fields(columns[2]), " "); strings.Contains(bare, "CASE") {
		t.Errorf("the old uniqueness column %q was already derived, so this proves nothing", bare)
	}
}

func mustQuery(t *testing.T, dbType string) string {
	t.Helper()
	query, _, err := IndexesQuery(dbType, "zs", "zs", "orders")
	if err != nil {
		t.Fatalf("%s: %v", dbType, err)
	}
	return query
}

// selectList returns the text between the first SELECT and the FROM that belongs
// to it, so a caller can count or name the columns without parsing the whole
// statement.
func selectList(t *testing.T, dbType, query string) string {
	t.Helper()
	upper := strings.ToUpper(query)
	start := strings.Index(upper, "SELECT ")
	if start < 0 {
		t.Fatalf("%s: no SELECT in %s", dbType, query)
	}
	body := query[start+len("SELECT "):]
	depth, quote := 0, byte(0)
	for i := 0; i < len(body); i++ {
		ch := body[i]
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if ch == '(' {
			depth++
			continue
		}
		if ch == ')' {
			depth--
			continue
		}
		if depth != 0 || i+4 > len(body) || strings.ToUpper(body[i:i+4]) != "FROM" {
			continue
		}
		beforeIsSpace := i == 0 || isSpaceByte(body[i-1])
		afterIsSpace := i+4 == len(body) || isSpaceByte(body[i+4])
		if !beforeIsSpace || !afterIsSpace {
			continue
		}
		return body[:i]
	}
	t.Fatalf("%s: no top-level FROM in %s", dbType, query)
	return ""
}

// splitTopLevel splits a column list on the commas that separate columns,
// leaving the commas of CAST(x AS int) and 'a,b' inside their own segment.
func splitTopLevel(list string) []string {
	out := []string{}
	segment := strings.Builder{}
	depth, quote := 0, byte(0)
	for i := 0; i <= len(list); i++ {
		end := i == len(list)
		if !end {
			ch := list[i]
			segment.WriteByte(ch)
			switch {
			case quote != 0:
				if ch == quote {
					quote = 0
				}
			case ch == '\'' || ch == '"':
				quote = ch
			case ch == '(':
				depth++
			case ch == ')':
				depth--
			}
		}
		if end || (list[i] == ',' && depth == 0 && quote == 0) {
			if text := strings.TrimSpace(segment.String()); text != "" {
				out = append(out, text)
			}
			segment.Reset()
		}
	}
	return out
}

func isSpaceByte(ch byte) bool {
	return ch == ' ' || ch == '\n' || ch == '\t' || ch == '\r'
}

// 替代拼写回归：客户端会持久化多种引擎拼写（"postgres"/"postgresql"、
// "mssql"/"sqlserver"、"mariadb"/"mysql"），所有目录 SQL 生成器必须在
// 入口 Normalize，否则整族 API 会因拼写不一致而报 unsupported type。
func TestCatalogQueriesNormalizeAlternativeSpellings(t *testing.T) {
	altPairs := [][2]string{
		{"postgres", Postgres},
		{"pg", Postgres},
		{"mssql", SQLServer},
		{"sqlserver", SQLServer},
		{"sql server", SQLServer},
		{"mariadb", MySQL},
	}

	for _, pair := range altPairs {
		alt, canonical := pair[0], pair[1]

		q1, err1 := DatabasesQuery(alt)
		q2, err2 := DatabasesQuery(canonical)
		if err1 != nil || err2 != nil {
			t.Errorf("DatabasesQuery(%q): err=%v, err=%v", alt, err1, err2)
			continue
		}
		if q1 != q2 {
			t.Errorf("DatabasesQuery(%q) = %q, want %q", alt, q1, q2)
		}

		s1, err1 := SchemasQuery(alt, "db")
		s2, err2 := SchemasQuery(canonical, "db")
		if err1 != nil || err2 != nil {
			t.Errorf("SchemasQuery(%q): err=%v, err=%v", alt, err1, err2)
			continue
		}
		if s1 != s2 {
			t.Errorf("SchemasQuery(%q) = %q, want %q", alt, s1, s2)
		}

		t1, _, err1 := TablesQuery(alt, "db", "sch", "", false)
		t2, _, err2 := TablesQuery(canonical, "db", "sch", "", false)
		if err1 != nil || err2 != nil {
			t.Errorf("TablesQuery(%q): err=%v, err=%v", alt, err1, err2)
			continue
		}
		if t1 != t2 {
			t.Errorf("TablesQuery(%q) = %q, want %q", alt, t1, t2)
		}

		c1, _, err1 := ColumnsQuery(alt, "db", "sch", "tbl")
		c2, _, err2 := ColumnsQuery(canonical, "db", "sch", "tbl")
		if err1 != nil || err2 != nil {
			t.Errorf("ColumnsQuery(%q): err=%v, err=%v", alt, err1, err2)
			continue
		}
		if c1 != c2 {
			t.Errorf("ColumnsQuery(%q) = %q, want %q", alt, c1, c2)
		}

		i1, _, err1 := IndexesQuery(alt, "db", "sch", "tbl")
		i2, _, err2 := IndexesQuery(canonical, "db", "sch", "tbl")
		if err1 != nil || err2 != nil {
			t.Errorf("IndexesQuery(%q): err=%v, err=%v", alt, err1, err2)
			continue
		}
		if i1 != i2 {
			t.Errorf("IndexesQuery(%q) = %q, want %q", alt, i1, i2)
		}

		f1, _, err1 := ForeignKeysQuery(alt, "db", "sch", "tbl")
		f2, _, err2 := ForeignKeysQuery(canonical, "db", "sch", "tbl")
		if err1 != nil || err2 != nil {
			t.Errorf("ForeignKeysQuery(%q): err=%v, err=%v", alt, err1, err2)
			continue
		}
		if f1 != f2 {
			t.Errorf("ForeignKeysQuery(%q) = %q, want %q", alt, f1, f2)
		}

		fc1, _, err1 := FullColumnTypesQuery(alt, "db", "sch", "tbl")
		fc2, _, err2 := FullColumnTypesQuery(canonical, "db", "sch", "tbl")
		if err1 != nil || err2 != nil {
			t.Errorf("FullColumnTypesQuery(%q): err=%v, err=%v", alt, err1, err2)
			continue
		}
		if fc1 != fc2 {
			t.Errorf("FullColumnTypesQuery(%q) = %q, want %q", alt, fc1, fc2)
		}

		p1, _, err1 := PreviewPageQuery(alt, "db", "sch", "tbl", "", "", 10, 0)
		p2, _, err2 := PreviewPageQuery(canonical, "db", "sch", "tbl", "", "", 10, 0)
		if err1 != nil || err2 != nil {
			t.Errorf("PreviewPageQuery(%q): err=%v, err=%v", alt, err1, err2)
			continue
		}
		if p1 != p2 {
			t.Errorf("PreviewPageQuery(%q) = %q, want %q", alt, p1, p2)
		}
	}
}

// P1-2 回归：FullColumnTypesQuery 的 SQL Server 分支必须对 nchar/nvarchar
// 的 CHARACTER_MAXIMUM_LENGTH 除 2（UTF-16 字节长度），char/varchar 不除。
func TestFullColumnTypesQueryMSSQLUnicodeHalf(t *testing.T) {
	q, _, err := FullColumnTypesQuery(SQLServer, "db", "sch", "tbl")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q, "CHARACTER_MAXIMUM_LENGTH / 2") {
		t.Error("MSSQL FullColumnTypesQuery must divide unicode length by 2")
	}
	if !strings.Contains(q, "'nchar'") {
		t.Error("MSSQL FullColumnTypesQuery must cover nchar")
	}
	// nchar 不能留在不除 2 的首个分支。
	firstBranch := strings.SplitN(q, "WHEN", 3)[1]
	if strings.Contains(firstBranch, "'nchar'") {
		t.Error("nchar must not sit in the non-halving branch")
	}
}
