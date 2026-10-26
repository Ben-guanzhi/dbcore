package dbcore

import "testing"

func TestNormalizeFKAction(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"NO ACTION":   FKActionNoAction,
		"NO_ACTION":   FKActionNoAction,
		"no action ":  FKActionNoAction,
		"CASCADE":     FKActionCascade,
		"cascade":     FKActionCascade,
		"SET NULL":    FKActionSetNull,
		"SET_NULL":    FKActionSetNull,
		"SET DEFAULT": FKActionSetDefault,
		"RESTRICT":    FKActionRestrict,
	}
	for in, want := range cases {
		if got := NormalizeFKAction(in); got != want {
			t.Errorf("NormalizeFKAction(%q) = %q, want %q", in, got, want)
		}
	}
}

// A JOIN projecting two same-named columns must yield distinct display keys,
// and a suffix that collides with a real column must keep bumping rather than
// producing a third duplicate.
func TestDisambiguateColumnNames(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{[]string{"id", "name"}, []string{"id", "name"}},
		{[]string{"id", "id"}, []string{"id", "id_2"}},
		{[]string{"id", "id", "id"}, []string{"id", "id_2", "id_3"}},
		// Case-insensitive: engines fold unquoted identifiers.
		{[]string{"ID", "id"}, []string{"ID", "id_2"}},
		// A real id_2 already present forces the duplicate past it.
		{[]string{"id", "id_2", "id"}, []string{"id", "id_2", "id_3"}},
		// Empty names are engine noise, not collisions worth suffixing.
		{[]string{"", ""}, []string{"", "_2"}},
	}
	for _, c := range cases {
		cols := make([]ColumnInfo, len(c.in))
		for i, n := range c.in {
			cols[i] = ColumnInfo{Name: n, Type: "text"}
		}
		got := DisambiguateColumnNames(cols)
		for i, want := range c.want {
			if got[i].Name != want {
				t.Errorf("%v -> %v, want %v (at %d)", c.in, names(got), c.want, i)
				break
			}
		}
		// Everything but the name survives: a display key is not a rename.
		for _, col := range got {
			if col.Type != "text" {
				t.Errorf("%v: Type lost on %q", c.in, col.Name)
			}
		}
	}
}

func names(cols []ColumnInfo) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.Name
	}
	return out
}
