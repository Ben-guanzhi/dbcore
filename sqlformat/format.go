// Package sqlformat provides a small, dependency-free SQL beautifier. It is
// intentionally lightweight (no parser/visitor): it tokenizes, uppercases
// keywords, and lays clauses out on their own indented lines. Correct SQL
// round-trips unchanged; the formatting is best-effort for readability, not a
// semantic pretty-printer.
//
// It is a client-side tool: the same SQL, formatted, still parses the same way,
// so it is safe to run before a query without changing what the server sees.
// The dialect does not change the layout — the clause keywords and quoting this
// formatter knows are the SQL-standard ones every engine shares, so the output
// is engine-agnostic even if the input quotes an identifier differently.
//
// It lives in the kernel so every client shares one implementation: dbstudio
// (Go), sqlgpui (Rust, ported from this file) and db-dev (JavaScript, through
// the Go backend) must not drift apart.
package sqlformat

import (
	"strings"
	"unicode"
)

type tokKind int

const (
	kWord tokKind = iota
	kString
	kNumber
	kPunct
	kComment
	kWS
)

type token struct {
	kind tokKind
	text string
}

// keywords is the set of words uppercased during formatting.
var keywords = map[string]bool{
	"SELECT": true, "FROM": true, "WHERE": true, "GROUP": true, "HAVING": true,
	"ORDER": true, "LIMIT": true, "OFFSET": true, "UNION": true, "INSERT": true,
	"VALUES": true, "UPDATE": true, "DELETE": true, "SET": true, "JOIN": true,
	"INNER": true, "LEFT": true, "RIGHT": true, "FULL": true, "CROSS": true,
	"OUTER": true, "ON": true, "AND": true, "OR": true, "AS": true, "BY": true,
	"INTO": true, "IN": true, "IS": true, "NOT": true, "NULL": true,
	"DISTINCT": true, "CASE": true, "WHEN": true, "THEN": true, "ELSE": true,
	"END": true, "ASC": true, "DESC": true, "USING": true, "BETWEEN": true,
	"LIKE": true, "EXISTS": true, "ALL": true, "ANY": true, "TRUE": true,
	"FALSE": true, "INTERVAL": true,
}

// newlineBefore lists keywords that start a new indented line.
var newlineBefore = map[string]bool{
	"SELECT": true, "FROM": true, "WHERE": true, "GROUP": true, "HAVING": true,
	"ORDER": true, "LIMIT": true, "OFFSET": true, "UNION": true, "INSERT": true,
	"VALUES": true, "UPDATE": true, "DELETE": true, "SET": true, "JOIN": true,
	"ON": true, "AND": true, "OR": true,
}

func isWordChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// isOpChar is true for characters that may form a (possibly multi-character)
// operator token: = < > ! + - * / % ^ & |. Comments (-- and /* */) and the
// dot are handled separately, before this set is consulted.
func isOpChar(r rune) bool {
	switch r {
	case '=', '<', '>', '!', '+', '-', '*', '/', '%', '^', '&', '|':
		return true
	}
	return false
}

