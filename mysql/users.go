package mysql

// UserAdmin 能力（dbcore/users.go 的接口）：MySQL 的用户是 'user'@'host'
// 二元组，DDL 全按反引号/单引号方言生成。

import (
	"context"
	"fmt"
	"strings"

	"github.com/Ben-guanzhi/dbcore"
)

// ListUsers 列出可登录用户。
func (d *MySQLDriver) ListUsers(ctx context.Context) ([]dbcore.ServerUser, error) {
	res, err := d.Execute(ctx, "SELECT user, host FROM mysql.user ORDER BY user, host")
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
		u := dbcore.ServerUser{Name: get("user"), Host: get("host")}
		if u.Name != "" {
			out = append(out, u)
		}
	}
	return out, nil
}

// CreateUserDDL 生成创建用户的语句（host 空 = '%'）。
func (d *MySQLDriver) CreateUserDDL(user, host, password string) string {
	if host == "" {
		host = "%"
	}
	return fmt.Sprintf("CREATE USER %s@%s IDENTIFIED BY %s",
		dbcore.QuoteSingleIdent(user), dbcore.QuoteSingleIdent(host),
		dbcore.QuoteSingleLiteral(password))
}

// DropUserDDL 生成删除用户的语句。
func (d *MySQLDriver) DropUserDDL(user, host string) string {
	if host == "" {
		host = "%"
	}
	return fmt.Sprintf("DROP USER %s@%s",
		dbcore.QuoteSingleIdent(user), dbcore.QuoteSingleIdent(host))
}

// GrantDatabaseDDL 生成把某库的完全权限授给用户的语句。
func (d *MySQLDriver) GrantDatabaseDDL(user, host, database string) string {
	if host == "" {
		host = "%"
	}
	return fmt.Sprintf("GRANT ALL PRIVILEGES ON %s.* TO %s@%s",
		dbcore.QuoteBacktickIdent(database),
		dbcore.QuoteSingleIdent(user), dbcore.QuoteSingleIdent(host))
}
