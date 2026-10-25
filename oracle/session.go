package oracle

// The wide session on the built-in Oracle driver: metrics from v$version and
// v$sysstat. CreateDatabase is intentionally unsupported — Oracle databases
// are created via DBCA or DBA tooling, not from an ordinary client session —
// and the kernel's SessionCore reports that clearly.

import "github.com/dbcore/dbcore"

var _ dbcore.WideSession = (*OracleDriver)(nil)

func (d *OracleDriver) initWide() {
	d.SessionCore = dbcore.NewSessionCore(d.db, d.dialect)
	d.SessionCore.SetMetricsQuery("Oracle", `
		SELECT 'Version', banner FROM v$version WHERE ROWNUM = 1
		UNION ALL SELECT 'Sessions', TO_CHAR(value) FROM v$sysstat WHERE name = 'session count'
		UNION ALL SELECT 'User commits', TO_CHAR(value) FROM v$sysstat WHERE name = 'user commits'
		UNION ALL SELECT 'User rollbacks', TO_CHAR(value) FROM v$sysstat WHERE name = 'user rollbacks'
		UNION ALL SELECT 'Physical reads', TO_CHAR(value) FROM v$sysstat WHERE name = 'physical reads'`)
}
