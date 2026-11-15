package dbcore_test

// 乐观并发守卫（Red Book ch13 的 lost update）：更新只按主键定位时，另一个写入者
// 改过的行仍然匹配，于是本次更新静默覆盖别人的修改。RowChange.Expect 把端上读到
// 的旧值并进 WHERE，行一旦不再匹配就影响 0 行，ApplyBatch 回 ErrRowConflict 而不
// 是当作"改了 0 行"糊过去。
//
// 这里钉四件事：
//   1. 守卫谓词按方言渲染，NULL 用 IS NULL（= NULL 永不成立）；
//   2. 只守卫「本次真正写入的列」——守无关列会把别人的无关编辑误报成冲突；
//   3. 无 Expect 时语句与改动前逐字节相同（增量改进，不改协议）；
//   4. 冲突是带稳定码的类型化错误，UI 才能区分"别人先改了"与语句失败。

import (
	"errors"
	"strings"
	"testing"

	"github.com/Ben-guanzhi/dbcore"
	"github.com/Ben-guanzhi/dbcore/mysql"
	"github.com/Ben-guanzhi/dbcore/oracle"
	"github.com/Ben-guanzhi/dbcore/postgres"
	"github.com/Ben-guanzhi/dbcore/sqlite"
)

func iv(n int64) dbcore.Value  { return dbcore.Value{Kind: dbcore.ValInt, I: n} }
func tv(s string) dbcore.Value { return dbcore.Value{Kind: dbcore.ValText, S: s} }
func nullv() dbcore.Value      { return dbcore.Value{Kind: dbcore.ValNull} }

func renderUpdate(t *testing.T, d dbcore.Dialect, table string, pk, changes, expect map[string]dbcore.Value) (string, []dbcore.Value) {
	t.Helper()
	return dbcore.BuildUpdateForTest(d, table, pk, changes, expect)
}

func TestExpectGuardRendersNullAwarePredicate(t *testing.T) {
	pk := map[string]dbcore.Value{"id": iv(7)}
	// Both written columns are guarded; `salary` is in Expect (the client read
	// the whole row) but not written, so it must not appear.
	changes := map[string]dbcore.Value{"name": tv("Ada"), "note": tv("hi")}
	expect := map[string]dbcore.Value{"name": tv("Grace"), "note": nullv(), "salary": iv(190000)}

	// PostgreSQL numbers placeholders statement-wide: SET values first, then the
	// WHERE key, then the guard columns in sorted order.
	sql, args := renderUpdate(t, postgres.PostgresDialect{}, "people", pk, changes, expect)
	for _, want := range []string{
		`UPDATE "people" SET "name" = $1, "note" = $2`,
		`WHERE "id" = $3`,
		`"name" = $4`,
		`"note" IS NULL`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("guarded UPDATE lacks %q\n got: %s", want, sql)
		}
	}
	if strings.Contains(sql, "salary") {
		t.Errorf("guard leaked an unwritten column: %s", sql)
	}
	// name(change), note(change), id(pk), name(guard) — note's guard is IS NULL.
	if len(args) != 4 {
		t.Fatalf("args = %d (%v), want 4", len(args), args)
	}
	if got := args[3].String(); got != "Grace" {
		t.Errorf("guard arg = %q, want the read value Grace", got)
	}
}

func TestExpectGuardIsDialectPortable(t *testing.T) {
	// Every dialect must produce a syntactically valid guard with its own
	// placeholder and identifier quoting; nothing may fall back to "= NULL".
	cases := map[string]dbcore.Dialect{
		"mysql":    mysql.MySQLDialect{},
		"postgres": postgres.PostgresDialect{},
		"sqlite":   sqlite.SQLiteDialect{},
		"oracle":   oracle.OracleDialect{},
	}
	for name, d := range cases {
		sql, _ := renderUpdate(t, d, "t",
			map[string]dbcore.Value{"id": iv(1)},
			map[string]dbcore.Value{"v": tv("new")},
			map[string]dbcore.Value{"v": nullv()})
		if !strings.Contains(sql, "IS NULL") {
			t.Errorf("%s: NULL guard must render IS NULL, got %s", name, sql)
		}
		if strings.Contains(sql, "= NULL") {
			t.Errorf("%s: rendered '= NULL', which never matches: %s", name, sql)
		}
	}
}

func TestExpectOnlyGuardsWrittenColumns(t *testing.T) {
	pk := map[string]dbcore.Value{"id": iv(1)}
	changes := map[string]dbcore.Value{"a": tv("x")}
	expect := map[string]dbcore.Value{"a": tv("old"), "b": tv("untouched")}

	sql, args := renderUpdate(t, sqlite.SQLiteDialect{}, "t", pk, changes, expect)
	if strings.Contains(sql, `"b"`) {
		t.Errorf("guard leaked an unwritten column into WHERE: %s", sql)
	}
	// change(a), pk(id), guard(a)
	if len(args) != 3 {
		t.Errorf("args = %d (%v), want 3", len(args), args)
	}
}

func TestUnguardedUpdateIsUnchanged(t *testing.T) {
	// Nil Expect must reproduce the pre-guard statement byte for byte, so
	// clients that do not track old values see no behaviour change at all.
	pk := map[string]dbcore.Value{"id": iv(1)}
	changes := map[string]dbcore.Value{"v": tv("x")}

	sql, args := renderUpdate(t, sqlite.SQLiteDialect{}, "t", pk, changes, nil)
	want := `UPDATE "t" SET "v" = ? WHERE "id" = ?`
	if sql != want {
		t.Errorf("unguarded UPDATE =\n%s\nwant\n%s", sql, want)
	}
	if len(args) != 2 {
		t.Errorf("args = %d, want 2", len(args))
	}
}

func TestRowConflictIsTyped(t *testing.T) {
	// The UI needs to tell "someone else changed this row" from a statement
	// error; that is what the stable code is for.
	if !errors.Is(dbcore.ErrRowConflict, dbcore.ErrRowConflict) {
		t.Fatal("ErrRowConflict must match itself with errors.Is")
	}
	var de *dbcore.Error
	if !errors.As(dbcore.ErrRowConflict, &de) || de.Code != dbcore.ErrCodeRowConflict {
		t.Fatalf("ErrRowConflict = %#v, want a *dbcore.Error with code %s",
			dbcore.ErrRowConflict, dbcore.ErrCodeRowConflict)
	}
}
