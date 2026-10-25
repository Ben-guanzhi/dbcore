package mysql

// The wide session on the built-in MySQL driver: curated metrics from SHOW
// GLOBAL STATUS, and CREATE DATABASE with charset/collation (handled by the
// kernel's SessionCore dispatch).

import (
	"context"
	"database/sql"

	"github.com/dbcore/dbcore"
)

var _ dbcore.WideSession = (*MySQLDriver)(nil)

func (d *MySQLDriver) initWide() {
	d.SessionCore = dbcore.NewSessionCore(d.db, d.dialect)
	d.SessionCore.SetMetrics(mysqlMetrics(d.db))
}

// mysqlMetrics reads SHOW GLOBAL STATUS (not one query shape, so not
// SetMetricsQuery) and presents the curated subset in a fixed order with human
// labels, mirroring the reference client's metrics panel.
func mysqlMetrics(db *sql.DB) func(context.Context) ([]dbcore.Metric, error) {
	return func(ctx context.Context) ([]dbcore.Metric, error) {
		rows, err := db.QueryContext(ctx, "SHOW GLOBAL STATUS WHERE Variable_name IN ("+
			"'Threads_connected','Threads_running','Uptime','Questions','Queries',"+
			"'Slow_queries','Bytes_received','Bytes_sent','Innodb_buffer_pool_read_requests',"+
			"'Innodb_buffer_pool_reads','Max_used_connections')")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		byName := map[string]string{}
		for rows.Next() {
			var name, val string
			if err := rows.Scan(&name, &val); err != nil {
				return nil, err
			}
			byName[name] = val
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		human := map[string]string{
			"Bytes_received":                   "Bytes received",
			"Bytes_sent":                       "Bytes sent",
			"Innodb_buffer_pool_read_requests": "InnoDB buffer reads (req)",
			"Innodb_buffer_pool_reads":         "InnoDB disk reads",
			"Max_used_connections":             "Peak connections",
		}
		order := []string{
			"Uptime", "Threads_connected", "Threads_running", "Max_used_connections",
			"Questions", "Queries", "Slow_queries",
			"Innodb_buffer_pool_read_requests", "Innodb_buffer_pool_reads",
			"Bytes_received", "Bytes_sent",
		}
		out := make([]dbcore.Metric, 0, len(order))
		for _, k := range order {
			if v, ok := byName[k]; ok {
				label := k
				if h, ok := human[k]; ok {
					label = h
				}
				out = append(out, dbcore.Metric{Label: label, Value: v, Group: "MySQL"})
			}
		}
		return out, nil
	}
}
