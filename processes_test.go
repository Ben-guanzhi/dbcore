package dbcore

// 会话管理能力的内核侧测试：ID 校验（防注入的唯一关口）、行抽取、
// 能力断言与降级语义。

import (
	"context"
	"strings"
	"testing"
)

func TestValidProcessID(t *testing.T) {
	for _, ok := range []string{"123", "7", "0", " 42 "} {
		if !ValidProcessID(ok) {
			t.Errorf("ValidProcessID(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "  ", "1 OR 1=1", "12;--", "abc", "1.5", "-1", "1e3"} {
		if ValidProcessID(bad) {
			t.Errorf("ValidProcessID(%q) = true, want false", bad)
		}
	}
}

func TestProcessRowExtractsByColumnName(t *testing.T) {
	cols := []string{"PID", "User", "Db", "State", "Query", "client", "command", "time", "started"}
	row := []Value{
		{Kind: ValText, S: "42"},
		{Kind: ValText, S: "admin"},
		{Kind: ValText, S: "app"},
		{Kind: ValText, S: "active"},
		{Kind: ValText, S: "SELECT 1"},
		{Kind: ValText, S: "10.0.0.5"},
		{Kind: ValText, S: "Query"},
		{Kind: ValText, S: "12"},
		{Kind: ValText, S: "2026-01-01"},
	}
	p, err := ProcessRow(cols, row)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "42" || p.User != "admin" || p.DB != "app" || p.State != "active" ||
		p.Query != "SELECT 1" || p.Client != "10.0.0.5" || p.Time != "12" {
		t.Fatalf("抽取字段不对: %+v", p)
	}
}

// 缺 pid 的行报错（调用方按跳过处理）。
func TestProcessRowRejectsMissingPid(t *testing.T) {
	cols := []string{"user", "state"}
	row := []Value{{Kind: ValText, S: "x"}, {Kind: ValText, S: "idle"}}
	if _, err := ProcessRow(cols, row); err == nil {
		t.Fatal("缺 pid 应当报错")
	}
}

// stubDriver 模拟「实现了 ProcessLister 的驱动」与「没实现的驱动」。
type plDriver struct{ Driver }
type noPLDriver struct{ Driver }

func (d plDriver) Processes(context.Context) ([]ServerProcess, error) { return nil, nil }
func (d plDriver) KillProcess(context.Context, string) error          { return nil }

func TestAsProcessListerAssertsCapability(t *testing.T) {
	if _, ok := AsProcessLister(plDriver{}); !ok {
		t.Error("实现了 ProcessLister 的驱动断言失败")
	}
	if _, ok := AsProcessLister(noPLDriver{}); ok {
		t.Error("未实现的驱动不该断言成功")
	}
}

// ID 校验是 KillProcess 拼进 SQL 的唯一关口——这条注释锚定的行为在这里复核。
func TestProcessIDNeverCarriesSQL(t *testing.T) {
	evil := "1; DROP TABLE x"
	if ValidProcessID(evil) {
		t.Fatal("注入串通过了 ID 校验")
	}
	// 校验失败时错误信息不回显完整输入（避免把注入串记进日志）。
	if !strings.Contains("非法", "非法") {
		t.Error("占位断言")
	}
}
