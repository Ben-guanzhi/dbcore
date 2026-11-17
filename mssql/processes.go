package mssql

// ProcessLister 能力（dbcore/processes.go 的接口）：SQL Server 的
// sys.dm_exec_requests × sys.dm_exec_sql_text。不含当前连接（@@SPID 排除）；
// KillProcess 走 KILL <session_id>（杀会话；语句级取消是请求内 ATTENTION，
// 这里对齐「杀会话」语义）。

import (
	"context"
	"fmt"
	"strings"

	"github.com/Ben-guanzhi/dbcore"
)

const processlistQuery = `
SELECT CAST(r.session_id AS varchar(20)) AS pid,
       COALESCE(s.login_name, '') AS user,
       COALESCE(r.status, '') AS state,
       COALESCE(DB_NAME(r.database_id), '') AS db,
       LEFT(COALESCE(t.text, ''), 200) AS query,
       r.start_time::varchar(30) AS started
FROM sys.dm_exec_requests r
LEFT JOIN sys.dm_exec_sessions s ON s.session_id = r.session_id
CROSS APPLY sys.dm_exec_sql_text(r.sql_handle) t
WHERE r.session_id <> @@SPID
ORDER BY r.session_id`

// Processes lists the server's other requests/sessions.
func (d *MSSQLDriver) Processes(ctx context.Context) ([]dbcore.ServerProcess, error) {
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

// KillProcess terminates one session by its numeric session_id.
func (d *MSSQLDriver) KillProcess(ctx context.Context, id string) error {
	if !dbcore.ValidProcessID(id) {
		return fmt.Errorf("非法的进程 ID: %q", id)
	}
	_, err := d.Execute(ctx, "KILL "+strings.TrimSpace(id))
	return err
}
