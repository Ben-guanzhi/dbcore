package mssql

import (
	"context"

	"github.com/dbcore/dbcore"
)

// ExecuteMulti answers every result set the statement emits: a batch of
// SELECTs, or a stored procedure's sets. The single-result Execute is unchanged
// and is what a caller that wants one grid keeps using.
func (d *MSSQLDriver) ExecuteMulti(ctx context.Context, query string, args ...dbcore.Value) ([]*dbcore.QueryResult, error) {
	sets, err := dbcore.RunMulti(ctx, d.db, query, args, 0)
	if err != nil {
		return sets, err
	}
	if len(sets) == 0 {
		// No set at all: a statement that only changed rows still owes the
		// caller its count, as Execute reports it.
		affected, lastID, err := dbcore.RunExec(ctx, d.db, query, args)
		if err != nil {
			return nil, err
		}
		return []*dbcore.QueryResult{{AffectedRows: affected, LastInsertID: lastID}}, nil
	}
	return sets, nil
}
