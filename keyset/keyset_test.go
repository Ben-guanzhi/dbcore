package keyset

import (
	"testing"

	"github.com/dbcore/dbcore"
)

func keys(t *testing.T, cols ...dbcore.ColumnInfo) []Key {
	t.Helper()
	return Keys("postgresql", cols, nil)
}

func TestKeysOnlySeeksWholeNumbersAndText(t *testing.T) {
	k := keys(t,
		dbcore.ColumnInfo{Name: "id", Type: "bigint", IsPrimaryKey: true},
	)
	if len(k) != 1 || k[0].Kind != Integer || k[0].Quoted != `"id"` {
		t.Fatalf("keys = %+v", k)
	}
	// A float primary key cannot round-trip through a text cursor.
	if got := keys(t, dbcore.ColumnInfo{Name: "id", Type: "double precision", IsPrimaryKey: true}); got != nil {
		t.Fatalf("float key should be refused, got %+v", got)
	}
	// A catalog read error means no key at all (the page falls back to OFFSET).
	if got := Keys("postgresql", []dbcore.ColumnInfo{{Name: "id", Type: "bigint", IsPrimaryKey: true}}, errAny{}); got != nil {
		t.Fatalf("catalog error should yield no keys, got %+v", got)
	}
	// SQL Server/Oracle are deliberately excluded.
	if got := Keys("sqlserver", []dbcore.ColumnInfo{{Name: "id", Type: "bigint", IsPrimaryKey: true}}, nil); got != nil {
		t.Fatalf("mssql should not seek, got %+v", got)
	}
}

func TestWhereExpandsCompositeKeys(t *testing.T) {
	k := []Key{
		{Name: "region", Quoted: `"region"`, Kind: Text},
		{Name: "id", Quoted: `"id"`, Kind: Integer},
	}
	where, args, err := Where("postgresql", k, []string{"east", "41"}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := `("region" > $1 OR ("region" = $2 AND "id" > $3))`
	if where != want {
		t.Fatalf("where = %q, want %q", where, want)
	}
	if len(args) != 3 {
		t.Fatalf("args = %v", args)
	}
	// Backwards flips the comparison so the rows nearest the cursor come first.
	back, _, err := Where("postgresql", k, []string{"east", "41"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := `("region" < $1 OR ("region" = $2 AND "id" < $3))`; back != want {
		t.Fatalf("backward = %q, want %q", back, want)
	}
}

func TestWhereRefusesMismatchedOrUnusableCursors(t *testing.T) {
	k := []Key{{Name: "id", Quoted: `"id"`, Kind: Integer}}
	if _, _, err := Where("postgresql", k, []string{"1", "2"}, false); err == nil {
		t.Fatal("cursor arity must match the key")
	}
	if _, _, err := Where("postgresql", k, []string{"not-a-number"}, false); err == nil {
		t.Fatal("a non-numeric cursor for an integer key must be refused")
	}
	text := []Key{{Name: "s", Quoted: `"s"`, Kind: Text}}
	if _, _, err := Where("postgresql", text, []string{"a\x00b"}, false); err == nil {
		t.Fatal("NUL in a text cursor must be refused")
	}
}

func TestOrderAndCursors(t *testing.T) {
	k := []Key{{Name: "id", Quoted: `"id"`, Kind: Integer}}
	if got := Order(k, false); got != ` ORDER BY "id"` {
		t.Fatalf("order = %q", got)
	}
	if got := Order(k, true); got != ` ORDER BY "id" DESC` {
		t.Fatalf("backward order = %q", got)
	}

	rows := [][]interface{}{{int64(1)}, {int64(2)}}
	start, end := PageCursors(rows, k, []string{"id"})
	if start == nil || end == nil || start[0] != "1" || end[0] != "2" {
		t.Fatalf("cursors = %v / %v", start, end)
	}
	// A result that does not carry the key column yields no cursors.
	if s, e := PageCursors(rows, k, []string{"other"}); s != nil || e != nil {
		t.Fatalf("missing key column should disable cursors: %v / %v", s, e)
	}
	// Column names match case-insensitively (drivers fold differently).
	if s, e := PageCursors(rows, k, []string{"ID"}); s == nil || e == nil {
		t.Fatalf("case-insensitive key column should yield cursors: %v / %v", s, e)
	}

	rows = [][]interface{}{{int64(1)}, {int64(2)}}
	ReversePage(rows)
	if rows[0][0].(int64) != 2 {
		t.Fatalf("reverse = %v", rows)
	}
}

func TestSeekableKeyTypeUsesKernelTypeFolding(t *testing.T) {
	cases := map[string]Kind{
		"integer":           Integer,
		"bigint":            Integer,
		"character varying": Text,
		"double precision":  NotSeekable,
		"timestamp":         NotSeekable,
	}
	for typ, want := range cases {
		if got := SeekableKeyType(typ); got != want {
			t.Errorf("%s = %v, want %v", typ, got, want)
		}
	}
}

type errAny struct{}

func (errAny) Error() string { return "catalog unreadable" }
