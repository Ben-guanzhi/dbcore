// Package keyset implements keyset ("seek") paging for table browsing.
//
// OFFSET m makes the server read and discard m rows before returning one page,
// so stepping to page k costs O(k²) rows in total; seeking from the key of the
// page already on screen reads exactly a page. A grid that only offers previous
// and next always shows a page a cursor can be taken from, so keyset covers it.
//
// The cursor travels as text, never as a JSON number: a 64-bit key does not
// survive JavaScript's double, and a key that came back rounded would skip or
// repeat a row without any error to show for it.
//
// Lifted from TableProGo's internal/paging (INTEGRATION-PLAN P1-7); the client
// keeps only a type adapter over dbcore.ColumnInfo.
package keyset

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/sqltext"
)

// Kind is how a key column's values are read back and compared.
type Kind int

const (
	// NotSeekable means the column's type cannot round-trip through a text
	// cursor; a page containing one is read by OFFSET.
	NotSeekable Kind = iota
	// Integer keys are compared as numbers and printed without decoration.
	Integer
	// Text keys are compared as strings.
	Text
)

// Key is one column of the page's key: the catalog name, the quoted identifier
// to write into SQL, and how its values must be read back.
type Key struct {
	Name   string
	Quoted string
	Kind   Kind
}

// Keys returns the key to seek by, in the order the page sorts by, or nil when
// this page cannot be seeked. A key holding a type the caller cannot round-trip
// is treated as no key at all: the page is then read by OFFSET.
//
// SQL Server and Oracle are left out on purpose: their parameter conventions
// cannot be checked from here, and their paging form is already a server-side
// cursor statement.
func Keys(dbType string, columns []dbcore.ColumnInfo, columnsErr error) []Key {
	d, ok := sqltext.DialectFor(dbType)
	if !ok || !d.KeysetEnabled {
		return nil
	}
	if columnsErr != nil {
		return nil
	}
	var keys []Key
	for _, col := range columns {
		if !col.IsPrimaryKey {
			continue
		}
		kind := SeekableKeyType(col.Type)
		if kind == NotSeekable {
			return nil
		}
		quoted, err := sqltext.QuoteIdent(dbType, col.Name)
		if err != nil {
			return nil
		}
		keys = append(keys, Key{Name: col.Name, Quoted: quoted, Kind: kind})
	}
	return keys
}

// SeekableKeyType admits whole numbers and text by their declared type. Floats
// and decimals print differently on the way back, dates and times come out of
// the driver formatted rather than stored, and binary arrives as text that is
// not the bytes — so those keys are refused instead of guessed at.
func SeekableKeyType(colType string) Kind {
	ct := sqltext.NewColumnType(colType)
	if !ct.IsSeekable() {
		return NotSeekable
	}
	if ct.IsInteger() {
		return Integer
	}
	return Text
}

// Order orders a seek by the key, descending when stepping backwards so the rows
// nearest the cursor are the ones a page holds.
func Order(keys []Key, backward bool) string {
	parts := make([]string, len(keys))
	for i, key := range keys {
		parts[i] = key.Quoted
		if backward {
			parts[i] += " DESC"
		}
	}
	return " ORDER BY " + strings.Join(parts, ", ")
}

