package mysql

import (
	"context"
	"strings"

	"github.com/dbcore/dbcore"
)

// EstimatedRows reads one table's TABLE_ROWS: the catalog's estimate, from
// index statistics rather than a scan.
//
// The list of tables does not select TABLE_ROWS (that column makes the server
// compute statistics for every table in the schema, which turned a catalog read
// into a 30s stall); asking for one table keeps the cost to that table, which is
// what a pager's "about N rows" is worth.
func (d *MySQLDriver) EstimatedRows(ctx context.Context, schema, table string) (int64, bool, bool, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return 0, false, false, err
	}
	res, err := dbcore.RunQuery(ctx, d.db,
		`SELECT TABLE_ROWS FROM information_schema.TABLES
		 WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`,
		[]dbcore.Value{{Kind: dbcore.ValText, S: schema}, {Kind: dbcore.ValText, S: table}})
	if err != nil {
		return 0, false, false, err
	}
	if res == nil || len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return 0, false, false, nil // not found, or a view with no statistics
	}
	n, ok := dbcore.TryGetInt(res.Rows[0][0])
	if !ok || n < 0 {
		return 0, false, false, nil // NULL: statistics never gathered
	}
	return n, true, true, nil
}

// the interface check lives here so a missing method breaks the build, not the
// pager at runtime.
var _ dbcore.RowEstimator = (*MySQLDriver)(nil)

var _ = strings.TrimSpace
