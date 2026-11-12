// Package workbench defines the P0 workbench plugin protocol, layered on top of the
// existing plugindriver framing.
//
// Design anchors (Shi/Zhang/Cui, arXiv:2608.25512, "A Programming Paradigm for
// Spatiotemporal Composability"):
//
//   - Def 48 / AD-2: the banner is upgraded from a bare engine name to a component's
//     interface (d, p) — declared coeffect dependencies `Depends`, provided keys
//     `Provides`, plus `Secrets` and a stream dialect. Satisfaction σ ⊧ d (eq. 22)
//     is decidable from the banner alone, so the host can leave a fiber Inactive
//     until every dependency is present, without starting a process.
//   - §3.1 / AD-1: every context mutation is an effect carrying its inverse. The
//     wire cannot transmit a Go closure, so an EffectFrame carries an inverse
//     WITNESS (a name + payload) and the host owns the actual inverse. The unchecked
//     witness obligation of §5.1.1 is made explicit: the plugin declares the inverse
//     it expects; the host maps it to a tracked disposer and never trusts the plugin
//     to run it.
//   - §6.1 / AD-3: frames are classified as acquisition (inside the boundary,
//     revertible) or emission (outside, idΓ). This is a TAG, not a runtime check;
//     the host withholds emissions behind Safe Mode and compensates the rest.
//   - §3.4 / Thm 47: every key is a commutative table with UNIQUE TAGS. Nothing here
//     is an ordered chain or a "lowest available descriptor" allocator. Channel ids
//     are host-allocated monotonic names, and keys are `Key` strings with a
//     `Witness` marker carried by the provider so independence reduces to
//     P1 ∩ S2 = P2 ∩ S1 = ⌀ read off the declarations.
//
// This package has NO side effects and NO dependencies beyond encoding/json: it is
// the shared vocabulary both sides of the pipe compile against, and it is unit
// tested as pure logic. The runtime that actually tracks and reverts effects lives in
// the host (TableProGo/internal/ctxcore), not here.

package workbench

import "encoding/json"

// StreamDialect names the framing dialect a workbench plugin speaks.
const (
	StreamFrameV1 = "frame-v1"
)

// Capability is one coeffect key a plugin provides or depends on. The witness is
// Def 46's proof obligation, discharged by declaring a key commutative: operations
// at a key are key-local maps over a table with unique tags, so two registrations
// commute and either can be withdrawn while the other stands (Thm 45/47).
//
// Commutative must be true for a provided key; a key declared non-commutative is
// refused at load time. That is the one check the host CAN make from the declaration;
// the deeper witness (that the plugin's operations really commute) remains the
// component author's obligation (§5.1.1).
type Capability struct {
	Key         string `json:"key"`
	Commutative bool   `json:"commutative"`
}

// Banner is the workbench plugin's declaration, sent after the plugindriver banner
// as the first application frame (MethodWorkbenchHello). It is the component
// interface (d, p, e) of Def 48 minus the iterator e, which the host drives step by
// step through effect frames.
//
// ProviderID is a stable uid, NOT a version string: the target view is keyed by
// provider uids (Def 53 / §5.1.3), so a replaced provider cannot be mistaken for
// the one it replaced even when both provide equal values. Version is advisory
// metadata for the marketplace, never an identity.
type Banner struct {
	Stream     string       `json:"stream"`
	PluginID   string       `json:"pluginId"`
	ProviderID string       `json:"providerId"`
	Version    string       `json:"version,omitempty"`
	Depends    []Capability `json:"depends,omitempty"`
	Provides   []Capability `json:"provides,omitempty"`
	// Secrets are key names the plugin needs injected. Values NEVER travel in a JSON
	// frame (AD-4): the host lends them over a separate channel with an erasing
	// inverse. This slice declares NAMES only.
	Secrets []string `json:"secrets,omitempty"`
}

