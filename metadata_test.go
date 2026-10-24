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
