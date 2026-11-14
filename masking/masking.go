// Package masking masks sensitive values before rows leave the machine for an
// AI surface (MCP tool output, LLM context). The goal is that a model can see
// the *shape* of the data — enough to write correct SQL and reason about it —
// without seeing an actual person's email, phone number, national ID or card
// number.
//
// Two layers, both pure functions:
//
//  1. Column-name recognition: a column called email / phone / id_card /
//     password / token / api_key is masked wholesale. This is the primary
//     layer — it works even when the value is not a well-formed example.
//  2. Pattern recognition: a value that *looks like* an email, a Chinese
//     mobile number, a national ID, or a payment card is masked even when it
//     sits under an innocent column name (notes, comments, address).
//
// Masking keeps the first and last characters (or a fixed prefix/suffix for
// structured identifiers) so the model can still tell values apart and see
// their format: `a***@***.com`, `138****5678`, `3***********1234`.
package masking

import (
	"regexp"
	"strings"
)

// defaultSensitiveNames are column-name fragments that mark a column as
// sensitive wholesale. Matching is a case-insensitive substring test on the
// column name; fragments are ordered from most to least specific so callers
// can report which rule fired.
var defaultSensitiveNames = []string{
	"password", "passwd", "pwd",
	"email", "e-mail", "mail",
	"phone", "mobile", "tel",
	"id_card", "idcard", "identity",
	"bank", "card_no", "cardno", "iban", "iban2",
	"ssn", "passport",
	"token", "secret", "api_key", "apikey", "access_key",
	"salary", "wage",
	"weixin", "wechat", "alipay",
}

// defaultSensitive is the ready-to-use name-based policy.
var defaultSensitive = NewSensitiveNames(defaultSensitiveNames...)

// SensitiveNames matches column names against a list of sensitive fragments.
type SensitiveNames struct {
	fragments []string
}

// NewSensitiveNames builds a name matcher. Empty fragments are dropped.
func NewSensitiveNames(names ...string) *SensitiveNames {
	f := make([]string, 0, len(names))
	for _, n := range names {
		if s := strings.ToLower(strings.TrimSpace(n)); s != "" {
			f = append(f, s)
		}
	}
	return &SensitiveNames{fragments: f}
}

// IsSensitive reports whether a column name carries a sensitive fragment.
// Separators are folded away in a second pass, so "e-mail" / "e_mail" still
// hit a "email" fragment.
func (s *SensitiveNames) IsSensitive(column string) bool {
	if s == nil || column == "" {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(column))
	name = strings.ReplaceAll(name, "-", "_")
	name = strings.ReplaceAll(name, " ", "_")
	stripped := strings.ReplaceAll(name, "_", "")
	for _, frag := range s.fragments {
		if strings.Contains(name, frag) {
			return true
		}
		if f := strings.ReplaceAll(frag, "_", ""); f != "" && strings.Contains(stripped, f) {
			return true
		}
	}
	return false
}

// Patterns are the shape recognisers. Go's RE2 has no lookbehind, so the
// digit-run patterns match the digits alone and the replacement callback
// checks the neighbours itself (see digitRunBoundary).
type Patterns struct {
	email    *regexp.Regexp
	mobileCN *regexp.Regexp
	idCardCN *regexp.Regexp
	card     *regexp.Regexp
}

// defaultPatterns is the ready-to-use shape policy.
var defaultPatterns = NewDefaultPatterns()