// Validate checks the banner's shape BEFORE the host admits the component (O-Insert
// premises: distinct provisions disjoint, cycles detectable from declarations).
// It deliberately does not require Depends/Provides to be non-empty: a leaf plugin
// that only registers UI nodes provides keys, and a host-side-only component may
// declare neither.
func (b Banner) Validate() error {
	if b.Stream != StreamFrameV1 {
		return &Error{Code: CodeBadBanner, Message: "stream must be " + StreamFrameV1}
	}
	if b.PluginID == "" || b.ProviderID == "" {
		return &Error{Code: CodeBadBanner, Message: "pluginId and providerId are required"}
	}
	for _, c := range b.Provides {
		if c.Key == "" {
			return &Error{Code: CodeBadBanner, Message: "a provided key has an empty name"}
		}
		if !c.Commutative {
			return &Error{Code: CodeNonCommutative, Message: "provided key " + c.Key + " is not witnessed commutative (Thm 47)"}
		}
	}
	for _, c := range b.Depends {
		if c.Key == "" {
			return &Error{Code: CodeBadBanner, Message: "a declared dependency has an empty name"}
		}
	}
	// Self-dependency would make precedence ≺ cyclic (Def 72: n ≺ n when a component
	// declares a key it provides itself). Theorem 73 assumes ≺ acyclic, and §6.5 says
	// a cycle leaves the fiber permanently Inactive — better to refuse at the door
	// with a clear message than to let the operator wonder why nothing activated.
	provided := map[string]bool{}
	for _, c := range b.Provides {
		if provided[c.Key] {
			return &Error{Code: CodeBadBanner, Message: "key " + c.Key + " provided twice (single-source discipline)"}
		}
		provided[c.Key] = true
	}
	for _, c := range b.Depends {
		if provided[c.Key] {
			return &Error{Code: CodeCycle, Message: "plugin " + b.PluginID + " declares and provides " + c.Key + " (precedence cycle; Thm 73)"}
		}
	}
	return nil
}

// DependsKeys / ProvidesKeys are the declaration sets d and p used by the host's
// Theorem 47 disjointness check and by σ ⊧ d.
func (b Banner) DependsKeys() []string {
	out := make([]string, 0, len(b.Depends))
	for _, c := range b.Depends {
		out = append(out, c.Key)
	}
	return out
}

func (b Banner) ProvidesKeys() []string {
	out := make([]string, 0, len(b.Provides))
	for _, c := range b.Provides {
		out = append(out, c.Key)
	}
	return out
}

// ---------- frame vocabulary ----------

// Method names, additive over the plugindriver protocol. A plugin predating them
// answers ERR_NOT_SUPPORTED and the host keeps the plain SQL-driver path unchanged.
const (
	MethodHello        = "workbench/hello"
	MethodEffect       = "workbench/effect"
	MethodChannelOpen  = "workbench/channel/open"
	MethodChannelData  = "workbench/channel/data"
	MethodChannelSize  = "workbench/channel/resize"
	MethodChannelClose = "workbench/channel/close"
	// MethodFilesAction is the W-F1 file-action surface (list/stat/mkdir/rename/
	// upload/download), session-addressed like the channel methods. Additive: a
	// plugin predating it answers ERR_NOT_SUPPORTED and the host keeps the
	// terminal-only surface.
	MethodFilesAction = "workbench/files/action"
	MethodProxy       = "workbench/proxy"
	MethodSecret      = "workbench/secret"
)

// BoundaryClass is §6.1's acquisition / emission split.
//
//	Acquisition: reversible, inside the system boundary. The host tracks an inverse
//	  and restores the prior state on unload (Thm 68 / Cor 69).
//	Emission: crosses the boundary, idΓ — the bytes already rendered, the MQTT
//	  message already published, the command already exec'd. The host CANNOT revert
//	  it. It withholds such frames behind Safe Mode (output commit) or compensates
//	  (session recording / .tmp cleanup), and Theorem 80 does not speak of them.
type BoundaryClass string

const (
	BoundaryAcquisition BoundaryClass = "acquisition"
	BoundaryEmission    BoundaryClass = "emission"
)

