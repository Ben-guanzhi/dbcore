package dbcore

// 用户/权限管理（Navicat「用户」、HeidiSQL 的 User manager、DBeaver 的
// Users/Roles 视图）：
//
// 列出服务器上可登录的用户/角色，并生成创建/删除/授权的 DDL。
// **执行仍归客户端**（与 ProcessLister 不同——用户管理是高破坏性运维，客户端
// 各有确认闸门与审计路径），内核只提供两件事：**列表查询**与**方言正确的
// DDL 生成**。fynequery 的 user_mgmt.go 是现成参照（286 行客户端实现），
// 这里收进内核让其它仓共享。
//
// 明确不含：改密码（各家语法与加密语义差异大且敏感）、细粒度权限编辑
// （列级/表级授权的 UI 是一个完整产品）、密码哈希查看。

import (
	"context"
	"strings"
)

// ServerUser 是一个可登录的用户/角色。
type ServerUser struct {
	Name string
	Host string // MySQL 的 'user'@'host' 二元组；其它引擎空
}

// UserAdmin 是「列出用户 + 生成用户 DDL」的可选能力。
//
// 实现约定：
//   - ListUsers 只返回**可登录**的用户（MySQL: mysql.user 全量；PG: rolcanlogin）；
//   - 生成的 DDL 是方言正确的完整语句（引号、host 二元组、IDENTIFIED BY 等），
//     直接可执行；
//   - Create/Drop/Grant 语句由调用方执行并各自确认（本接口不执行）。
type UserAdmin interface {
	// ListUsers 列出可登录用户，按名字排序。
	ListUsers(ctx context.Context) ([]ServerUser, error)
	// CreateUserDDL 生成创建用户的语句。host 仅 MySQL 系使用（"" = '%'）。
	CreateUserDDL(user, host, password string) string
	// DropUserDDL 生成删除用户的语句。
	DropUserDDL(user, host string) string
	// GrantDatabaseDDL 生成把某库的完全权限授给用户的语句。
	// database 的引用（反引号/双引号）由实现按方言处理。
	GrantDatabaseDDL(user, host, database string) string
}

// AsUserAdmin 是能力断言 helper。
func AsUserAdmin(d Driver) (UserAdmin, bool) {
	x, ok := d.(UserAdmin)
	return x, ok
}

// quoteSingle 是 SQL 单引号字符串字面量（' → ”）。
func quoteSingle(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// QuoteSingleLiteral 是引擎实现生成 DDL 用的单引号字面量（密码等）。
func QuoteSingleLiteral(s string) string { return quoteSingle(s) }

// QuoteSingleIdent 是单引号包裹的「标识符位」（MySQL 的 'user'@'host' 用的是
// 单引号字面量语法而非标识符引用——引擎解析的是字符串）。
func QuoteSingleIdent(s string) string { return quoteSingle(s) }

// QuoteBacktickIdent 是 MySQL 的反引号标识符（数据库名）。
func QuoteBacktickIdent(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

// QuoteDoubleIdent 是 ANSI 双引号标识符。
func QuoteDoubleIdent(s string) string { return "\"" + strings.ReplaceAll(s, "\"", "\"\"") + "\"" }
