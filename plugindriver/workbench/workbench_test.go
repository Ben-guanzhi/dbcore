package workbench

import (
	"encoding/json"
	"testing"
)

// P0 protocol tests: pure logic. The point is that the DECLARATIONS, not the code,
// carry the guarantees (Thm 47 / Def 53 / σ ⊧ d), so the tests assert on banners and
// frames rather than on process behaviour.

func validBanner() Banner {
	return Banner{
		Stream:     StreamFrameV1,
		PluginID:   "navop-containers",
		ProviderID: "uid-containers-1",
		Depends:    []Capability{{Key: "tunnel.proxy", Commutative: true}, {Key: "connections.store", Commutative: true}},
		Provides:   []Capability{{Key: "container.session", Commutative: true}},
		Secrets:    []string{"docker.socket"},
	}
}

func TestBannerValidateAcceptsWitnessedDeclarations(t *testing.T) {
	if err := validBanner().Validate(); err != nil {
		t.Fatalf("a valid banner must pass: %v", err)
	}
}

func TestBannerRejectsNonCommutativeProvision(t *testing.T) {
	// Def 46: the PROVIDER carries the commutativity witness. A key declared
	// non-commutative cannot be admitted, because Thm 47's independence would no
	// longer reduce to a disjointness check on declarations.
	b := validBanner()
	b.Provides[0].Commutative = false
	if err := b.Validate(); err == nil {
		t.Fatal("a non-commutative provided key must be refused")
	} else if e, ok := err.(*Error); !ok || e.Code != CodeNonCommutative {
		t.Fatalf("want %s, got %v", CodeNonCommutative, err)
	}
}

func TestBannerRejectsSelfDependencyCycle(t *testing.T) {
	// Def 72: n ≺ n when a component declares a key it provides. Thm 73 assumes
	// ≺ acyclic; §6.5 says a cycle leaves the fiber permanently Inactive. Refusing
	// at the door turns a silent mystery into a clear diagnostic.
	b := validBanner()
	b.Depends = append(b.Depends, Capability{Key: "container.session", Commutative: true})
	if err := b.Validate(); err == nil {
		t.Fatal("a self-dependency cycle must be refused")
	} else if e, ok := err.(*Error); !ok || e.Code != CodeCycle {
		t.Fatalf("want %s, got %v", CodeCycle, err)
	}
}

func TestBannerRejectsDuplicateProvision(t *testing.T) {
	b := validBanner()
	b.Provides = append(b.Provides, b.Provides[0])
	if err := b.Validate(); err == nil {
		t.Fatal("providing a key twice must be refused (single-source discipline)")
	}
}

func TestBannerRejectsMissingIdentityAndBadStream(t *testing.T) {
	for _, mutate := range []func(*Banner){
		func(b *Banner) { b.Stream = "frame-v0" },
		func(b *Banner) { b.PluginID = "" },
		func(b *Banner) { b.ProviderID = "" },
		func(b *Banner) { b.Depends[0].Key = "" },
	} {
		b := validBanner()
		mutate(&b)
		if err := b.Validate(); err == nil {
			t.Fatalf("invalid banner accepted: %+v", b)
		} else if e, ok := err.(*Error); !ok || e.Code != CodeBadBanner {
			t.Fatalf("want %s, got %v", CodeBadBanner, err)
		}
	}
}

func TestTheorem47DisjointnessIsDeclarationOnly(t *testing.T) {
	// The plan's own example: containers depends on tunnel.proxy, mqtt depends on
	// tunnel.proxy; neither provides what the other declares ⇒ automatically
	// independent, reverted in any permutation (Thm 43).
	ssh := Banner{
		Stream: StreamFrameV1, PluginID: "navop-ssh", ProviderID: "uid-ssh-1",
		Provides: []Capability{{Key: "tunnel.proxy", Commutative: true}, {Key: "terminal.channels", Commutative: true}},
	}
	containers := validBanner()
	mqtt := Banner{
		Stream: StreamFrameV1, PluginID: "navop-mqtt", ProviderID: "uid-mqtt-1",
		Depends:  []Capability{{Key: "tunnel.proxy", Commutative: true}},
		Provides: []Capability{{Key: "mqtt.session", Commutative: true}},
	}
	if !Disjoint(containers, mqtt) {
		t.Fatal("containers and mqtt declare disjoint provisions — they must be independent")
	}
	if Disjoint(ssh, containers) {
		t.Fatal("ssh provides tunnel.proxy which containers declares — they are entangled (guard orders them)")
	}
}

func TestEffectFrameRoundTripCarriesTheWitness(t *testing.T) {
	// The wire carries an inverse NAME, never a closure. The host owns the disposer;
	// the plugin only witnesses which inverse it expects (§5.1.1 unchecked
	// obligation made explicit at the protocol boundary).
	in := EffectFrame{
		Tag: "nav.node/container-tab", Key: "nav.nodes", Op: "insert",
		Payload: json.RawMessage(`{"label":"Containers"}`),
		Inverse: "remove", Boundary: BoundaryAcquisition,
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out EffectFrame
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Tag != in.Tag || out.Inverse != "remove" || out.Boundary != BoundaryAcquisition {
		t.Fatalf("round trip lost fields: %+v", out)
	}
}

func TestEmissionIsTaggedNotReverted(t *testing.T) {
	// §6.1: an emission crosses the boundary and is idΓ. The frame must SAY so, so
	// the host withholds it behind Safe Mode instead of pretending to track it.
	f := ChannelFrame{ChannelID: "ch-1", Seq: 7, Kind: "data", Payload: []byte("rm -rf /"), Boundary: BoundaryEmission}
	raw, _ := json.Marshal(f)
	var out ChannelFrame
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Boundary != BoundaryEmission {
		t.Fatal("emission boundary class must survive the wire")
	}
}

func TestSecretLendCarriesNamesOnly(t *testing.T) {
	// AD-4: secret VALUES never enter a JSON frame. If a future edit adds a value
	// field here, the marshal test below is the tripwire.
	lend := SecretLend{Names: []string{"docker.socket", "kubeconfig"}}
	raw, _ := json.Marshal(lend)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["value"]; ok {
		t.Fatal("a SecretLend must never carry a secret value")
	}
	if _, ok := m["names"]; !ok {
		t.Fatal("names missing")
	}
}
