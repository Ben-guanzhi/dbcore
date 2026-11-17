package dbcore

// 数据生成器（Navicat 的 Data Generator、DBeaver 的 mock data）：
//
// 给表的列清单生成 N 行测试数据（确定性——同一列清单与种子产出同一批值，
// 测试可复现），并拼成 INSERT 语句。**生成是纯逻辑**（类型→值），执行归客户端
// ——与 UserAdmin 同一分层：内核出「方言正确、转义统一」的 SQL 文本。
//
// 类型覆盖：整数 / 小数 / 文本（按类型名猜测语义——email/name/phone 的常见
// 列名走真实感数据）/ 日期时间 / 布尔 / 二进制 / UUID。未知类型退回字符串。
//
// 明确不含：引用完整性（外键值从父表抽样——需要真实数据源，客户端自己组合）、
// 大批量流式生成（一次 N 行字符串足够，千行级是 GUI 生成器的合理上限）。

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// GenOptions 控制一次生成。
type GenOptions struct {
	Rows    int    // 生成的行数（>0；GUI 上限由客户端把）
	Seed    uint64 // 确定性种子（0 = 用随机种子）
	Dialect string // "mysql" | "postgres" | "sqlserver" | ""（默认 ANSI）
	Table   string // INSERT 的目标表名
	// SkipPrimaryKey 为 true 时生成语句不含主键列（自增/序列由服务器填）。
	SkipPrimaryKey bool
}

// GenerateRows 给列清单生成 rows 行数据（纯函数，确定性）。
func GenerateRows(cols []ColumnInfo, opt GenOptions) [][]string {
	if opt.Rows <= 0 {
		opt.Rows = 1
	}
	rng := newGenRand(opt.Seed)
	out := make([][]string, 0, opt.Rows)
	for i := 0; i < opt.Rows; i++ {
		row := make([]string, 0, len(cols))
		for _, c := range cols {
			if opt.SkipPrimaryKey && c.IsPrimaryKey {
				continue
			}
			row = append(row, genValue(c, i, rng))
		}
		out = append(out, row)
	}
	return out
}

// GenerateInserts 把生成的行拼成多条 INSERT（每行一条——批量执行时逐条过
// 客户端的闸门与错误定位更清楚）。
func GenerateInserts(cols []ColumnInfo, opt GenOptions) []string {
	rows := GenerateRows(cols, opt)
	// 实际参与 INSERT 的列（SkipPrimaryKey 会跳过）。
	use := make([]ColumnInfo, 0, len(cols))
	for _, c := range cols {
		if opt.SkipPrimaryKey && c.IsPrimaryKey {
			continue
		}
		use = append(use, c)
	}
	if len(use) == 0 || len(rows) == 0 {
		return nil
	}
	q := dialectQuote(opt.Dialect)
	names := make([]string, len(use))
	for i, c := range use {
		names[i] = q.ident(c.Name)
	}
	stmts := make([]string, 0, len(rows))
	table := opt.Table
	if table == "" {
		table = "generated_data"
	}
	for _, row := range rows {
		stmts = append(stmts, fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s);",
			q.ident(table), strings.Join(names, ", "), strings.Join(row, ", ")))
	}
	return stmts
}

// genValue 按列类型生成一个值字面量（含引号——文本/日期带引号，数字裸值）。
func genValue(c ColumnInfo, rowIdx int, rng *genRand) string {
	t := strings.ToLower(c.Type)
	switch {
	// bool 先于 int：tinyint(1) 同时含 "tinyint"（int 族）与布尔语义，
	// MySQL 的惯例是它是布尔——顺序决定正确性。
	case isBoolType(t):
		if rng.int64(0, 2) == 0 {
			return "FALSE"
		}
		return "TRUE"
	case isIntType(t):
		return strconv.FormatInt(rng.int64(1, 1_000_000), 10)
	case isFloatType(t):
		return strconv.FormatFloat(rng.float64(0, 10000), 'f', 2, 64)
	case isDateType(t):
		// 近 3 年内的随机时刻（UTC，免时区歧义）。
		sec := rng.int64(0, 3*365*24*3600)
		return quoteLit(time.Unix(int64(1735689600)+sec, 0).UTC().
			Format("2006-01-02 15:04:05"))
	case isUUIDType(t):
		return quoteLit(rng.uuid())
	case isBlobType(t):
		// 短十六进制串（不生成真二进制——文本协议下更安全可读）。
		return quoteLit(strings.ToUpper(rng.hex(8)))
	default:
		// 文本：按列名猜语义，猜不出走通用词。
		return quoteLit(genText(c.Name, rowIdx, rng))
	}
}

