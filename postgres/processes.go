package postgres

// ProcessLister 能力（dbcore/processes.go 的接口）：PostgreSQL 的
// pg_stat_activity。不含当前连接（pg_backend_pid() 排除）；KillProcess 走
// pg_terminate_backend（硬终止会话；取消单条语句是 pg_cancel_backend，
// 这里对齐 Navicat/DBeaver「杀会话」的语义）。

import (
	"context"
	"fmt"
	"strings"

	"github.com/Ben-guanzhi/dbcore"
)

const processlistQuery = `
SELECT pid::text AS pid, usename AS user,
       COALESCE(client_addr::text, '') AS client,
       COALESCE(datname, '') AS db, state,
       LEFT(COALESCE(query, ''), 200) AS query,
       backend_start::text AS started
FROM pg_stat_activity
WHERE pid <> pg_backend_pid()
ORDER BY pid`

// Processes lists the server's other sessions.
func (d *PostgresDriver) Processes(ctx context.Context) ([]dbcore.ServerProcess, error) {
	res, err := d.Execute(ctx, processlistQuery)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(res.Columns))
	for i, c := range res.Columns {
		names[i] = c.Name
	}
	out := make([]dbcore.ServerProcess, 0, len(res.Rows))
	for _, row := range res.Rows {
		p, err := dbcore.ProcessRow(names, row)
		if err != nil {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// KillProcess terminates one backend by its pid.
func (d *PostgresDriver) KillProcess(ctx context.Context, id string) error {
	if !dbcore.ValidProcessID(id) {
		return fmt.Errorf("非法的进程 ID: %q", id)
	}
	_, err := d.Execute(ctx, "SELECT pg_terminate_backend("+strings.TrimSpace(id)+")")
	return err
}
