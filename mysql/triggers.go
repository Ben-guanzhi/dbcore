package mysql

// The TriggerLister surface for MySQL: information_schema.TRIGGERS lists them
// and SHOW CREATE TRIGGER gives the definition. The recipes live in sqltext.

import (
	"context"
	"fmt"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/sqltext"
)

// Triggers lists the table's triggers in name order, with MySQL's own timing,
// event and status spellings.
func (d *MySQLDriver) Triggers(ctx context.Context, schema, table string) ([]dbcore.TriggerInfo, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.TriggersQuery("mysql", schema, table)
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

// TriggerDefinition returns SHOW CREATE TRIGGER's text, which is the complete
// statement the engine would run to recreate it.
func (d *MySQLDriver) TriggerDefinition(ctx context.Context, schema, trigger string) (string, error) {
	q, a, err := sqltext.TriggerDefinitionQuery("mysql", schema, trigger)
	if err != nil {
		return "", err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 {
		return "", fmt.Errorf("mysql: trigger %q not found", trigger)
	}
	// SHOW CREATE TRIGGER answers (Trigger, sql_mode, SQL Original Statement,
	// ...): the statement is the third column, not the first.
	row := res.Rows[0]
	if len(row) >= 3 {
		return row[2].String(), nil
	}
	if len(row) > 0 {
		return row[0].String(), nil
	}
	return "", fmt.Errorf("mysql: trigger %q has no readable definition", trigger)
}

var _ dbcore.TriggerLister = (*MySQLDriver)(nil)