// NewDefaultPatterns builds the built-in shape set: email, Chinese mobile,
// Chinese national ID, and generic 13-19 digit card runs.
func NewDefaultPatterns() *Patterns {
	return &Patterns{
		email:    regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`),
		mobileCN: regexp.MustCompile(`1[3-9]\d{9}`),
		idCardCN: regexp.MustCompile(`\d{17}[\dXx]`),
		card:     regexp.MustCompile(`\d{13,19}`),
	}
}

// digitRunBoundary reports whether the digit run of length n starting at i in
// s stands alone: the neighbour on each side is not a digit. This is what the
// removed lookbehind/lookahead did.
func digitRunBoundary(s string, i, n int) bool {
	if i > 0 && isDigit(s[i-1]) {
		return false
	}
	if i+n < len(s) && isDigit(s[i+n]) {
		return false
	}
	return true
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// maskEmail keeps the local part's first character and the domain's TLD:
// alice@example.com → a***@e******.com.
func maskEmail(s string) string {
	at := strings.IndexByte(s, '@')
	if at <= 0 {
		return keepEnds(s, 1, 0)
	}
	local, domain := s[:at], s[at+1:]
	dot := strings.LastIndexByte(domain, '.')
	var tld string
	if dot >= 0 && dot < len(domain)-1 {
		tld = domain[dot+1:]
	} else {
		tld = ""
	}
	var b strings.Builder
	b.WriteByte(local[0])
	b.WriteString("***@")
	if len(domain) > 0 {
		b.WriteByte(domain[0])
		b.WriteString(strings.Repeat("*", len(domain)-1))
	}
	if tld != "" {
		b.WriteByte('.')
		b.WriteString(tld)
	}
	return b.String()
}

// keepEnds masks the middle of s, keeping the first keepStart and last keepEnd
// runes. Values too short to keep anything meaningful become all stars.
func keepEnds(s string, keepStart, keepEnd int) string {
	r := []rune(s)
	if keepStart+keepEnd >= len(r) {
		return strings.Repeat("*", len(r))
	}
	var b strings.Builder
	b.WriteString(string(r[:keepStart]))
	b.WriteString(strings.Repeat("*", len(r)-keepStart-keepEnd))
	b.WriteString(string(r[len(r)-keepEnd:]))
	return b.String()
}

// maskMobileCN keeps the 3-4-4 shape of a Chinese mobile number:
// 13812345678 → 138****5678.
func maskMobileCN(s string) string {
	if len(s) != 11 {
		return keepEnds(s, 3, 2)
	}
	return s[:3] + "****" + s[7:]
}

// maskIDCardCN keeps province (2) + birth year (4) and the last 2:
// 110101199003077777 → 11**********77. The birthday month/day is what makes a
// national ID identifying, so it goes.
func maskIDCardCN(s string) string {
	if len(s) != 18 {
		return keepEnds(s, 2, 2)
	}
	return s[:2] + strings.Repeat("*", 14) + s[16:]
}

// maskCard keeps the BIN (first 4) and the last 4 — the shape a support agent
// needs, the middle digits nobody needs: 4111111111111111 → 4111********1111.
func maskCard(s string) string {
	if len(s) < 13 {
		return keepEnds(s, 2, 2)
	}
	return s[:4] + strings.Repeat("*", len(s)-8) + s[len(s)-4:]
}

// Value masks one value according to the shape patterns. Every recognised
// fragment inside the value is replaced; anything unrecognised passes through
// unchanged.
func (p *Patterns) Value(s string) string {
	if s == "" {
		return s
	}
	out := p.email.ReplaceAllStringFunc(s, maskEmail)
	out = replaceDigitRuns(out, p.idCardCN, 18, func(m string) string { return maskIDCardCN(m) })
	// Card runs before mobile: an 18-digit run could be either, and the card
	// shape keeps less. (A run that was really a national ID was already
	// masked by the pass above and is no longer all-digits.)
	out = replaceDigitRuns(out, p.card, 0, func(m string) string { return maskCard(m) })
	out = replaceDigitRuns(out, p.mobileCN, 11, maskMobileCN)
	return out
}

// replaceDigitRuns masks standalone digit runs that match re. wantLen 0 means
// any length the pattern matches; a nonzero wantLen additionally requires the
// run to be exactly that long, so the shorter mobile pattern cannot re-mask a
// fragment of an already-masked or longer run.
func replaceDigitRuns(s string, re *regexp.Regexp, wantLen int, mask func(string) string) string {
	return re.ReplaceAllStringFunc(s, func(m string) string {
		// Locate this match inside s to check its neighbours. ReplaceAllStringFunc
		// does not hand over offsets, so re-scan: runs are unique enough for the
		// purpose (the neighbour check is a safety net, not the identity).
		idx := strings.Index(s, m)
		if idx < 0 || !digitRunBoundary(s, idx, len(m)) {
			return m
		}
		if wantLen != 0 && len(m) != wantLen {
			return m
		}
		return mask(m)
	})
}

// Row masks one row of a result set. columns and row are aligned; a shorter
// row is tolerated (trailing columns absent). A nil cell passes through as
// nil; only string-typed cells are masked.
func (p *Patterns) Row(columns []string, row []interface{}, names *SensitiveNames) []interface{} {
	if len(row) == 0 {
		return row
	}
	out := make([]interface{}, len(row))
	copy(out, row)
	for i, cell := range out {
		if cell == nil {
			continue
		}
		s, ok := cell.(string)
		if !ok {
			continue
		}
		if i < len(columns) && names.IsSensitive(columns[i]) {
			out[i] = MaskWhole(s)
			continue
		}
		out[i] = p.Value(s)
	}
	return out
}

// MaskWhole replaces every non-newline character with '*', keeping only the
// value's length: a column named password carries secrets whose shape reveals
// nothing, so nothing survives.
func MaskWhole(s string) string {
	r := []rune(s)
	for i := range r {
		if r[i] != '\n' && r[i] != '\r' && r[i] != '\t' {
			r[i] = '*'
		}
	}
	return string(r)
}

// RowString is Row for string rows (db-dev's rows are [][]string).
func (p *Patterns) RowString(columns, row []string, names *SensitiveNames) []string {
	if len(row) == 0 {
		return row
	}
	out := make([]string, len(row))
	copy(out, row)
	for i, cell := range out {
		if cell == "" {
			continue
		}
		if i < len(columns) && names.IsSensitive(columns[i]) {
			out[i] = MaskWhole(cell)
			continue
		}
		out[i] = p.Value(cell)
	}
	return out
}

// Rows masks a whole result set ([][]string rows under columns).
func (p *Patterns) Rows(columns []string, rows [][]string, names *SensitiveNames) [][]string {
	if len(rows) == 0 {
		return rows
	}
	out := make([][]string, len(rows))
	for i, row := range rows {
		out[i] = p.RowString(columns, row, names)
	}
	return out
}

// Default returns the built-in combined policy: default sensitive names and
// default shape patterns.
func Default() *Policy {
	return &Policy{Names: defaultSensitive, Patterns: defaultPatterns}
}

// Policy is the combined masking policy.
type Policy struct {
	Names    *SensitiveNames
	Patterns *Patterns
}

// RowsString masks a string result set with the policy.
func (pol *Policy) RowsString(columns []string, rows [][]string) [][]string {
	return pol.Patterns.Rows(columns, rows, pol.Names)
}

// RowInterface masks an interface-row result set with the policy.
func (pol *Policy) RowInterface(columns []string, row []interface{}) []interface{} {
	return pol.Patterns.Row(columns, row, pol.Names)
}
