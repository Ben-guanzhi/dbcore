package sqltext

import (
	"reflect"
	"testing"
)

func TestToggleLineCommentThreeStates(t *testing.T) {
	cases := []struct{ in, want string }{
		{"SELECT 1", "-- SELECT 1"},
		{"-- SELECT 1", "SELECT 1"},
		{"--SELECT 1", "SELECT 1"}, // 无空格的注释也移除
		{"  SELECT 1", "  -- SELECT 1"},
		{"  -- SELECT 1", "  SELECT 1"},
	}
	for _, c := range cases {
		if got := ToggleLineComment(c.in); got != c.want {
			t.Errorf("ToggleLineComment(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestToggleLinesCommentBlockSemantics(t *testing.T) {
	// 整块里有未注释行 → 全部加注释（空行跳过）。
	block := []string{"SELECT a", "", "SELECT b"}
	want := []string{"-- SELECT a", "", "-- SELECT b"}
	if got := ToggleLinesComment(block); !reflect.DeepEqual(got, want) {
		t.Fatalf("add block = %v, want %v", got, want)
	}
	// 整块全已注释（按去缩进判断）→ 全部移除。
	commented := []string{"-- SELECT a", "-- SELECT b"}
	wantOff := []string{"SELECT a", "SELECT b"}
	if got := ToggleLinesComment(commented); !reflect.DeepEqual(got, wantOff) {
		t.Fatalf("remove block = %v, want %v", got, wantOff)
	}
	// 缩进保留。
	indented := []string{"  -- SELECT a"}
	if got := ToggleLinesComment(indented); got[0] != "  SELECT a" {
		t.Fatalf("indent lost: %q", got[0])
	}
}