// Where keeps the rows strictly after (or before) a cursor. A key of more than
// one column cannot be compared as a row value on every engine, so the tuple is
// expanded the portable way: greater in the first column, or equal so far and
// greater in the next. Values go in as parameters, never as pasted text.
func Where(dbType string, keys []Key, cursor []string, backward bool) (string, []interface{}, error) {
	if len(cursor) != len(keys) {
		return "", nil, fmt.Errorf("游标有 %d 个值，键有 %d 列", len(cursor), len(keys))
	}
	comparison := ">"
	if backward {
		comparison = "<"
	}

	var args []interface{}
	// A repeated key column needs one parameter per mention, because "?" cannot
	// be named twice.
	param := func(key Key, raw string) (string, error) {
		arg, err := cursorArg(key, raw)
		if err != nil {
			return "", err
		}
		args = append(args, arg)
		return cursorParam(dbType, len(args)), nil
	}

	terms := make([]string, 0, len(keys))
	for i := range keys {
		parts := make([]string, 0, i+1)
		for j := 0; j < i; j++ {
			place, err := param(keys[j], cursor[j])
			if err != nil {
				return "", nil, err
			}
			parts = append(parts, keys[j].Quoted+" = "+place)
		}
		place, err := param(keys[i], cursor[i])
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, keys[i].Quoted+" "+comparison+" "+place)
		term := strings.Join(parts, " AND ")
		if len(parts) > 1 {
			term = "(" + term + ")"
		}
		terms = append(terms, term)
	}
	return "(" + strings.Join(terms, " OR ") + ")", args, nil
}

// cursorParam spells the nth parameter (counting from one) the way dbType wants.
func cursorParam(dbType string, position int) string {
	if dbType == "postgresql" {
		return "$" + strconv.Itoa(position)
	}
	return "?"
}

// cursorArg turns one echoed key back into a value of the type its column
// declares, which is what lets the seek run down the key's index instead of
// casting every row it passes.
func cursorArg(key Key, raw string) (interface{}, error) {
	switch key.Kind {
	case Integer:
		text := strings.TrimSpace(raw)
		if number, err := strconv.ParseInt(text, 10, 64); err == nil {
			return number, nil
		}
		// An unsigned 64-bit key above the signed range is still digits.
		if number, err := strconv.ParseUint(text, 10, 64); err == nil {
			return number, nil
		}
		return nil, fmt.Errorf("游标值 %q 不是 %s 列的整数", raw, key.Name)
	case Text:
		if strings.ContainsRune(raw, 0) {
			return nil, fmt.Errorf("游标值不能包含 NUL")
		}
		return raw, nil
	}
	return nil, fmt.Errorf("%s 列不能作为游标", key.Name)
}

// PageCursors reads the key of the first and the last row of a page back into
// the cursors the next step is taken from; columns are the result's column
// names in order (matched case-insensitively). Anything that stops it — a key
// column the result does not carry, a value whose shape is not the declared
// one — leaves the page without cursors, and the following page is read by
// OFFSET.
func PageCursors(rows [][]interface{}, keys []Key, columns []string) (start, end []string) {
	if len(rows) == 0 {
		return nil, nil
	}
	nameToPos := make(map[string]int, len(columns))
	for j, name := range columns {
		nameToPos[strings.ToLower(name)] = j
	}
	positions := make([]int, len(keys))
	for i, key := range keys {
		if pos, ok := nameToPos[strings.ToLower(key.Name)]; ok {
			positions[i] = pos
		} else {
			return nil, nil
		}
	}
	pick := func(row []interface{}) ([]string, bool) {
		if len(row) == 0 {
			return nil, false
		}
		values := make([]string, len(keys))
		for i, key := range keys {
			value, ok := cursorValue(key, row[positions[i]])
			if !ok {
				return nil, false
			}
			values[i] = value
		}
		return values, true
	}
	if first, ok := pick(rows[0]); ok {
		start = first
	}
	if last, ok := pick(rows[len(rows)-1]); ok {
		end = last
	}
	if start == nil || end == nil {
		return nil, nil
	}
	return start, end
}

// cursorValue spells one stored key as the text a client echoes back.
func cursorValue(key Key, value interface{}) (string, bool) {
	switch key.Kind {
	case Integer:
		switch number := value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return fmt.Sprintf("%d", number), true
		case string:
			if _, err := strconv.ParseInt(strings.TrimSpace(number), 10, 64); err == nil {
				return strings.TrimSpace(number), true
			}
		}
	case Text:
		switch text := value.(type) {
		case string:
			return text, true
		case []byte:
			return string(text), true
		}
	}
	return "", false
}

// ReversePage puts a page read backwards back into the order the caller shows.
func ReversePage(rows [][]interface{}) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}
