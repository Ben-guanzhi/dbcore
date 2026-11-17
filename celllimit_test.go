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
