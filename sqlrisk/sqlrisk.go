// Package sqlrisk classifies a statement's blast radius: read / write /
// destructive, with the specific reasons a user approving it should read.
//
// 收编自 TableProGo 的 internal/sqlrisk（DataGrip 的"语句风险检测"特色，
// 八产品对齐 §343）：分类逻辑与中文文案原样移植，safemode 的
// StripLeadingComments/stripLiterals 一并进来（包内自足，无反向依赖）。
// 五客户端共用一份检测；TableProGo 的 internal/sqlrisk 变成薄壳。
package sqlrisk

import (
	"fmt"
	"strings"

	"github.com/Ben-guanzhi/dbcore"
)

// Level 是风险的档位。
type Level string

const (
	// LevelRead 只读。
	LevelRead Level = "read"
	// LevelWrite 改数据但有行过滤（或插入）：影响半径由语句自身圈定。
	LevelWrite Level = "write"
	// LevelDestructive 可改写/删除全部行，或删除对象。
	LevelDestructive Level = "destructive"
)

// Risk 是一次评估的结果。
type Risk struct {
	Level   Level    `json:"level"`
	Summary string   `json:"summary"`
	Reasons []string `json:"reasons,omitempty"`
	// UnboundedScan 标记一个没有行上限的读：不是写风险，
	// 但这条查询可能把整张表拉进结果。
	UnboundedScan bool `json:"unboundedScan,omitempty"`
}

// Assess 评估一条语句。
//
// 检查顺序即读者理解顺序：是不是写、是哪种写、这种写是否需要行过滤。
func Assess(sqlText string) Risk {
	stmt := strings.TrimSpace(sqlText)
	if stmt == "" {
		return Risk{Level: LevelRead, Summary: "空语句"}
	}
	if dbcore.IsReadQuery(stmt) {
		return assessRead(stmt)
	}

	kind := writeKind(stmt)
	switch kind {
	case "insert":
		return Risk{
			Level:   LevelWrite,
			Summary: "插入数据",
			Reasons: []string{"会新增行；不会改动已有行"},
		}
	case "ddl":
		name := ddlVerb(stmt)
		return Risk{
			Level:   LevelDestructive,
			Summary: "结构变更（" + strings.ToUpper(name) + "）",
			Reasons: []string{fmt.Sprintf("%s 会改动表结构，可能不可回滚", strings.ToUpper(name))},
		}
	case "update", "delete", "merge":
		upper := strings.ToUpper(kind)
		if !HasWhereClause(stmt) {
			return Risk{
				Level:   LevelDestructive,
				Summary: upper + " 没有 WHERE：整表改写",
				Reasons: []string{
					fmt.Sprintf("%s 没有 WHERE 条件，会作用到表的每一行", upper),
					"这类语句通常无法撤销；建议先 SELECT 同样的条件确认范围",
				},
			}
		}
		return Risk{
			Level:   LevelWrite,
			Summary: upper + "（带 WHERE）",
			Reasons: []string{fmt.Sprintf("%s 带 WHERE 条件，影响范围由条件决定", upper)},
		}
	}
	// 既不是读也不属于已知写类型：保守对待。
	return Risk{
		Level:   LevelWrite,
		Summary: "未分类的写语句",
		Reasons: []string{"语句不是只读的，但也不属于已知的写类型：请人工确认它做什么"},
	}
}

// assessRead 标记仍可能伤人的读：没有行上限的查询能把整张表拉进结果。
func assessRead(stmt string) Risk {
	risk := Risk{Level: LevelRead, Summary: "只读查询"}
	if !hasRowLimit(stmt) {
		risk.UnboundedScan = true
		risk.Reasons = append(risk.Reasons,
			"没有 LIMIT/TOP/FETCH：结果可能很大（执行时会按连接的行数上限截断）")
	}
	return risk
}

