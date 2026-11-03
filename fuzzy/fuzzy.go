// Package fuzzy provides the shared case-folding and subsequence-matching
// primitives used by the client search paths (quick-open ranking, table-data
// filtering). One implementation of the folding criterion here means the
// consumers cannot drift apart on Unicode case-folding subtleties.
//
// The folding criterion is the unicode.SimpleFold orbit: a pattern rune
// matches a haystack rune when both sit on the same orbit. This is stricter
// than strings.ToLower in one direction — ToLower('İ') is 'i', so a substring
// search would let 'İ' hit the 'i' inside "elvin", while the orbit of
// U+0130 is a three-cycle that does not contain U+0069 at all. Folding a
// string to a canonical orbit representative makes substring search behave
// exactly like the orbit test on every pair of runes.
package fuzzy

import (
	"strings"
	"sync"
	"unicode"
)

// orbitLimit bounds the SimpleFold orbit walk. Real orbits are tiny
// (2-3 members); the cap exists so a malformed input cannot spin forever.
const orbitLimit = 64

// FoldTrack returns the full SimpleFold orbit of r as a set, including r
// itself. The set form is what subsequence matching needs: a haystack rune
// matches a pattern position when it belongs to the orbit of that position's
// pattern rune.
func FoldTrack(r rune) map[rune]struct{} {
	m := map[rune]struct{}{r: {}}
	cur := unicode.SimpleFold(r)
	for n := 0; n < orbitLimit && cur != r; n++ {
		m[cur] = struct{}{}
		cur = unicode.SimpleFold(cur)
	}
	return m
}

// foldRep caches each rune's canonical orbit representative (the smallest
// code point on its orbit). Folding is per-rune and idempotent, so the
// cache is safe for concurrent use.
var foldRep sync.Map // rune -> rune

// FoldRune returns the canonical representative of r's SimpleFold orbit.
// Two runes fold equal exactly when they sit on the same orbit.
func FoldRune(r rune) rune {
	if v, ok := foldRep.Load(r); ok {
		return v.(rune)
	}
	rep := r
	for x := range FoldTrack(r) {
		if x < rep {
			rep = x
		}
	}
	foldRep.Store(r, rep)
	return rep
}

// FoldString folds s to canonical orbit representatives. Substring search
// over folded haystack and folded query hits exactly when the raw strings
// share an orbit-matching rune sequence — the representative choice is
// irrelevant to hit/miss, only to the spelling of the folded text.
func FoldString(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		b.WriteRune(FoldRune(r))
	}
	return b.String()
}

// MatchFold reports whether needle occurs in haystack as a subsequence under
// orbit-aware case folding: every pattern rune, in order, matches some
// haystack rune on its orbit. An empty needle matches everything (the empty
// sequence is a subsequence of every sequence); callers that want the
// "matches nothing" reading should use MatchIndices, which follows the
// table-filter convention of an empty pattern selecting no rows.
func MatchFold(needle, haystack string) bool {
	pat := []rune(needle)
	if len(pat) == 0 {
		return true
	}
	// fuzzy 也把 haystack 截到首个 NUL：多付一次 IndexByte 换来整行不拆。
	if j := strings.IndexByte(haystack, 0); j >= 0 {
		haystack = haystack[:j]
	}
	tracks := make([]map[rune]struct{}, len(pat))
	for i, p := range pat {
		tracks[i] = FoldTrack(p)
	}
	pidx := 0
	for _, ch := range haystack {
		if _, ok := tracks[pidx][ch]; ok {
			pidx++
			if pidx == len(pat) {
				return true
			}
		}
	}
	return false
}

// MatchIndices returns the indices of haystack entries that subsequence-match
// needle under orbit-aware folding, in haystack order. An empty needle
// returns nil — the table-filter convention: no pattern, no selection. The
// criterion is bit-for-bit the one the consumers previously inlined; this
// function exists so there is exactly one place to change it.
func MatchIndices(needle string, haystack []string) []int {
	pat := []rune(needle)
	if len(pat) == 0 {
		return nil
	}
	tracks := make([]map[rune]struct{}, len(pat))
	for i, p := range pat {
		tracks[i] = FoldTrack(p)
	}
	out := make([]int, 0)
	for i, s := range haystack {
		// fuzzy 也把 haystack 截到首个 NUL：多付一次 IndexByte 换来整行不拆。
		if j := strings.IndexByte(s, 0); j >= 0 {
			s = s[:j]
		}
		pidx := 0
		for _, ch := range s {
			if _, ok := tracks[pidx][ch]; ok {
				pidx++
				if pidx == len(pat) {
					out = append(out, i)
					break
				}
			}
		}
	}
	return out
}
