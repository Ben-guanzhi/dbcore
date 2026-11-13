package sqlite

// The TriggerLister surface for SQLite: triggers live in sqlite_master, which
// keeps the whole CREATE TRIGGER text, so both the list and the definition come
// from the same table. The recipes (and their binds) live in sqltext.

import (
	"context"
	"fmt"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/sqltext"
)

// Triggers lists the table's triggers in name order. SQLite stores no timing or
// event column, so those come back empty and the recipe reports whether a
// trigger is enabled or was dropped.
func (d *SQLiteDriver) Triggers(ctx context.Context, schema, table string) ([]dbcore.TriggerInfo, error) {
	q, a, err := sqltext.TriggersQuery("sqlite", d.resolveSchema(schema), table)
	if err != nil {
		return nil, err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.TriggerInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		out = append(out, dbcore.TriggerInfo{
			Name:    r[0].S,
			Timing:  r[1].S,
			Event:   r[2].S,
			Enabled: r[3].S,
		})
	}
	return out, nil
}

// TriggerDefinition returns the trigger's CREATE TRIGGER statement as stored in
// sqlite_master.
func (d *SQLiteDriver) TriggerDefinition(ctx context.Context, schema, trigger string) (string, error) {
	q, a, err := sqltext.TriggerDefinitionQuery("sqlite", schema, trigger)
	if err != nil {
		return "", err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return "", fmt.Errorf("sqlite: trigger %q has no readable definition", trigger)
	}
	return res.Rows[0][0].String(), nil
}

var _ dbcore.TriggerLister = (*SQLiteDriver)(nil)
