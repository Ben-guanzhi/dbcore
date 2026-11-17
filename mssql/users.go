package mssql

// UserAdmin 能力（dbcore/users.go 的接口）：SQL Server 的登录（sys.sql_logins），
// DDL 按 [ ] 标识符方言生成。

import (
	"context"
	"fmt"
	"strings"

	"github.com/Ben-guanzhi/dbcore"
)

// ListUsers 列出 SQL 登录（可登录的服务器级主体）。
func (d *MSSQLDriver) ListUsers(ctx context.Context) ([]dbcore.ServerUser, error) {
	res, err := d.Execute(ctx,
		"SELECT CAST(name AS varchar(128)) AS name FROM sys.sql_logins WHERE is_disabled = 0 ORDER BY name")
	if err != nil {
		return nil, err
	}
	names := make([]string, len(res.Columns))
	for i, c := range res.Columns {
		names[i] = c.Name
	}
	out := make([]dbcore.ServerUser, 0, len(res.Rows))
	for _, row := range res.Rows {
		get := func(want string) string {
			for i, c := range names {
				if strings.EqualFold(c, want) && i < len(row) {
					return row[i].String()
				}
			}
			return ""
		}
		u := dbcore.ServerUser{Name: get("name")}
		if u.Name != "" {
			out = append(out, u)
		}
	}
	return out, nil
}

// bracketIdent 是 MSSQL 的 [ ] 标识符。
func bracketIdent(s string) string { return "[" + strings.ReplaceAll(s, "]", "]]") + "]" }

// CreateUserDDL 生成创建登录的语句（SQL Server 没有密码占位的占位符语法，
// 用默认数据库语境）。
func (d *MSSQLDriver) CreateUserDDL(user, host, password string) string {
	return fmt.Sprintf("CREATE LOGIN %s WITH PASSWORD = %s",
		bracketIdent(user), dbcore.QuoteSingleLiteral(password))
}

// DropUserDDL 生成删除登录的语句。
func (d *MSSQLDriver) DropUserDDL(user, host string) string {
	return "DROP LOGIN " + bracketIdent(user)
}

// GrantDatabaseDDL 生成把某库的 db_owner 授给登录的语句（先在库建用户再授
// 角色需要两步且要在库语境执行——客户端闸门负责；这里生成最常用的单语句）。
func (d *MSSQLDriver) GrantDatabaseDDL(user, host, database string) string {
	return fmt.Sprintf("USE %s; CREATE USER %s FOR LOGIN %s; EXEC sp_addrolemember 'db_owner', %s",
		bracketIdent(database), bracketIdent(user), bracketIdent(user), dbcore.QuoteSingleLiteral(user))
}
