package dm

// DM8's own catalog details: the parts where "Oracle-compatible" stops being true.
//
// A live DM8 (20241022) settled these the hard way. Reusing Oracle's recipes was
// the right first guess and it is wrong in two specific places:
//
//   - ALL_TAB_IDENTITY_COLS does not exist, so Oracle's columns query fails with
//     "无效的表或视图名[ALL_TAB_IDENTITY_COLS]" (-2106). DM's dictionary has no
//     identity-column view at all in this build, so the identity flag is reported
//     as a constant 0 rather than guessed from a system table's bitmask.
//   - DM returns INT (and only some integer types) as TEXT over the wire, so a
//     numeric column arrives as the string "42". Left alone, the grid sorts and
//     filters integers as text and every count reads 0.
//
// Everything else — ALL_USERS, ALL_TABLES, ALL_INDEXES, ALL_CONSTRAINTS,
// ALL_DEPENDENCIES — answered Oracle's recipe unchanged.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/sqltext"
)

// columnsRecipe is Oracle's columns query minus the identity join.
//
// The column ORDER is kept identical to the Oracle recipe so dm.go's row mapping
// stays one code path: name, type, nullable, default, pk flag, comment, column id,
// identity flag. The identity slot is the literal 0.
func columnsRecipe(schema, table string) string {
	s := sqltext.QuotedLiteral("oracle", strings.ToUpper(schema))
	t := sqltext.QuotedLiteral("oracle", strings.ToUpper(table))
	return `SELECT c.COLUMN_NAME, c.DATA_TYPE ||
		CASE
			WHEN c.DATA_TYPE IN ('VARCHAR2','NVARCHAR2','CHAR','NCHAR','RAW','VARCHAR') THEN
				'(' || CASE WHEN c.CHAR_USED = 'C' THEN c.CHAR_LENGTH ELSE c.DATA_LENGTH END ||
					CASE WHEN c.CHAR_USED = 'C' AND c.DATA_TYPE IN ('VARCHAR2','CHAR','VARCHAR')
						THEN ' CHAR' ELSE '' END ||
				')'
			WHEN c.DATA_TYPE = 'NUMBER' AND c.DATA_PRECISION IS NOT NULL THEN
				'(' || c.DATA_PRECISION ||
					CASE WHEN c.DATA_SCALE IS NOT NULL THEN ',' || c.DATA_SCALE ELSE '' END ||
				')'
			WHEN c.DATA_TYPE = 'FLOAT' AND c.DATA_PRECISION IS NOT NULL THEN
				'(' || c.DATA_PRECISION || ')'
			ELSE ''
		END, c.NULLABLE, c.DATA_DEFAULT,
			CASE WHEN pk.COLUMN_NAME IS NULL THEN 0 ELSE 1 END,
			NVL(cm.COMMENTS, ''),
			c.COLUMN_ID,
			0
		FROM ALL_TAB_COLUMNS c
		LEFT JOIN (
			SELECT cc.OWNER, cc.TABLE_NAME, cc.COLUMN_NAME
			FROM ALL_CONSTRAINTS k
			INNER JOIN ALL_CONS_COLUMNS cc
				ON cc.OWNER = k.OWNER AND cc.CONSTRAINT_NAME = k.CONSTRAINT_NAME
			WHERE k.CONSTRAINT_TYPE = 'P'
		) pk
			ON pk.OWNER = c.OWNER AND pk.TABLE_NAME = c.TABLE_NAME
			AND pk.COLUMN_NAME = c.COLUMN_NAME
		LEFT JOIN ALL_COL_COMMENTS cm
			ON cm.OWNER = c.OWNER AND cm.TABLE_NAME = c.TABLE_NAME
			AND cm.COLUMN_NAME = c.COLUMN_NAME
		WHERE c.OWNER = ` + s + ` AND c.TABLE_NAME = ` + t + `
		ORDER BY c.COLUMN_ID`
}

// numericTypes is the set of DM type names whose text representation must be
// turned back into a number. Only the integer family is affected in practice; the
// float family is handled too because a driver may change its mind per build, and
// a text "1.5" that the grid sorts as text is the same bug.
var numericTypes = map[string]bool{
	"INT": true, "INTEGER": true, "BIGINT": true, "SMALLINT": true, "TINYINT": true,
	"BYTE": true, "FLOAT": true, "DOUBLE": true, "REAL": true,
}

// coerceNumericText converts text values in numeric columns back to numbers.
//
// Why here and not in the kernel: this is the DM driver's quirk, and the kernel's
// Value mapping is deliberately engine-agnostic — teaching it "some drivers return
// integers as strings" would invite every other driver to rely on it.
func coerceNumericText(res *dbcore.QueryResult) {
	if res == nil || len(res.Rows) == 0 {
		return
	}
	for ci, col := range res.Columns {
		base := strings.ToUpper(strings.TrimSpace(col.Type))
		// Strip a size/precision suffix: "INT(11)", "NUMBER(8,2)".
		if i := strings.IndexAny(base, "( "); i > 0 {
			base = base[:i]
		}
		if !numericTypes[base] {
			continue
		}
		for ri := range res.Rows {
			if ci >= len(res.Rows[ri]) {
				continue
			}
			v := res.Rows[ri][ci]
			if v.Kind != dbcore.ValText || strings.TrimSpace(v.S) == "" {
				continue
			}
			text := strings.TrimSpace(v.S)
			if n, err := strconv.ParseInt(text, 10, 64); err == nil {
				res.Rows[ri][ci] = dbcore.ValueOf(n)
				continue
			}
			if f, err := strconv.ParseFloat(text, 64); err == nil {
				res.Rows[ri][ci] = dbcore.ValueOf(f)
			}
		}
	}
}

// intTypeNames is the integer family, used where an integer is required and a
// float would be wrong (row counts, ids).
func isIntegerType(t string) bool {
	base := strings.ToUpper(strings.TrimSpace(t))
	if i := strings.IndexAny(base, "( "); i > 0 {
		base = base[:i]
	}
	switch base {
	case "INT", "INTEGER", "BIGINT", "SMALLINT", "TINYINT", "BYTE":
		return true
	}
	return false
}

// describeColumns turns an unsupported-but-expected error into something a user can
// act on: DM prints a bare "-2111 第1 行附近出现错误" for a syntax problem, which is
// useless without the statement.
func describeQueryError(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("dm: %s: %w", what, err)
}

// intOf reads an integer out of a catalog cell whatever shape DM returned it in.
//
// The catalog views mix NUMBER, DECIMAL and INT, and DM's driver hands those back
// as ValDecimal or ValText depending on the column — so reading .I blindly silently
// answers 0. A live test caught exactly that: every primary-key flag and every
// ordinal position came back zero while the query was correct.
func intOf(v dbcore.Value) int64 {
	switch v.Kind {
	case dbcore.ValInt:
		return v.I
	case dbcore.ValFloat:
		return int64(v.F)
	case dbcore.ValDecimal:
		if n, ok := dbcore.DecimalToInt64(v); ok {
			return n
		}
		if f, ok := dbcore.DecimalToFloat64(v); ok {
			return int64(f)
		}
	case dbcore.ValText:
		text := strings.TrimSpace(v.S)
		if n, err := strconv.ParseInt(text, 10, 64); err == nil {
			return n
		}
		if f, err := strconv.ParseFloat(text, 64); err == nil {
			return int64(f)
		}
	}
	return 0
}