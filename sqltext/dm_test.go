package sqltext

import (
	"strings"
	"testing"
)

// W14: 达梦 DM8.
//
// The engine is a KNOWN dialect and an UNKNOWN connection. These tests pin both
// halves of that sentence, because the failure mode they guard against is the
// kernel quietly claiming more than it can back: a DM connection that produces
// Oracle-looking SQL is useful, a DM connection that claims to support DDL
// generators nobody has ever run against a server is a lie a client will act on.

func TestNormalizeKnowsDM(t *testing.T) {
	for _, spelling := range []string{"dm", "DM", "dameng", "dm8", "DM7", " dm6 "} {
		if got := Normalize(spelling); got != DM {
			t.Fatalf("Normalize(%q) = %q, want %q", spelling, got, DM)
		}
	}
}

func TestDMDialectMatchesOracle(t *testing.T) {
	// DM8's default Oracle-compatibility mode takes the same identifier quoting,
	// ":n" binds and ROWNUM limiting. If these ever diverge, the shared dialect is
	// wrong and DM needs its own. The Dialect holds FUNCTIONS, so the comparison is
	// behavioural: run both and compare the text.
	dm, ok := DialectFor("dameng")
	if !ok {
		t.Fatal("DM has no dialect")
	}
	oracle, ok := DialectFor("oracle")
	if !ok {
		t.Fatal("Oracle has no dialect")
	}
	if got, want := dm.RowLimit("SELECT * FROM t", 5), oracle.RowLimit("SELECT * FROM t", 5); got != want {
		t.Fatalf("row limit differs:\n dm    = %q\n oracle = %q", got, want)
	}
	if got, want := dm.QuoteLiteral("x'y"), oracle.QuoteLiteral("x'y"); got != want {
		t.Fatalf("literal quoting differs: %q vs %q", got, want)
	}
	if dm.KeysetEnabled != oracle.KeysetEnabled {
		t.Fatal("keyset support differs")
	}
}

func TestDMQuotingIsOracleStyle(t *testing.T) {
	// Double quotes, "" escaping, and the same rejection of control characters.
	got, err := QuoteIdent(DM, `we"ird`)
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if got != `"we""ird"` {
		t.Fatalf("quoted = %q", got)
	}
	if _, err := QuoteIdent(DM, "bad\nname"); err == nil {
		t.Fatal("a control character in an identifier must be refused")
	}
}

func TestDMPlaceholderIsOracleStyle(t *testing.T) {
	if got := Placeholder(DM, 3); got != ":3" {
		t.Fatalf("placeholder = %q, want :3", got)
	}
	// And through an alias spelling, which is what a client persists.
	if got := Placeholder("dameng", 1); got != ":1" {
		t.Fatalf("alias placeholder = %q", got)
	}
}

func TestDMRowLimitUsesRownum(t *testing.T) {
	// A DM read is limited the Oracle way, not with LIMIT.
	d, ok := DialectFor("dm")
	if !ok {
		t.Fatal("DM has no dialect")
	}
	stmt := d.RowLimit("SELECT * FROM t", 10)
	if strings.Contains(strings.ToUpper(stmt), " LIMIT ") {
		t.Fatalf("DM must not be limited with LIMIT: %q", stmt)
	}
	if !strings.Contains(strings.ToUpper(stmt), "ROWNUM") {
		t.Fatalf("expected a ROWNUM limit, got %q", stmt)
	}
}

func TestDMIsNotAdvertisedAsFullySupported(t *testing.T) {
	// Engines is the kernel's promise that every listed engine answers every
	// operation (TestEveryEngineAnswersEveryOperation enforces it). DM answers the
	// DIALECT operations only, so it must stay out of that list until its DDL has
	// been checked against a real server.
	for _, engine := range Engines {
		if engine == DM {
			t.Fatal("DM must not be listed as a fully supported engine")
		}
	}
}

func TestDMRefusesCatalogRecipesItHasNotVerified(t *testing.T) {
	// DM's data dictionary is its own. The recipes below ask Oracle's catalog
	// questions, and answering them for DM would mean guessing object names in a
	// dictionary nobody here has queried.
	for _, probe := range []struct {
		name string
		call func() error
	}{
		{"InvalidObjectsQuery", func() error {
			_, _, err := InvalidObjectsQuery("dm", "SYSDBA")
			return err
		}},
		{"ObjectDependenciesQuery", func() error {
			_, _, err := ObjectDependenciesQuery("dm", "SYSDBA", "P")
			return err
		}},
		{"CompileErrorsQuery", func() error {
			_, _, err := CompileErrorsQuery("dm", "SYSDBA", "P")
			return err
		}},
		{"SequencesQuery", func() error {
			_, _, err := SequencesQuery("dm", "SYSDBA")
			return err
		}},
	} {
		if err := probe.call(); err == nil {
			t.Fatalf("%s should refuse DM rather than emit an Oracle catalog query", probe.name)
		}
	}
}

func TestDMRefusesCompileStatements(t *testing.T) {
	// ALTER … COMPILE is Oracle's grammar; whether DM8 accepts it in compatibility
	// mode is unverified, so the recipe refuses rather than emitting a statement
	// nobody tested.
	if _, err := CompileStatement("dm", "SYSDBA", "PROCEDURE", "P"); err == nil {
		t.Fatal("DM must not emit an ALTER ... COMPILE")
	}
}
