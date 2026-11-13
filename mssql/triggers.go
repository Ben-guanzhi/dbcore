package mssql

// The TriggerLister surface for SQL Server: sys.triggers lists a table's
// triggers and OBJECT_DEFINITION gives the definition. The recipes (which inline
// the object name, as this driver takes no binds on that path) live in sqltext.

import (
	"context"
	"fmt"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/sqltext"
)

// Triggers lists the table's triggers in name order.
func (d *MSSQLDriver) Triggers(ctx context.Context, schema, table string) ([]dbcore.TriggerInfo, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.TriggersQuery("sqlserver", schema, table)
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

// TriggerDefinition returns the trigger's source text from OBJECT_DEFINITION.
func (d *MSSQLDriver) TriggerDefinition(ctx context.Context, schema, trigger string) (string, error) {
	q, a, err := sqltext.TriggerDefinitionQuery("sqlserver", schema, trigger)
	if err != nil {
		return "", err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return "", fmt.Errorf("mssql: trigger %q has no readable definition", trigger)
	}
	return res.Rows[0][0].String(), nil
}

var _ dbcore.TriggerLister = (*MSSQLDriver)(nil)
