package sqlite

// Parsing of the CREATE INDEX text that sqlite_master keeps.
//
// SQLite has no expression-index catalog: pragma_index_xinfo reports the key
// parts' order and DESC flags but not an expression's text, and a partial
// index's WHERE clause exists only in the statement. Reading both back from
// sqlite_master is the only way to describe such an index faithfully, so the
// parser below is deliberately small — quote-, bracket- and comment-aware, but
// not a SQL parser: it returns the raw text between the key list's parentheses
// and the predicate after the closing one.

import "strings"

// splitCreateIndex extracts the key-part texts and the partial-index predicate
// from a CREATE INDEX statement. ok is false when the text is not shaped like
// CREATE [UNIQUE] INDEX name ON table (parts) [WHERE predicate].
func splitCreateIndex(sql string) (parts []string, predicate string, ok bool) {
	open := firstTopLevel(sql, '(')
	if open < 0 {
		return nil, "", false
	}
	closing := matchingParen(sql, open)
	if closing < 0 {
		return nil, "", false
	}
	for _, part := range splitTopLevel(sql[open+1:closing], ',') {
		if p := strings.TrimSpace(part); p != "" {
			parts = append(parts, p)
		}
	}
	rest := strings.TrimSpace(sql[closing+1:])
	if len(rest) >= 5 && strings.EqualFold(rest[:5], "WHERE") {
		predicate = strings.TrimSpace(rest[5:])
	}
	return parts, predicate, true
}

// stripIndexDirection drops a trailing ASC/DESC direction word so a parsed part
// can be shown as an expression; the direction itself comes from the pragma.
func stripIndexDirection(part string) string {
	fields := strings.Fields(part)
	if len(fields) > 1 {
		last := fields[len(fields)-1]
		if strings.EqualFold(last, "ASC") || strings.EqualFold(last, "DESC") {
			return strings.TrimSpace(strings.TrimSuffix(part, last))
		}
	}
	return strings.TrimSpace(part)
}

// firstTopLevel returns the index of the first sep outside quotes, brackets and
// comments, or -1.
func firstTopLevel(s string, sep byte) int {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'', '"', '`', '[':
			i = skipQuoted(s, i)
		case '-':
			if i+1 < len(s) && s[i+1] == '-' {
				i = skipLineComment(s, i)
			}
		case '/':
			if i+1 < len(s) && s[i+1] == '*' {
				i = skipBlockComment(s, i)
			}
		default:
			if s[i] == sep {
				return i
			}
		}
	}
	return -1
}

// matchingParen returns the index of the ')' closing the '(' at open.
func matchingParen(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '\'', '"', '`', '[':
			i = skipQuoted(s, i)
		case '-':
			if i+1 < len(s) && s[i+1] == '-' {
				i = skipLineComment(s, i)
			}
		case '/':
			if i+1 < len(s) && s[i+1] == '*' {
				i = skipBlockComment(s, i)
			}
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// splitTopLevel splits s on sep outside quotes, brackets and parentheses.
func splitTopLevel(s string, sep byte) []string {
	var out []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'', '"', '`', '[':
			i = skipQuoted(s, i)
		case '-':
			if i+1 < len(s) && s[i+1] == '-' {
				i = skipLineComment(s, i)
			}
		case '/':
			if i+1 < len(s) && s[i+1] == '*' {
				i = skipBlockComment(s, i)
			}
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if s[i] == sep && depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, s[start:])
	return out
}

// skipQuoted returns the index of the closing quote for the one at open,
// handling doubled-quote escapes. An unterminated string runs to the end.
func skipQuoted(s string, open int) int {
	quote := s[open]
	end := quote
	if quote == '[' {
		end = ']'
	}
	for i := open + 1; i < len(s); i++ {
		if s[i] != end {
			continue
		}
		if i+1 < len(s) && s[i+1] == end {
			i++ // doubled delimiter escapes itself
			continue
		}
		return i
	}
	return len(s) - 1
}

func skipLineComment(s string, start int) int {
	if nl := strings.IndexByte(s[start:], '\n'); nl >= 0 {
		return start + nl
	}
	return len(s) - 1
}

func skipBlockComment(s string, start int) int {
	if end := strings.Index(s[start:], "*/"); end >= 0 {
		return start + end + 1
	}
	return len(s) - 1
}
