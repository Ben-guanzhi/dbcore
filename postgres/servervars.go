package postgres

// ServerVariablesReader 能力（dbcore/servervars.go 的接口）：pg_settings 是
// PostgreSQL 变量的权威视图——boot_val（重启默认）/ reset_val（重置值）/
// min_val/max_val/enumvals（范围）/ short_desc（说明）都齐，比 SHOW ALL 多出
// 可展示的元数据。

import (
	"context"

	"github.com/Ben-guanzhi/dbcore"
)

const serverVariablesQuery = `
SELECT name, setting AS value,
       COALESCE(boot_val, '') AS default,
       COALESCE(
         CASE WHEN min_val IS NOT NULL AND max_val IS NOT NULL
              THEN min_val || ' .. ' || max_val
              ELSE enumvals::text END, '') AS range,
       COALESCE(short_desc, '') AS desc
FROM pg_settings
ORDER BY name`

// ServerVariables lists all system settings of the connected server.
func (d *PostgresDriver) ServerVariables(ctx context.Context) ([]dbcore.ServerVariable, error) {
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
				if stringsEqualFold(c, want) && i < len(row) {
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

// stringsEqualFold 是包内共享的 EqualFold（若 driver.go 已有则删此处）。
func stringsEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
