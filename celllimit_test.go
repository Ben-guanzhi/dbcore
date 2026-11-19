package dbcore

// 单元格大小上限：行数有各自的 cap，但一个巨大的 TEXT / BLOB 单值能绕过它
// 撑爆客户端内存。valueForColumn 是内核唯一的取值转换点，在这里收口。

import (
	"strings"
	"testing"
)

func TestClipCellLeavesShortValuesAlone(t *testing.T) {
	for _, s := range []string{"", "hello", strings.Repeat("x", 1000)} {
		if got := ClipCell(s); got != s {
			t.Errorf("ClipCell 改动了短值（len=%d）", len(s))
		}
	}
}

func TestClipCellTruncatesAndReportsLength(t *testing.T) {
	huge := strings.Repeat("x", MaxCellRunes+500)
	got := ClipCell(huge)
	if !strings.Contains(got, "单元格已截断") {
		t.Error("截断没有标明")
	}
	if !strings.Contains(got, "原长") {
		t.Error("截断没有报告原长度")
	}
	if n := len([]rune(got)); n > MaxCellRunes+80 {
		t.Errorf("截断后仍过长: %d runes", n)
	}
}

// 多字节字符按 rune 截断，不能切半个字符。
func TestClipCellIsRuneSafe(t *testing.T) {
	huge := strings.Repeat("数", MaxCellRunes+10)
	got := ClipCell(huge)
	if strings.ContainsRune(got, '\uFFFD') {
		t.Error("截断产生了非法 UTF-8 替换字符")
	}
	if !strings.HasPrefix(got, "数") {
		t.Error("多字节字符被破坏")
	}
}

// 取值出口：超长文本被收口。
func TestValueForColumnClipsHugeText(t *testing.T) {
	huge := strings.Repeat("y", MaxCellRunes+100)
	got := valueForColumn(huge, "TEXT")
	if got.Kind != ValText {
		t.Fatalf("kind = %v, want ValText", got.Kind)
	}
	if !strings.Contains(got.S, "单元格已截断") {
		t.Error("valueForColumn 没有收口超长文本")
	}
}

// 整数/浮点等非文本值不受影响。
func TestValueForColumnKeepsNumbers(t *testing.T) {
	if got := valueForColumn(int64(42), "BIGINT"); got.Kind != ValInt || got.I != 42 {
		t.Errorf("整数被改动: %+v", got)
	}
	if got := valueForColumn(3.5, "DOUBLE"); got.Kind != ValFloat || got.F != 3.5 {
		t.Errorf("浮点被改动: %+v", got)
	}
}

// 二进制列走同一个取值出口：超长 BLOB 按字节截断并携带原长标注，
// 短 BLOB 原样保留（截断逻辑不误伤正常数据）。
func TestValueForColumnClipsHugeBlob(t *testing.T) {
	huge := make([]byte, MaxBlobBytes+1024)
	for i := range huge {
		huge[i] = byte(i % 251)
	}
	got := valueForColumn(huge, "BLOB")
	if got.Kind != ValBlob {
		t.Fatalf("kind = %v, want ValBlob", got.Kind)
	}
	if len(got.B) != MaxBlobBytes {
		t.Fatalf("blob len = %d, want capped at %d", len(got.B), MaxBlobBytes)
	}
	if !strings.Contains(got.S, "已截断") || !strings.Contains(got.S, "原长") {
		t.Errorf("截断没有携带原长标注: %q", got.S)
	}
	// 内容是前缀（未被改写）。
	if got.B[0] != 0 || got.B[MaxBlobBytes-1] != byte((MaxBlobBytes-1)%251) {
		t.Error("截断破坏了前缀内容")
	}
}

func TestValueForColumnKeepsShortBlob(t *testing.T) {
	small := []byte{1, 2, 3, 4}
	got := valueForColumn(small, "BLOB")
	if got.Kind != ValBlob || len(got.B) != 4 {
		t.Fatalf("short blob mangled: kind=%v len=%d", got.Kind, len(got.B))
	}
	if got.S != "" {
		t.Errorf("未截断的 BLOB 不该带标注: %q", got.S)
	}
}

// PG 扩展文本类型（lib/pq 扫成 []byte 的那些）必须归一成文本：
// jsonb/inet/cidr 的网格显示是内容本身，不是 "<blob N bytes>"。
func TestValueForColumnNormalizesPostgresTextExtensions(t *testing.T) {
	cases := []struct {
		typeName string
		payload  string
	}{
		{"JSONB", `{"k": 1}`},
		{"jsonb", `[1,2,3]`},
		{"INET", "192.168.1.10"},
		{"CIDR", "10.0.0.0/8"},
		{"MACADDR", "08:00:2b:01:02:03"},
	}
	for _, c := range cases {
		got := valueForColumn([]byte(c.payload), c.typeName)
		if got.Kind != ValText {
			t.Errorf("%s: kind = %v, want ValText", c.typeName, got.Kind)
		}
		if got.S != c.payload {
			t.Errorf("%s: text = %q, want %q", c.typeName, got.S, c.payload)
		}
	}
}
