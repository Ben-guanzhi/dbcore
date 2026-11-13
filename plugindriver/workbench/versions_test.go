package workbench

// §6.6 acceptance: integer versions on declarations, host-side resolution, and a
// refusal that NAMES the gap. Multi-version coexistence is not supported, so the
// only honest answers are "satisfied" or "upgrade the provider".

import (
	"strings"
	"testing"
)

func TestCapabilityVersionValidation(t *testing.T) {
	base := Banner{
		Stream: StreamFrameV1, PluginID: "a", ProviderID: "a-1",
		Provides: []Capability{{Key: "k", Commutative: true, Version: 2}},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("a provided version must validate: %v", err)
	}
	// A provided key cannot carry a minimum: that field belongs to dependencies.
	bad := base
	bad.Provides = []Capability{{Key: "k", Commutative: true, MinVersion: 2}}
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "minimum version") {
		t.Fatalf("a provided MinVersion must be refused: %v", err)
	}
	// Negative versions are refused on both sides.
	neg := base
	neg.Provides = []Capability{{Key: "k", Commutative: true, Version: -1}}
	if err := neg.Validate(); err == nil {
		t.Fatal("a negative provided version must be refused")
	}
	depNeg := base
	depNeg.Depends = []Capability{{Key: "k", MinVersion: -1}}
	if err := depNeg.Validate(); err == nil {
		t.Fatal("a negative minimum version must be refused")
	}
}

func TestResolvePeerVersionsSatisfied(t *testing.T) {
	provider := Banner{
		Stream: StreamFrameV1, PluginID: "navop-ssh", ProviderID: "ssh-1",
		Provides: []Capability{{Key: "tunnel.proxy", Commutative: true, Version: 3}},
	}
	consumer := Banner{
		Stream: StreamFrameV1, PluginID: "navop-containers", ProviderID: "c-1",
		Depends: []Capability{{Key: "tunnel.proxy", MinVersion: 2}},
	}
	if gaps := ResolvePeerVersions([]Banner{provider, consumer}); len(gaps) != 0 {
		t.Fatalf("3 ≥ 2 must be satisfied: %+v", gaps)
	}
	// Zero minimum is satisfied by any version.
	anyNeed := Banner{
		Stream: StreamFrameV1, PluginID: "x", ProviderID: "x-1",
		Depends: []Capability{{Key: "tunnel.proxy"}},
	}
	if gaps := ResolvePeerVersions([]Banner{provider, anyNeed}); len(gaps) != 0 {
		t.Fatalf("MinVersion 0 means any: %+v", gaps)
	}
}

func TestResolvePeerVersionsReportsAGapWithACause(t *testing.T) {
	provider := Banner{
		Stream: StreamFrameV1, PluginID: "navop-ssh", ProviderID: "ssh-1",
		Provides: []Capability{{Key: "tunnel.proxy", Commutative: true, Version: 1}},
	}
	consumer := Banner{
		Stream: StreamFrameV1, PluginID: "navop-containers", ProviderID: "c-1",
		Depends: []Capability{{Key: "tunnel.proxy", MinVersion: 4}},
	}
	gaps := ResolvePeerVersions([]Banner{provider, consumer})
	if len(gaps) != 1 {
		t.Fatalf("gaps = %+v", gaps)
	}
	g := gaps[0]
	if g.Consumer != "navop-containers" || g.Key != "tunnel.proxy" || g.Need != 4 || g.Got != 1 {
		t.Fatalf("gap = %+v", g)
	}
	// The reason is the operator-facing sentence: it must name both sides and say
	// what to do, because "nothing activated" is the failure mode this prevents.
	for _, want := range []string{"navop-containers", "tunnel.proxy", "4", "1", "升级"} {
		if !strings.Contains(g.Reason, want) {
			t.Fatalf("reason %q is missing %q", g.Reason, want)
		}
	}
}

func TestResolvePeerVersionsIgnoresMissingKeys(t *testing.T) {
	// A dependency on a key nobody provides is σ ⊧ d (ctxcore's job). Reporting it
	// here too would conflate "wrong version" with "no provider at all".
	consumer := Banner{
		Stream: StreamFrameV1, PluginID: "x", ProviderID: "x-1",
		Depends: []Capability{{Key: "ghost.key", MinVersion: 2}},
	}
	if gaps := ResolvePeerVersions([]Banner{consumer}); len(gaps) != 0 {
		t.Fatalf("a missing key is not a version gap: %+v", gaps)
	}
}

func TestVersionOrDefaultsToOne(t *testing.T) {
	if got := (Capability{Key: "k"}).VersionOr(); got != 1 {
		t.Fatalf("default = %d, want 1", got)
	}
	if got := (Capability{Key: "k", Version: 7}).VersionOr(); got != 7 {
		t.Fatalf("explicit = %d", got)
	}
}
