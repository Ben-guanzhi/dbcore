package mysql

// ServerVariablesReader 能力（dbcore/servervars.go 的接口）：
// SHOW VARIABLES 是 MySQL 变量的权威来源（session 级值；全局值在
// SHOW GLOBAL VARIABLES——客户端要的是「这个连接看到的值」，session 口径）。

import (
	"context"
	"strings"

	"github.com/Ben-guanzhi/dbcore"
)

// ServerVariables lists all system variables of the connected server.
func (d *MySQLDriver) ServerVariables(ctx context.Context) ([]dbcore.ServerVariable, error) {
	res, err := d.Execute(ctx, "SHOW VARIABLES")
	if err != nil {
		return nil, err
	}
	names := make([]string, len(res.Columns))
	for i, c := range res.Columns {
		names[i] = c.Name
	}
	out := make([]dbcore.ServerVariable, 0, len(res.Rows))
	for _, row := range res.Rows {
		get := func(want string) string {
			for i, c := range names {
				if strings.EqualFold(c, want) && i < len(row) {
					return row[i].String()
				}
			}
			return ""
		}
		v := dbcore.ServerVariable{
			Name:  get("Variable_name"),
			Value: get("Value"),
		}
		if v.Name != "" {
			out = append(out, v)
		}
	}
	return out, nil
}
