package dm

// The wide session on the DM driver.
//
// DM8 exposes no v$sysstat equivalent that this client can rely on across editions,
// so the metrics query is limited to what the server always answers: its version and
// the current user. A richer panel would need a DM instance to check against, which
// is exactly what this build did not have.

import "github.com/dbcore/dbcore"

var _ dbcore.WideSession = (*DMDriver)(nil)

func (d *DMDriver) initWide() {
	d.SessionCore = dbcore.NewSessionCore(d.db, d.dialect)
	d.SessionCore.SetMetricsQuery("DM", `
		SELECT 'Version', BANNER FROM V$VERSION WHERE ROWNUM = 1
		UNION ALL SELECT 'User', USER FROM DUAL`)
}
