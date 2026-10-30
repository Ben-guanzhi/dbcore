package dbcore_test

import (
	"strings"
	"testing"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/mysql"
	"github.com/dbcore/dbcore/mssql"
	"github.com/dbcore/dbcore/oracle"
	"github.com/dbcore/dbcore/postgres"
	"github.com/dbcore/dbcore/sqlite"
)

// TestFilterIsNullContract pins the IS NULL / IS NOT NULL contract across
// all five dialects: dbcore.FilterIsNull and dbcore.FilterIsNotNull must render as
// "col IS NULL" / "col IS NOT NULL" with NO placeholder and NO bound value.
// The pre-fix implementation wrote "col is null ?" and bound the value,
// producing invalid SQL on every engine.
func TestFilterIsNullContract(t *testing.T) {
	type tc struct {
		name  string
		build func(f dbcore.FilterExpr) (string, []dbcore.Value)
	}
	cases := []tc{
		{"mysql", func(f dbcore.FilterExpr) (string, []dbcore.Value) {
			d := mysql.MySQLDialect{}
			return d.BuildRowsQuery("app", "users", &dbcore.TableRowsRequest{Filter: []dbcore.FilterExpr{f}})
		}},
		{"postgres", func(f dbcore.FilterExpr) (string, []dbcore.Value) {
			d := postgres.PostgresDialect{}
			return d.BuildRowsQuery("app", "users", &dbcore.TableRowsRequest{Filter: []dbcore.FilterExpr{f}})
		}},
		{"sqlite", func(f dbcore.FilterExpr) (string, []dbcore.Value) {
			d := sqlite.SQLiteDialect{}
			return d.BuildRowsQuery("app", "users", &dbcore.TableRowsRequest{Filter: []dbcore.FilterExpr{f}})
		}},
		{"mssql", func(f dbcore.FilterExpr) (string, []dbcore.Value) {
			d := mssql.MSSQLDialect{}
			return d.BuildRowsQuery("app", "users", &dbcore.TableRowsRequest{Filter: []dbcore.FilterExpr{f}})
		}},
		{"oracle", func(f dbcore.FilterExpr) (string, []dbcore.Value) {
			d := oracle.OracleDialect{}
			return d.BuildRowsQuery("app", "users", &dbcore.TableRowsRequest{Filter: []dbcore.FilterExpr{f}})
		}},
	}

	for _, c := range cases {
		for _, op := range []dbcore.FilterOp{dbcore.FilterIsNull, dbcore.FilterIsNotNull} {
			q, args := c.build(dbcore.FilterExpr{Column: "email", Op: op})
			want := "IS NULL"
			if op == dbcore.FilterIsNotNull {
				want = "IS NOT NULL"
			}
			if !strings.Contains(strings.ToUpper(q), want) {
				t.Errorf("%s %s: query %q does not contain %q", c.name, op, q, want)
			}
			// No placeholder should appear for IS NULL / IS NOT NULL.
			if len(args) != 0 {
				t.Errorf("%s %s: args = %v, want empty (IS NULL binds no value)", c.name, op, args)
			}
			// The value field must not leak into the SQL.
			if strings.Contains(q, "?") && op == dbcore.FilterIsNull {
				t.Errorf("%s %s: query %q contains a placeholder for IS NULL", c.name, op, q)
			}
		}
	}
}
