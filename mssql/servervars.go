package mssql

// ServerVariablesReader 能力（dbcore/servervars.go 的接口）：SQL Server 的
// sys.configurations——config_value（重启生效）/ run_value（运行值）/ min/max。
// 两值不同 = 改了但未重启，UI 可高亮。

import (
	"context"
	"fmt"
	"strings"

	"github.com/Ben-guanzhi/dbcore"
)

const serverVariablesQuery = `
SELECT c.name,
       COALESCE(c.value_in_use, c.value) AS value,
       COALESCE(c.value, '') AS default,
       CASE WHEN c.minimum IS NOT NULL AND c.maximum IS NOT NULL
            THEN CAST(c.minimum AS varchar(20)) + ' .. ' + CAST(c.maximum AS varchar(20))
            ELSE '' END AS range,
       COALESCE(c.description, '') AS desc
FROM sys.configurations c
ORDER BY c.name`

// ServerVariables lists all server configuration values.
func (d *MSSQLDriver) ServerVariables(ctx context.Context) ([]dbcore.ServerVariable, error) {
	res, err := d.Execute(ctx, serverVariablesQuery)
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
			Name:    get("name"),
			Value:   get("value"),
			Default: get("default"),
			Range:   get("range"),
			Desc:    get("desc"),
		}
		if v.Name != "" {
			out = append(out, v)
		}
	}
	return out, nil
}

var _ = fmt.Sprint
