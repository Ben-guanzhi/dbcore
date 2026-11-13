package oracle

// The TriggerLister surface for Oracle: USER_TRIGGERS lists them and holds both
// the description and the body. The recipes (which inline the upper-cased name,
// as the catalog stores upper case) live in sqltext.

import (
	"context"
	"fmt"
	"strings"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/sqltext"
)

// Triggers lists the table's triggers in name order, with Oracle's own timing,
// event and status spellings.
func (d *OracleDriver) Triggers(ctx context.Context, schema, table string) ([]dbcore.TriggerInfo, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.TriggersQuery("oracle", schema, table)
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

// TriggerDefinition returns the trigger's description followed by its body: the
// data dictionary keeps the two apart, so a reader needs both to see the
// statement.
func (d *OracleDriver) TriggerDefinition(ctx context.Context, schema, trigger string) (string, error) {
	q, a, err := sqltext.TriggerDefinitionQuery("oracle", schema, trigger)
	if err != nil {
		return "", err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return "", fmt.Errorf("oracle: trigger %q has no readable definition", trigger)
	}
	text := strings.TrimSpace(res.Rows[0][0].String())
	if text == "" {
		return "", fmt.Errorf("oracle: trigger %q has no readable definition", trigger)
	}
	return text, nil
}

var _ dbcore.TriggerLister = (*OracleDriver)(nil)