// quoteLit 文本类值包单引号（日期/UUID/文本共通；数字/布尔裸值不走这里）。
func quoteLit(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// genText 按列名猜测语义生成真实感文本（email/name/phone 的常见命名走
// 对应形状；其余走通用词+序号）。
func genText(colName string, rowIdx int, rng *genRand) string {
	n := strings.ToLower(colName)
	switch {
	case strings.Contains(n, "email"):
		return fmt.Sprintf("user%d@example%d.test", rowIdx+1, rng.int64(1, 9))
	case strings.Contains(n, "name"):
		return "name_" + strconv.FormatInt(rng.int64(1, 999), 10)
	case strings.Contains(n, "phone") || strings.Contains(n, "mobile"):
		return fmt.Sprintf("+8613%09d", rng.int64(0, 999999999))
	case strings.Contains(n, "address") || strings.Contains(n, "addr"):
		return fmt.Sprintf("%d %s Street", rng.int64(1, 999), rng.word())
	case strings.Contains(n, "city"):
		return rng.word()
	case strings.Contains(n, "title") || strings.Contains(n, "subject"):
		return rng.word() + " " + rng.word()
	default:
		return fmt.Sprintf("value_%s_%d", rng.word(), rowIdx+1)
	}
}

// isIntType 判整数族（含 id/count/qty 的常见类型名）。
func isIntType(t string) bool {
	for _, k := range []string{"int", "integer", "bigint", "smallint", "tinyint",
		"serial", "bigserial", "mediumint"} {
		if strings.Contains(t, k) {
			return true
		}
	}
	return false
}

// isFloatType 判小数族。
func isFloatType(t string) bool {
	for _, k := range []string{"float", "double", "decimal", "numeric", "real", "money"} {
		if strings.Contains(t, k) {
			return true
		}
	}
	return false
}

// isBoolType 判布尔族。
func isBoolType(t string) bool {
	return strings.Contains(t, "bool") || t == "bit" || t == "tinyint(1)"
}

// isDateType 判日期时间族。
func isDateType(t string) bool {
	for _, k := range []string{"date", "time", "timestamp"} {
		if strings.Contains(t, k) {
			return true
		}
	}
	return false
}

// isUUIDType 判 UUID 族（PG 的 uuid、MySQL 的 char(36) 由列名兜底）。
func isUUIDType(t string) bool {
	return strings.Contains(t, "uuid") || strings.Contains(t, "uniqueidentifier")
}

// isBlobType 判二进制族。
func isBlobType(t string) bool {
	return strings.Contains(t, "blob") || strings.Contains(t, "binary") ||
		strings.Contains(t, "bytea") || strings.Contains(t, "bytes")
}

// genRand 是确定性的伪随机源（seed 相同 → 序列相同，测试可复现）。
type genRand struct {
	state uint64
}

func newGenRand(seed uint64) *genRand {
	if seed == 0 {
		var b [8]byte
		_, _ = rand.Read(b[:])
		seed = binary.LittleEndian.Uint64(b[:])
	}
	if seed == 0 {
		seed = 1
	}
	// splitmix64 初始化。
	return &genRand{state: seed}
}

func (r *genRand) next() uint64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

func (r *genRand) int64(min, max int64) int64 {
	if max <= min {
		return min
	}
	return min + int64(r.next()%uint64(max-min))
}

func (r *genRand) float64(min, max float64) float64 {
	return min + (max-min)*float64(r.next()%1_000_000)/1_000_000
}

var genWords = []string{"alpha", "bravo", "delta", "orbit", "quartz", "harbor",
	"meadow", "vector", "signal", "atlas"}

func (r *genRand) word() string {
	return genWords[r.int64(0, int64(len(genWords)))]
}

// uuid 生成 RFC 4122 v4 形状的字符串。
func (r *genRand) uuid() string {
	var b [16]byte
	binary.LittleEndian.PutUint64(b[0:8], r.next())
	binary.LittleEndian.PutUint64(b[8:16], r.next())
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (r *genRand) hex(n int) string {
	var buf strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&buf, "%02X", byte(r.next()))
	}
	return buf.String()
}

// dialectQuote 按方言取标识符引用器。
func dialectQuote(dialect string) struct{ ident func(string) string } {
	switch strings.ToLower(dialect) {
	case "mysql":
		return struct{ ident func(string) string }{ident: func(s string) string {
			return "`" + strings.ReplaceAll(s, "`", "``") + "`"
		}}
	case "sqlserver", "mssql":
		return struct{ ident func(string) string }{ident: func(s string) string {
			return "[" + strings.ReplaceAll(s, "]", "]]") + "]"
		}}
	default:
		return struct{ ident func(string) string }{ident: func(s string) string {
			return "\"" + strings.ReplaceAll(s, "\"", "\"\"") + "\""
		}}
	}
}
