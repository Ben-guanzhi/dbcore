package fuzzy

import (
	"strings"
	"testing"
	"unicode"
)

// oldMatchIndices is the criterion consumers previously inlined (the sqltui
// fuzzyMatchIndices). It stays here as the differential oracle: the shared
// implementation must agree with it on every corpus entry, and any future
// change to the shared criterion that shifts a hit must fail this test
// first. Kept verbatim, including its NUL-truncation comment.
func oldMatchIndices(needle string, haystack []string) []int {
	pat := []rune(needle)
	if len(pat) == 0 {
		return nil
	}
	pi := make([]map[rune]struct{}, len(pat))
	for i, p := range pat {
		m := map[rune]struct{}{p: {}}
		cur := unicode.SimpleFold(p)
		for n := 0; n < 64 && cur != p; n++ {
			m[cur] = struct{}{}
			cur = unicode.SimpleFold(cur)
		}
		pi[i] = m
	}
	out := make([]int, 0)
	for i, s := range haystack {
		if j := strings.IndexByte(s, 0); j >= 0 {
			s = s[:j]
		}
		pidx := 0
		for _, ch := range s {
			if _, ok := pi[pidx][ch]; ok {
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

func eqIndices(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFoldTrackOrbits(t *testing.T) {
	// 'K' (U+004B) sits on the three-cycle {K, k, U+212A KELVIN SIGN}:
	// SimpleFold('k') is U+212A, not 'K'. The orbit set is what makes
	// subsequence matching agree with fuzzy's equalFold.
	got := FoldTrack('K')
	want := map[rune]struct{}{'K': {}, 'k': {}, '\u212A': {}}
	if len(got) != len(want) {
		t.Fatalf("FoldTrack('K') = %v, want the three-cycle %v", got, want)
	}
	for r := range want {
		if _, ok := got[r]; !ok {
			t.Errorf("FoldTrack('K') missing %q", r)
		}
	}
	// Singleton orbits survive (e.g. '1', CJK).
	if got := FoldTrack('1'); len(got) != 1 {
		t.Errorf("FoldTrack('1') = %v, want singleton", got)
	}
	if got := FoldTrack('中'); len(got) != 1 {
		t.Errorf("FoldTrack('中') = %v, want singleton", got)
	}
}

func TestFoldRuneIdempotentAndEqual(t *testing.T) {
	// Folding is idempotent and fold-equal iff same orbit.
	for _, r := range []rune{'a', 'A', 'k', 'K', '\u212A', 'İ', 'ı', '中', '1', 'ß'} {
		rep := FoldRune(r)
		if FoldRune(rep) != rep {
			t.Errorf("FoldRune(%q) = %q, not idempotent", r, rep)
		}
		if _, ok := FoldTrack(r)[rep]; !ok {
			t.Errorf("FoldRune(%q) = %q is not on its own orbit", r, rep)
		}
	}
	// 'İ' (U+0130) orbit: {İ, ı(U+0131), ḿ?...} — the point is it must NOT
	// contain 'i' (U+0069): ToLower('İ') is 'i', which would let a substring
	// search hit the i inside "elvin"; the orbit test must not.
	if _, ok := FoldTrack('İ')['i']; ok {
		t.Error("FoldTrack('İ') contains 'i'; the orbit test must reject ToLower's over-folding")
	}
}

func TestFoldStringSubstringImpliesSubsequence(t *testing.T) {
	// Substring containment over folded strings implies orbit subsequence
	// matching (a contiguous run is a special case of an in-order walk), but
	// not the reverse: "ord" subsequence-matches "o r d" yet the folded hay
	// "O R D" does not contain the folded needle "ORD". The reverse direction
	// holds only for contiguous hits, which is exactly what matchRank (the
	// substring model) relies on.
	corpus := []string{"orders", "order_items", "invoice.items", "lineitems", "中文items表", "elvin", "KELVIN", "\u212Aelvin", "user:1", "ORDER-42"}
	queries := []string{"ord", "items", "elvin", "k", "K", "kelvin", "item", "user", "中"}
	for _, hay := range corpus {
		foldedHay := FoldString(hay)
		for _, q := range queries {
			substringHit := strings.Contains(foldedHay, FoldString(q))
			orbitHit := MatchFold(q, hay)
			if substringHit && !orbitHit {
				t.Errorf("fold-substring(%q, %q) = true but orbit match = false", hay, q)
			}
		}
	}
	// The contiguous corpus holds the reverse too — pin the equality that
	// matchRank's substring model actually depends on.
	for _, hay := range corpus {
		for _, q := range queries {
			if strings.Contains(FoldString(hay), FoldString(q)) != MatchFold(q, hay) {
				t.Errorf("contiguous corpus: fold-substring(%q, %q) != orbit match", hay, q)
			}
		}
	}
	// The orbit test must reject ToLower's over-folding where it matters:
	// ToLower('İ') = 'i' would make "elvin" contain "İ" after folding, but
	// the orbit of U+0130 does not contain U+0069.
	if MatchFold("İ", "elvin") {
		t.Error("MatchFold(\"İ\", \"elvin\") = true; orbit test must reject ToLower's over-folding")
	}
}

func TestMatchFoldSubsequence(t *testing.T) {
	cases := []struct {
		needle, hay string
		want        bool
	}{
		{"ord", "orders", true},         // prefix
		{"ord", "for orders", true},     // subsequence across the gap
		{"ord", "o r d", true},          // subsequence with gaps
		{"ord", "rdo", false},           // order broken: an anagram is not a subsequence
		{"ord", "f rd", false},          // pattern rune missing entirely
		{"", "anything", true},          // empty needle matches everything
		{"xyz", "abc", false},           // no hit
		{"K", "kelvin", true},           // orbit: K/k/KELVIN SIGN
		{"\u212A", "kelvin", true},      // KELVIN SIGN matches k
		{"kelvin", "\u212Aelvin", true}, // orbit runes in sequence
		{"İ", "elvin", false},           // orbit must reject ToLower over-folding
		{"\x00x", "abc\x00def", false},  // a NUL in the needle matches nothing in the truncated haystack
		{"def", "abc\x00def", false},    // NUL truncation: the match is past the NUL, so it never happens
		{"abc", "abc\x00def", true},     // NUL truncation keeps the hit before it
	}
	for _, tc := range cases {
		if got := MatchFold(tc.needle, tc.hay); got != tc.want {
			t.Errorf("MatchFold(%q, %q) = %v, want %v", tc.needle, tc.hay, got, tc.want)
		}
	}
}

func TestMatchIndicesOrderAndConvention(t *testing.T) {
	hay := []string{"orders", "for orders", "rdo", "lineitems", "中文items表"}
	// Hits keep haystack order (frame order), not any ranking.
	got := MatchIndices("ord", hay)
	want := []int{0, 1}
	if !eqIndices(got, want) {
		t.Errorf("MatchIndices(\"ord\") = %v, want %v", got, want)
	}
	// Empty needle selects nothing (table-filter convention).
	if got := MatchIndices("", hay); got != nil {
		t.Errorf("MatchIndices(\"\") = %v, want nil", got)
	}
	// CJK haystack: folding must not break non-ASCII runes.
	got = MatchIndices("items", hay)
	want = []int{3, 4}
	if !eqIndices(got, want) {
		t.Errorf("MatchIndices(\"items\") = %v, want %v", got, want)
	}
}

// TestMatchIndicesDifferentialOld pins the shared criterion to the inlined
// consumer implementation: same corpus, same output, byte for byte. K-F2's
// step 3: the old code stays as oracle until the new one proves identical.
func TestMatchIndicesDifferentialOld(t *testing.T) {
	haystacks := [][]string{
		{"orders", "order_items", "invoice.items", "lineitems", "中文items表"},
		{"orders", "for orders", "rdo", "lineitems"},
		{"user:1", "user:2", "order:42", "KELVIN", "\u212Aelvin", "elvin"},
		{"", "abc", "\x00abc", "a\x00bc", "abc\x00def", "中文", "İs"},
		{"ORDER-42", "order-42", "Order_Items", "ORDER_ITEMS"},
	}
	needles := []string{"ord", "items", "order", "items", "user", "k", "K", "\u212A", "kelvin", "İ", "elvin", "", "中", "a", "\x00", "abc"}
	for _, hay := range haystacks {
		for _, needle := range needles {
			got := MatchIndices(needle, hay)
			want := oldMatchIndices(needle, hay)
			if !eqIndices(got, want) {
				t.Errorf("differential: MatchIndices(%q, %v) = %v, old = %v", needle, hay, got, want)
			}
		}
	}
}

// TestMatchFoldDifferentialOld covers the single-string predicate the same way.
func TestMatchFoldDifferentialOld(t *testing.T) {
	hays := []string{"orders", "for orders", "rdo", "lineitems", "KELVIN", "\u212Aelvin", "elvin", "中文items表", "abc\x00def"}
	needles := []string{"ord", "items", "order", "user", "k", "K", "\u212A", "kelvin", "İ", "elvin", "中", "a", "\x00abc", "abc"}
	for _, hay := range hays {
		for _, needle := range needles {
			got := MatchFold(needle, hay)
			wantOld := len(oldMatchIndices(needle, []string{hay})) > 0
			if got != wantOld {
				t.Errorf("differential: MatchFold(%q, %q) = %v, old = %v", needle, hay, got, wantOld)
			}
		}
	}
}

// TestFoldRuneThreadSafety exercises the cache under -race: the package is
// imported by concurrent search paths, and FoldRune's cache is shared.
func TestFoldRuneThreadSafety(t *testing.T) {
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(i int) {
			for r := rune(i); r < rune(i+32); r++ {
				_ = FoldRune(r)
				_ = FoldString(string(r))
			}
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}
