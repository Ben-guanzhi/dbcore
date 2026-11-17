package dbcore

// 服务器会话/进程管理（Navicat 的「工具 ▸ 会话管理」、DBeaver 的
// "Database ▸ Sessions"、HeidiSQL 的 Process 面板）：
//
// 列出当前连接的引擎上**其他会话**的活动状态，并终止其中一个。数据与操作
// 都来自引擎自身的视图（information_schema.PROCESSLIST / pg_stat_activity /
// sys.dm_exec_requests），本包不发新协议指令。
//
// 形态：可选能力接口（ProcessLister），与 TriggerLister / ObjectHealthReader
// 同构——「引擎支持就提供，不支持报 ErrNotSupported」；客户端用 AsProcessLister
// 断言后按需渲染，避免客户端各自拼引擎方言 SQL（db-dev 的 ListProcesses 之前
// 就是这么做的，推广时收进内核）。

import (
	"context"
	"fmt"
	"strings"
)

// ServerProcess 是引擎报告的一条活动会话。字段是各引擎视图的公共交集，
// 空值表示该引擎不提供（UI 显示占位而非造数据）。
type ServerProcess struct {
	ID      string // 引擎侧的会话/进程 ID（PG 的 pid、MySQL 的 ID、MSSQL 的 session_id）
	User    string
	Client  string // 客户端地址；MySQL 无独立列时与 Host 同
	DB      string
	State   string // running / idle / sleeping …（引擎原生拼写）
	Command string // 语句类型或活动命令（MySQL 的 COMMAND 列）
	Query   string // 正在执行的语句（引擎可能截断——标明的是引擎的行为）
	Time    string // 已持续秒数（引擎报的是字符串，原样带出）
	Started string // 会话/语句开始时间（PG/MSSQL 有；MySQL 用 Time 近似）
}

// ProcessLister 是「列出并终止服务器会话」的可选能力。
//
// 实现约定：
//   - Processes 不包含**当前连接自己**（管理员看别人，不杀自己）；
//   - KillProcess 对自己的 ID 返回错误（客户端不用自己再判断）；
//   - 不支持的引擎返回 ErrNotSupported（nil, false 的断言让客户端先就筛掉）。
type ProcessLister interface {
	// Processes 列出活动会话（不含当前连接），按 ID 排序。
	Processes(ctx context.Context) ([]ServerProcess, error)
	// KillProcess 终止一个会话。
	KillProcess(ctx context.Context, id string) error
}

// AsProcessLister 是能力断言 helper。
func AsProcessLister(d Driver) (ProcessLister, bool) {
	x, ok := d.(ProcessLister)
	return x, ok
}

// validProcessID 校验引擎侧 ID 只含 digits（三引擎的 pid/ID/session_id 都是
// 数字）。KillProcess 拼进 SQL 的只有这个 ID——校验在内核做一次，客户端与
// 各实现都不用重复防注入。
func validProcessID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ValidProcessID 是引擎实现用的 ID 校验（KillProcess 拼进 SQL 前必须过它）。
func ValidProcessID(id string) bool { return validProcessID(id) }

// ProcessRow 把一行 catalog 查询结果抽成 ServerProcess（列名不敏感小写）。
// 缺 pid 的行返回错误——调用方按「跳过该行」处理，不 fail 整个列表。
func ProcessRow(cols []string, row []Value) (ServerProcess, error) {
	get := func(name string) (Value, bool) {
		for i, c := range cols {
			if strings.EqualFold(c, name) && i < len(row) {
				return row[i], true
			}
		}
		return Value{}, false
	}
	str := func(name string) string {
		if v, ok := get(name); ok {
			return v.String()
		}
		return ""
	}
	p := ServerProcess{
		ID:      str("pid"),
		User:    str("user"),
		Client:  str("client"),
		DB:      str("db"),
		State:   str("state"),
		Command: str("command"),
		Query:   str("query"),
		Time:    str("time"),
		Started: str("started"),
	}
	if p.ID == "" {
		return p, fmt.Errorf("process row without pid: %v", cols)
	}
	return p, nil
}
