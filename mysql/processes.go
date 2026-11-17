package mysql

// ProcessLister 能力（dbcore/processes.go 的接口）：MySQL 的
// information_schema.PROCESSLIST。不含当前连接（CONNECTION_ID() 排除）；
// KillProcess 走 KILL <id>，ID 只允许数字（内核 ValidProcessID 校验）。

import (
	"context"
	"fmt"
	"strings"

	"github.com/Ben-guanzhi/dbcore"
)

const processlistQuery = `
SELECT ID AS pid, USER AS user, HOST AS client, DB AS db,
       COMMAND AS command, TIME AS time, STATE AS state,
       LEFT(COALESCE(INFO, ''), 200) AS query
FROM information_schema.PROCESSLIST
WHERE ID <> CONNECTION_ID()
ORDER BY ID`

// Processes lists the server's other sessions.
func (d *MySQLDriver) Processes(ctx context.Context) ([]dbcore.ServerProcess, error) {
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
			continue // 缺 pid 的行不产出，也不 fail 整个列表
		}
		out = append(out, p)
	}
	return out, nil
}

// KillProcess terminates one session by its numeric ID.
func (d *MySQLDriver) KillProcess(ctx context.Context, id string) error {
	if !dbcore.ValidProcessID(id) {
		return fmt.Errorf("非法的进程 ID: %q", id)
	}
	_, err := d.Execute(ctx, "KILL "+strings.TrimSpace(id))
	return err
}