// EffectFrame is the ctx.effect envelope (AD-1). Each effect:
//   - has a unique Tag (commutative-table discipline: the tag, not the position,
//     identifies it, so withdrawals commute — Thm 43/47);
//   - carries an Inverse naming the disposer the HOST will run, never a closure;
//   - is classified at the boundary (§6.1);
//   - declares the Key it operates on, so the host enforces confinement (Def 55:
//     a plugin may only touch its own table and declared keys).
//
// The plugin does not send the inverse function. It sends a WITNESS: "I expect the
// host to undo this by <Inverse>". The host either has a registered inverse for
// that name or refuses the effect before applying it. This is the protocol-boundary
// equivalent of Cordis's get-trap (§5.1.4): an undeclared access is rejected rather
// than silently allowed.
type EffectFrame struct {
	Tag      string          `json:"tag"`
	Key      string          `json:"key"`
	Op       string          `json:"op"`
	Payload  json.RawMessage `json:"payload,omitempty"`
	Inverse  string          `json:"inverse"`
	Boundary BoundaryClass   `json:"boundary"`
	// Done terminates the load iterator (Def 17's Nothing): the host stops
	// requesting stages and finalises the activation. A stage with Done=false
	// must carry a full effect; a Done frame carries no effect at all.
	Done bool `json:"done,omitempty"`
	// Seq is host-assigned on acceptance, not by the plugin: a monotone allocator
	// is not rewound (Thm 68's bound), but the sequence is an ACQUISITION ledger,
	// not an externally published address, so it stays inside the boundary.
}

// ChannelFrame is one terminal/SFTP/proxy stream frame. Data carries a Seq so the
// host can detect loss and replay a recording deterministically (W-T3's
// compensation obligation). Emission frames are the bytes themselves.
type ChannelFrame struct {
	ChannelID string        `json:"channelId"`
	Seq       uint64        `json:"seq"`
	Kind      string        `json:"kind"` // "open" | "data" | "resize" | "close" | "proxy"
	Payload   []byte        `json:"payload,omitempty"`
	Cols      uint16        `json:"cols,omitempty"`
	Rows      uint16        `json:"rows,omitempty"`
	Boundary  BoundaryClass `json:"boundary"`
}

// ProxyRequest is a plugin asking to use a coeffect key it DECLARED. The host
// resolves it against the fiber's committed view (§5.1.4) and refuses
// UNDECLARED_ACCESS when the key is not in d. It never returns a raw store lookup.
type ProxyRequest struct {
	Key  string          `json:"key"`
	Op   string          `json:"op"`
	Args json.RawMessage `json:"args,omitempty"`
}

// SecretLend is AD-4's reversible borrow. Value is deliberately NOT part of the
// frame: the host writes it over a separate stdin pipe and registers an erasing
// inverse; this struct only acknowledges which names were lent.
type SecretLend struct {
	Names []string `json:"names"`
}

// Disjoint discharges Thm 47's only remaining obligation: P1 ∩ S2 = P2 ∩ S1 = ⌀.
// With every provided key witnessed commutative (checked by Validate), a pair of
// components whose declarations pass this check are independent, hence revertible
// in ANY permutation (Thm 43) — the host reads this off the banners, never by
// inspecting plugin code. It is deliberately a function on DECLARATIONS, so a
// component's independence is a static property of what it says, not of its
// schedule.
func Disjoint(a, b Banner) bool {
	pa := map[string]bool{}
	for _, k := range a.ProvidesKeys() {
		pa[k] = true
	}
	pb := map[string]bool{}
	for _, k := range b.ProvidesKeys() {
		pb[k] = true
	}
	for _, k := range b.DependsKeys() {
		if pa[k] {
			return false
		}
	}
	for _, k := range a.DependsKeys() {
		if pb[k] {
			return false
		}
	}
	return true
}

// ---------- errors ----------

// Codes are protocol-level, not engine-level. They are distinct from dbcore's
// ERR_NOT_SUPPORTED so a workbench refusal is never mistaken for an unsupported
// SQL operation.
const (
	CodeBadBanner       = "WB_BAD_BANNER"
	CodeNonCommutative  = "WB_NONCOMMUTATIVE"
	CodeCycle           = "WB_CYCLE"
	CodeUndeclared      = "WB_UNDECLARED_ACCESS"
	CodeInactive        = "WB_INACTIVE_ACCESS"
	CodeDuplicateKey    = "WB_DUPLICATE_KEY"
	CodeRevokeAbsent    = "WB_REVOKE_ABSENT"
	CodeUnknownInverse  = "WB_UNKNOWN_INVERSE"
	CodeEmissionBlocked = "WB_EMISSION_BLOCKED"
)

// Error is a self-contained protocol error; this package must not import dbcore so
// it can be shared by hosts that do not link the kernel.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }
