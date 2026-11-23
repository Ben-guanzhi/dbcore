package mongo

// mongo_test.go — the driver's pure logic: the DSN it renders, the command
// language it accepts, the LIKE pattern it converts, and the filter it
// refuses. The live half (mongo_live_test.go) needs a server.

import (
	"testing"

	"github.com/Ben-guanzhi/dbcore"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// rawInt and rawString build BSON values for the type-mapping test.
func rawInt(i int32) bson.RawValue {
	return rawValue(i)
}

func rawString(s string) bson.RawValue {
	return rawValue(s)
}

// rawValue marshals a Go value into a BSON value.
func rawValue(v any) bson.RawValue {
	typ, data, err := bson.MarshalValue(v)
	if err != nil {
		panic(err)
	}
	return bson.RawValue{Type: typ, Value: data}
}

func TestDSNCarriesTheCredentialsAndDatabase(t *testing.T) {
	spec := dbcore.ConnSpec{
		Kind:     dbcore.KindMongo,
		Host:     "db.local",
		Port:     27017,
		User:     "app",
		Password: "s3cret",
		Database: "novelgen",
	}
	dsn := DSN(spec)
	for _, want := range []string{
		"mongodb://app:s3cret@db.local:27017",
		"database=novelgen",
	} {
		if !contains(dsn, want) {
			t.Fatalf("DSN %q is missing %q", dsn, want)
		}
	}
	// The auth source rides the schema when the engine needs one.
	spec.Schema = "admin"
	if !contains(DSN(spec), "authSource=admin") {
		t.Fatalf("DSN %q has no authSource", DSN(spec))
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func TestParseMongoCommand(t *testing.T) {
	cases := []struct {
		in    string
		verb  string
		arg   string
		db    string
		limit int
	}{
		{"list", "list", "", "", 0},
		{"LIST", "list", "", "", 0},
		{"list novelgen", "list", "novelgen", "", 0},
		{"count chapters", "count", "chapters", "", 0},
		{"find chapters", "find", "chapters", "", 0},
		{"find chapters limit 10", "find", "chapters", "", 10},
		{"find novelgen.chapters", "find", "chapters", "novelgen", 0},
	}
	for _, tc := range cases {
		cmd, err := parseMongoCommand(tc.in)
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if cmd.verb != tc.verb || cmd.arg != tc.arg || cmd.db != tc.db || cmd.limit != tc.limit {
			t.Errorf("%q parsed as %+v", tc.in, cmd)
		}
	}
}

func TestParseMongoCommandRefusesTheUnknown(t *testing.T) {
	for _, in := range []string{"", "drop everything", "count", "find chapters limit many"} {
		if _, err := parseMongoCommand(in); err == nil {
			t.Errorf("%q was accepted", in)
		}
	}
}

func TestLikeToRegex(t *testing.T) {
	got, err := likeToRegex("a%c_d")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if got != "a.*c.d" {
		t.Fatalf("pattern = %q", got)
	}
	// A dot in the pattern is a literal, not "any character".
	got, _ = likeToRegex("a.b")
	if got != `a\.b` {
		t.Fatalf("pattern = %q", got)
	}
}

func TestMongoFilterRefusesWhatItCannotExpress(t *testing.T) {
	// An expression the mapping cannot express is refused rather than
	// dropped: dropping it would return rows the caller did not ask for.
	_, err := mongoFilter([]dbcore.FilterExpr{
		{Column: "a", Op: dbcore.FilterOp("~~"), Value: dbcore.Value{Kind: dbcore.ValText, S: "x"}},
	})
	if err == nil {
		t.Fatal("an unknown filter operator was accepted")
	}
	// The supported ones map.
	out, err := mongoFilter([]dbcore.FilterExpr{
		{Column: "a", Op: dbcore.FilterEq, Value: dbcore.Value{Kind: dbcore.ValInt, I: 3}},
		{Column: "b", Op: dbcore.FilterNeq, Value: dbcore.Value{Kind: dbcore.ValText, S: "x"}},
		{Column: "c", Op: dbcore.FilterGt, Value: dbcore.Value{Kind: dbcore.ValInt, I: 1}},
	})
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("filter = %+v", out)
	}
}

func TestMongoSortDirection(t *testing.T) {
	keys, err := mongoSort([]dbcore.SortKey{{Column: "a"}, {Column: "b", Desc: true}})
	if err != nil {
		t.Fatalf("sort: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("sort = %+v", keys)
	}
	if keys[0].Value != 1 || keys[1].Value != -1 {
		t.Fatalf("directions = %+v", keys)
	}
}

func TestMongoTypeOf(t *testing.T) {
	// The type vocabulary is the kernel's; a grid reads it.
	if got := mongoTypeOf(rawInt(3)); got != "INTEGER" {
		t.Fatalf("int type = %q", got)
	}
	if got := mongoTypeOf(rawString("x")); got != "TEXT" {
		t.Fatalf("string type = %q", got)
	}
}
