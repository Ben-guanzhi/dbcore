package postgres

// UserAdmin 能力（dbcore/users.go 的接口）：PostgreSQL 的可登录角色
// （rolcanlogin），DDL 按双引号标识符方言生成。

import (
	"context"
	"fmt"
	"strings"

	"github.com/Ben-guanzhi/dbcore"
)

// ListUsers 列出可登录角色。
func (d *PostgresDriver) ListUsers(ctx context.Context) ([]dbcore.ServerUser, error) {
	res, err := d.Execute(ctx, "SELECT rolname FROM pg_roles WHERE rolcanlogin ORDER BY rolname")
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
		u := dbcore.ServerUser{Name: get("rolname")}
		if u.Name != "" {
			out = append(out, u)
		}
	}
	return out, nil
}

// CreateUserDDL 生成创建用户的语句（PG 的 CREATE USER 即 LOGIN 角色）。
func (d *PostgresDriver) CreateUserDDL(user, host, password string) string {
	return fmt.Sprintf("CREATE USER %s WITH PASSWORD %s",
		dbcore.QuoteDoubleIdent(user), dbcore.QuoteSingleLiteral(password))
}

// DropUserDDL 生成删除用户的语句。
func (d *PostgresDriver) DropUserDDL(user, host string) string {
	return "DROP USER " + dbcore.QuoteDoubleIdent(user)
}

// GrantDatabaseDDL 生成把某库的完全权限授给用户的语句。
// 注意：GRANT ON DATABASE 需在目标库执行——客户端的执行闸门负责连接语境。
func (d *PostgresDriver) GrantDatabaseDDL(user, host, database string) string {
	return fmt.Sprintf("GRANT ALL PRIVILEGES ON DATABASE %s TO %s",
		dbcore.QuoteDoubleIdent(database), dbcore.QuoteDoubleIdent(user))
}
