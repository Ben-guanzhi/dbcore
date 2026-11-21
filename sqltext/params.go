package sqltext

// 参数占位符的提取与重写（§382 收编自 dbstudio/params.go）：跨方言识别
// `?` / `$n` / `:name` / `@pn` 四式占位符（词法安全——跳过字符串、注释、
// PG 的 ::type 强转），按绑定序列出，并可重写为目标引擎的原生标记。
//
// 分工：内核只做**纯函数**两件——ExtractParams（提取）与
// RewritePlaceholders（重写）；值收集表单与执行归客户端（dbstudio 的
// paramPanel / fynequery 的表单 / polydb 的前端都是消费形态）。
//
// polydb 的消费是双实现纪律：Rust 侧 polydb-params 按同一语义实现 +
// 共享向量语料对拍（schema-diff 先例）。

import (
	"strconv"
	"strings"
)

// ParamRef 是一个占位符出现处：原文中的字节区间与它绑定的值序（具名
// 占位符映射到首次出现的序位）。
type ParamRef struct {
	Start, End int    // [Start, End) 在原文中的字节区间
	Index      int    // 0 基的绑定序位
	Name       string // 具名占位符的名字（无名式为空）
}

// Label 返回面板展示标签：具名为 ":name"，位置式为 1 基序号。
func (r ParamRef) Label() string {
	if r.Name != "" {
		return r.Name
	}
	return strconv.Itoa(r.Index + 1)
}

// ExtractParams 按绑定序列出 sqlText 的占位符：`?`（位置式）、`$n`
// （1 基自索引式）、`:name`（具名式，首次出现定序位）、`@pn`（SQL Server
// 具名式）。字符串与注释整体跳过，PG 的 `::type` 强转不算参数。
//
// SQL Server 的 @pn 也是具名式（@ 后跟标识符）；MySQL 的用户变量 @@var
// 不算（双前缀跳过）。
func ExtractParams(sqlText string) []ParamRef {
	var refs []ParamRef
	byName := map[string]int{}
	n := len(sqlText)
	for i := 0; i < n; {
		c := sqlText[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			// 引号串整体跳过（含成双引号转义）；反引号标识符同样跳过——
			// `weird?name` 里的问号不是参数。
			quote := c
			j := i + 1
			for j < n {
				if sqlText[j] == quote {
					if j+1 < n && sqlText[j+1] == quote {
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			i = j
		case c == '-' && i+1 < n && sqlText[i+1] == '-':
			j := strings.IndexByte(sqlText[i:], '\n')
			if j < 0 {
				i = n
			} else {
				i += j + 1
			}
		case c == '/' && i+1 < n && sqlText[i+1] == '*':
			j := strings.Index(sqlText[i+2:], "*/")
			if j < 0 {
				i = n
			} else {
				i += 2 + j + 2
			}
		case c == '?' && (i == 0 || sqlText[i-1] != ':' && sqlText[i-1] != '$'):
			refs = append(refs, ParamRef{Start: i, End: i + 1, Index: len(refs)})
			i++
		case c == '$' && i+1 < n && isDigit(sqlText[i+1]):
			j := i + 1
			for j < n && isDigit(sqlText[j]) {
				j++
			}
			num, _ := strconv.Atoi(sqlText[i+1 : j])
			// $n 是 1 基自索引：$2 永远绑第二个值（乱序也按号取值）。
			idx := num - 1
			if idx < 0 {
				idx = len(refs)
			}
			refs = append(refs, ParamRef{Start: i, End: j, Index: idx})
			i = j
		case (c == ':' || c == '@') && i+1 < n && (isIdentStart(sqlText[i+1]) || sqlText[i+1] == ':' || sqlText[i+1] == '@'):
			// ::type 是 PG 强转不是参数：跳过双冒号。@@var 是 MySQL/TSQL
			// 系统变量：跳过双前缀（两个字符一起消费，避免第二个 @ 再命中）。
			if sqlText[i+1] == ':' {
				i += 2
				continue
			}
			if c == '@' && sqlText[i+1] == '@' {
				i += 2
				for i < n && isIdentChar(sqlText[i]) {
					i++
				}
				continue
			}
			j := i + 1
			for j < n && isIdentChar(sqlText[j]) {
				j++
			}
			name := sqlText[i+1 : j]
			idx, seen := byName[name]
			if !seen {
				idx = len(refs)
				byName[name] = idx
			}
			refs = append(refs, ParamRef{Start: i, End: j, Index: idx, Name: name})
			i = j
		default:
			i++
		}
	}
	return refs
}

// RewritePlaceholders 把每个占位符重写为目标引擎的原生标记（按绑定序）。
// 语句本就是目标方言书写时，除占位符外原样返回。
func RewritePlaceholders(sqlText string, refs []ParamRef, dbType string) string {
	if len(refs) == 0 {
		return sqlText
	}
	var b strings.Builder
	prev := 0
	for _, r := range refs {
		b.WriteString(sqlText[prev:r.Start])
		b.WriteString(Placeholder(dbType, r.Index+1))
		prev = r.End
	}
	b.WriteString(sqlText[prev:])
	return b.String()
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func isIdentStart(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b == '_'
}

func isIdentChar(b byte) bool { return isIdentStart(b) || isDigit(b) }
