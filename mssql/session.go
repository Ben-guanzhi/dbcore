package mssql

// The wide session on the built-in SQL Server driver: metrics from
// SERVERPROPERTY/sys catalog; CREATE DATABASE (with optional COLLATE) is
// handled by the kernel's SessionCore dispatch.

import "github.com/dbcore/dbcore"

var _ dbcore.WideSession = (*MSSQLDriver)(nil)

func (d *MSSQLDriver) initWide() {
	d.SessionCore = dbcore.NewSessionCore(d.db, d.dialect)
	d.SessionCore.SetMetricsQuery("SQL Server", `
		SELECT 'Version', CAST(SERVERPROPERTY('ProductVersion') AS varchar(64))
		UNION ALL SELECT 'Edition', CAST(SERVERPROPERTY('Edition') AS varchar(64))
		UNION ALL SELECT 'Active connections', CAST(COUNT(*) AS varchar) FROM sys.dm_exec_sessions WHERE is_user_process = 1
		UNION ALL SELECT 'Databases', CAST(COUNT(*) AS varchar) FROM sys.databases`)
}
