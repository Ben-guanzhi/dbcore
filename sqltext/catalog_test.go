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