// hasRowLimit 判断语句是否自带结果上限。
func hasRowLimit(stmt string) bool {
	clean, ok := stripLiterals(stmt)
	if !ok {
		clean = stmt
	}
	lower := strings.ToLower(clean)
	for _, word := range []string{" limit ", " top ", " fetch first ", " fetch next ", " rownum"} {
		if strings.Contains(lower, word) {
			return true
		}
	}
	// 句首 TOP（SELECT TOP 10 …）前面没有空格。
	if strings.HasPrefix(lower, "select top ") {
		return true
	}
	return false
}

// writeKind 给非读语句分类。
func writeKind(stmt string) string {
	trimmed := strings.TrimSpace(StripLeadingComments(stmt))
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(lower, "update"):
		return "update"
	case strings.HasPrefix(lower, "delete"):
		return "delete"
	case strings.HasPrefix(lower, "merge"):
		return "merge"
	case strings.HasPrefix(lower, "insert"):
		return "insert"
	case strings.HasPrefix(lower, "drop"), strings.HasPrefix(lower, "truncate"),
		strings.HasPrefix(lower, "alter"), strings.HasPrefix(lower, "create"):
		return "ddl"
	}
	return "other"
}

// ddlVerb 报告 DDL 动词，用于摘要。
func ddlVerb(stmt string) string {
	lower := strings.ToLower(strings.TrimSpace(StripLeadingComments(stmt)))
	for _, verb := range []string{"drop", "truncate", "alter", "create"} {
		if strings.HasPrefix(lower, verb) {
			return verb
		}
	}
	return "ddl"
}

// StripLeadingComments 去掉语句开头的空白与注释。
func StripLeadingComments(query string) string {
	s := query
	for {
		s = strings.TrimLeft(s, " \t\r\n")
		switch {
		case strings.HasPrefix(s, "--"):
			if idx := strings.IndexByte(s, '\n'); idx >= 0 {
				s = s[idx+1:]
				continue
			}
			return ""
		case strings.HasPrefix(s, "/*"):
			if idx := strings.Index(s, "*/"); idx >= 0 {
				s = s[idx+2:]
				continue
			}
			return ""
		}
		return s
	}
}

// HasWhereClause 判断语句是否带 WHERE——忽略字符串字面量、引号标识符与
// 注释里的 "WHERE"（"DELETE FROM t WHERE note = 'WHERE is not here'" 不能
// 数两次，被注释掉的 WHERE 也不能让无防护的语句看起来安全）。
func HasWhereClause(query string) bool {
	clean := StripLeadingComments(query)
	cleaned, ok := stripLiterals(clean)
	if !ok {
		return false
	}
	lower := strings.ToLower(cleaned)
	for i := 0; i+5 <= len(lower); i++ {
		if lower[i:i+5] != "where" {
			continue
		}
		before := byte(' ')
		if i > 0 {
			before = lower[i-1]
		}
		after := byte(' ')
		if i+5 < len(lower) {
			after = lower[i+5]
		}
		if !isIdentByte(before) && !isIdentByte(after) {
			return true
		}
	}
	return false
}

func isIdentByte(b byte) bool {
	return b == '_' || b == '$' || b == '#' ||
		(b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// stripLiterals 把字符串字面量、引号标识符与单行注释清空，关键词扫描因此
// 不会被其内容骗过。ok=false 表示文本在未闭合的字面量/注释中结束。
func stripLiterals(s string) (string, bool) {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\'', '"', '`':
			quote := c
			b.WriteByte(' ')
			i++
			for i < len(s) {
				if s[i] == quote {
					// 双写引号是转义引号，仍在字面量内。
					if i+1 < len(s) && s[i+1] == quote {
						i += 2
						continue
					}
					break
				}
				i++
			}
			if i >= len(s) {
				return "", false
			}
			b.WriteByte(' ')
		case '-':
			if i+1 < len(s) && s[i+1] == '-' {
				for i < len(s) && s[i] != '\n' {
					i++
				}
				b.WriteByte('\n')
			} else {
				b.WriteByte(c)
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), true
}