func tokenize(s string) []token {
	var toks []token
	n := len(s)
	for i := 0; i < n; {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			j := i
			for j < n && (s[j] == ' ' || s[j] == '\t' || s[j] == '\n' || s[j] == '\r') {
				j++
			}
			toks = append(toks, token{kWS, s[i:j]})
			i = j
		case c == '-' && i+1 < n && s[i+1] == '-':
			j := i + 2
			for j < n && s[j] != '\n' {
				j++
			}
			toks = append(toks, token{kComment, s[i:j]})
			i = j
		case c == '/' && i+1 < n && s[i+1] == '*':
			j := i + 2
			for j+1 < n && (s[j] != '*' || s[j+1] != '/') {
				j++
			}
			if j+1 < n {
				j += 2
			}
			toks = append(toks, token{kComment, s[i:j]})
			i = j
		case c == '\'' || c == '"':
			quote := c
			j := i + 1
			for j < n {
				if s[j] == quote {
					if j+1 < n && s[j+1] == quote {
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			toks = append(toks, token{kString, s[i:j]})
			i = j
		case unicode.IsDigit(rune(c)) || (c == '.' && i+1 < n && unicode.IsDigit(rune(s[i+1]))):
			j := i
			for j < n && (unicode.IsDigit(rune(s[j])) || s[j] == '.') {
				j++
			}
			toks = append(toks, token{kNumber, s[i:j]})
			i = j
		case isWordChar(rune(c)):
			j := i
			for j < n && isWordChar(rune(s[j])) {
				j++
			}
			toks = append(toks, token{kWord, s[i:j]})
			i = j
		default:
			if isOpChar(rune(c)) {
				j := i
				for j < n && isOpChar(rune(s[j])) {
					j++
				}
				toks = append(toks, token{kPunct, s[i:j]})
				i = j
				continue
			}
			toks = append(toks, token{kPunct, string(c)})
			i++
		}
	}
	return toks
}

// Format beautifies sql. Whitespace is collapsed, keywords are uppercased,
// and clauses/subqueries are laid out on indented lines. The output is still
// valid SQL equivalent to the input.
//
// Style: clause keywords (SELECT/FROM/WHERE/...) start a new line at the
// current parenthesis depth; a "(" increases the indent for everything inside
// it; commas start a continuation line at the same depth; binary operators
// get a space on each side; the dot in a qualified name gets no spaces.
func Format(sql string) string {
	toks := tokenize(sql)
	var b strings.Builder
	depth := 0
	atLineStart := true
	// tight suppresses the leading space of the next token: set right after
	// a dot or operator so "a.id" and "x = 1" stay glued together.
	tight := false
	// parenNL tracks, per open parenthesis, whether the group it encloses has
	// spanned more than one line. When a ")" closes such a group it drops onto
	// its own indented line; a nested group's newline also marks every
	// enclosing group, so a trailing ")" after a multi-line subquery does too.
	var parenNL []bool
	indent := func() string { return strings.Repeat("  ", depth) }

	writeIndent := func() {
		if atLineStart {
			b.WriteString(indent())
		}
	}
	newline := func() {
		b.WriteString("\n")
		atLineStart = true
		if len(parenNL) > 0 {
			parenNL[len(parenNL)-1] = true
		}
	}

	for _, t := range toks {
		switch t.kind {
		case kWS:
			continue
		case kComment:
			if !atLineStart {
				newline()
			}
			b.WriteString(t.text)
			if !strings.HasSuffix(t.text, "\n") {
				b.WriteString("\n")
			}
			atLineStart = true
			tight = false
		case kPunct:
			switch t.text {
			case "(":
				if !atLineStart && !tight {
					b.WriteString(" ")
				}
				b.WriteString("(")
				depth++
				parenNL = append(parenNL, false)
				atLineStart = false
				tight = true
			case ")":
				hadNL := false
				if len(parenNL) > 0 {
					hadNL = parenNL[len(parenNL)-1]
					parenNL = parenNL[:len(parenNL)-1]
				}
				if hadNL && len(parenNL) > 0 {
					parenNL[len(parenNL)-1] = true
				}
				depth--
				if hadNL {
					newline()
					writeIndent()
				} else if atLineStart {
					writeIndent()
				}
				b.WriteString(")")
				atLineStart = false
				tight = false
			case ",":
				b.WriteString(",")
				newline()
				tight = false
			case ";":
				b.WriteString(";")
				newline()
				tight = false
			case ".":
				b.WriteString(".")
				atLineStart = false
				tight = true
			default:
				// operator or other symbol: a space before; the following token
				// supplies its own leading space (tight stays false) so there is
				// never a double space.
				if !atLineStart && !tight {
					b.WriteString(" ")
				}
				b.WriteString(t.text)
				atLineStart = false
				tight = false
			}
		case kWord:
			upper := strings.ToUpper(t.text)
			if keywords[upper] {
				if newlineBefore[upper] {
					if !atLineStart {
						newline()
					}
					writeIndent()
				} else if !atLineStart && !tight {
					b.WriteString(" ")
				}
				b.WriteString(upper)
			} else {
				if atLineStart {
					writeIndent()
				} else if !tight {
					b.WriteString(" ")
				}
				b.WriteString(t.text)
			}
			atLineStart = false
			tight = false
		case kString, kNumber:
			if atLineStart {
				writeIndent()
			} else if !tight {
				b.WriteString(" ")
			}
			b.WriteString(t.text)
			atLineStart = false
			tight = false
		}
	}
	return strings.TrimSpace(strings.Join(trimRightLines(b.String()), "\n"))
}

// trimRightLines trims trailing whitespace from every line of s.
func trimRightLines(s string) []string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return lines
}
