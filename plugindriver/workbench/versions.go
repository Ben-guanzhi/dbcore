package workbench

// versions.go — §6.6's peer-dependency model, as a pure function on declarations.
//
// The choice, stated plainly: a banner declares KEY NAMES plus an integer minimum
// version, and the HOST resolves it against the provider's declared version.
// SEMVER IS NOT USED — not because it is unknown, but because it is an unenforceable
// convention: "^1.2.0" cannot be checked against a running plugin, while "the
// provider offers at least 2" can. Multi-version coexistence is NOT supported: one
// key has one provider, so an old and a new provider cannot both serve it. That is
// a documented limitation (an open question), not a silent behaviour.
//
// The resolution is deliberately a function on the BANNERS the host already has:
// no plugin code is inspected, and the answer is decidable before any effect runs.

import (
	"fmt"
	"sort"
)

// VersionGap is one unmet peer requirement.
type VersionGap struct {
	Consumer string `json:"consumer"`
	Key      string `json:"key"`
	Need     int    `json:"need"`
	Got      int    `json:"got"`
	// Reason is the operator-facing sentence; the host shows it as the fiber's
	// inactive reason so "why did nothing activate" has an answer.
	Reason string `json:"reason"`
}

// ResolvePeerVersions reports every dependency whose MinVersion exceeds what the
// providing banner offers. `banners` should be the ACTIVE plugins' declarations
// (the host decides what "active" means; this function is pure).
//
// A dependency with MinVersion == 0 is satisfied by any provider. A dependency on a
// key NO banner provides is NOT a version gap here — that is σ ⊧ d, ctxcore's job,
// and reporting it twice would confuse the two causes.
func ResolvePeerVersions(banners []Banner) []VersionGap {
	// offered[key] = highest provided version (single provider per key is enforced
	// elsewhere; taking the max makes the function total even on a bad banner set).
	offered := map[string]int{}
	for _, b := range banners {
		for _, c := range b.Provides {
			if v := c.VersionOr(); v > offered[c.Key] {
				offered[c.Key] = v
			}
		}
	}
	var gaps []VersionGap
	for _, b := range banners {
		for _, d := range b.Depends {
			need := d.MinVersion
			if need <= 0 {
				continue
			}
			got, provided := offered[d.Key]
			if !provided || got >= need {
				continue
			}
			gaps = append(gaps, VersionGap{
				Consumer: b.PluginID,
				Key:      d.Key,
				Need:     need,
				Got:      got,
				Reason: fmt.Sprintf("%s 需要 %s 的版本 ≥%d，当前提供者只提供 %d：请升级提供者插件",
					b.PluginID, d.Key, need, got),
			})
		}
	}
	sort.Slice(gaps, func(i, j int) bool {
		if gaps[i].Consumer != gaps[j].Consumer {
			return gaps[i].Consumer < gaps[j].Consumer
		}
		return gaps[i].Key < gaps[j].Key
	})
	return gaps
}
