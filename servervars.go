package dbcore

// 服务器变量/配置查看（Navicat「工具 ▸ 变量」、HeidiSQL 的 Variables 面板、
// DBeaver 的 Server Variables 编辑器）：
//
// 列出引擎的系统变量/设置（只读查看 + 名字过滤）。**不含改值**——SET GLOBAL
// 是有副作用的运维操作，各家权限语义不同（MySQL 需 SUPER、PG 的
// ALTER SYSTEM 只进配置文件），把它做成一键改值的 UI 会让「看看」变成「动
// 了服务器」；真要改的用户本来就会用 SQL。
//
// 形态：可选能力接口 ServerVariablesReader，与 ProcessLister / TriggerLister
// 同构——三引擎（MySQL SHOW VARIABLES / PG pg_settings / MSSQL
// sys.configurations），其余引擎断言失败自然降级。

import (
	"context"
	"strings"
)

// ServerVariable 是一条系统变量。Value 用引擎的原生拼写；Default/Range 空
// 表示引擎不提供（MySQL 的默认值 PG 的 boot_val/msssql 的
// config_value vs run_value 语义各不相同，不强归一）。
type ServerVariable struct {
	Name    string
	Value   string
	Default string
	Range   string // 允许范围/枚举（PG 的 enum 值、MSSQL 的 min/max）；空 = 不提供
	Desc    string // 引擎自带说明；空 = 不提供
}

// ServerVariablesReader 是「列出系统变量」的可选能力。
//
// 实现约定：
//   - 返回**全部**变量（分页/过滤是客户端的事——变量数在几百量级，一次拉取
//     便宜且客户端要做本地搜索）；
//   - 名字排序交给客户端（各引擎原生顺序不同，统一按名字排更可预期）；
//   - 不支持的引擎不实现本接口（AsServerVariables 断言失败 = UI 降级）。
type ServerVariablesReader interface {
	// ServerVariables lists all system variables of the connected server.
	ServerVariables(ctx context.Context) ([]ServerVariable, error)
}

// AsServerVariables 是能力断言 helper。
func AsServerVariables(d Driver) (ServerVariablesReader, bool) {
	x, ok := d.(ServerVariablesReader)
	return x, ok
}

// SortVariables 按名字排序（客户端统一口径——各引擎原生顺序不同）。
func SortVariables(vs []ServerVariable) {
	for i := 1; i < len(vs); i++ {
		for j := i; j > 0 && strings.ToLower(vs[j].Name) < strings.ToLower(vs[j-1].Name); j-- {
			vs[j], vs[j-1] = vs[j-1], vs[j]
		}
	}
}
