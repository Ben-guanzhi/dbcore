package postgres

// The wide session on the built-in PostgreSQL driver: metrics from
// pg_stat_activity/pg_stat_database; CREATE DATABASE (with ENCODING/LC_COLLATE
// options) is handled by the kernel's SessionCore dispatch.

import "github.com/dbcore/dbcore"

var _ dbcore.WideSession = (*PostgresDriver)(nil)

func (d *PostgresDriver) initWide() {
	d.SessionCore = dbcore.NewSessionCore(d.db, d.dialect)
	d.SessionCore.SetMetricsQuery("PostgreSQL", `
		SELECT 'Version', current_setting('server_version')
		UNION ALL SELECT 'Database', current_database()
		UNION ALL SELECT 'Active connections', count(*)::text FROM pg_stat_activity
		UNION ALL SELECT 'Max connections', current_setting('max_connections')
		UNION ALL SELECT 'Cache hit ratio',
			round(100.0*blks_hit/NULLIF(blks_hit+blks_read,0),1)::text || '%'
			FROM pg_stat_database WHERE datname = current_database()`)
}
