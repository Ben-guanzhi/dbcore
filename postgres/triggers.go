package postgres

// The TriggerLister surface for PostgreSQL: information_schema.TRIGGERS lists
// them and pg_get_triggerdef renders the whole CREATE TRIGGER. The recipes live
// in sqltext.

import (
	"context"
	"fmt"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/sqltext"
)

// Triggers lists the table's triggers in name order. PostgreSQL's
// information_schema has one row per event and no status column, so a trigger
// on INSERT and UPDATE appears twice and Enabled is empty.
func (d *PostgresDriver) Triggers(ctx context.Context, schema, table string) ([]dbcore.TriggerInfo, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.TriggersQuery("postgresql", schema, table)
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

// TriggerDefinition returns pg_get_triggerdef's text, which already is the whole
// "CREATE TRIGGER ..." statement.
func (d *PostgresDriver) TriggerDefinition(ctx context.Context, schema, trigger string) (string, error) {
	q, a, err := sqltext.TriggerDefinitionQuery("postgresql", schema, trigger)
	if err != nil {
		return "", err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return "", fmt.Errorf("postgres: trigger %q not found", trigger)
	}
	return res.Rows[0][0].String(), nil
}

var _ dbcore.TriggerLister = (*PostgresDriver)(nil)
