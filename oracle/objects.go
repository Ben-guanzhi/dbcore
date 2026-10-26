package oracle

// The ExtraMetadataReader surface for Oracle: view definitions, CHECK
// constraints, sequences and routines all exist in the data dictionary.

import (
	"context"
	"fmt"
	"strings"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/sqltext"
)

// ViewDefinition returns re-runnable DDL: ALL_VIEWS stores only the SELECT body
// (as a LONG column, which is why the composition happens here, not in SQL).
func (d *OracleDriver) ViewDefinition(ctx context.Context, schema, view string) (string, error) {
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return "", err
	}
	q, a, err := sqltext.ViewDefinitionQuery("oracle", s, view)
	if err != nil {
		return "", err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return "", fmt.Errorf("oracle: view %q not found", view)
	}
	return sqltext.ComposeViewDDL("oracle", s, view, res.Rows[0][0].String()), nil
}

// CheckConstraints lists the table's CHECK constraints, excluding the
// system-named NOT NULL constraints Oracle stores as checks.
func (d *OracleDriver) CheckConstraints(ctx context.Context, schema, table string) ([]dbcore.CheckConstraintInfo, error) {
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.CheckConstraintsQuery("oracle", s, table)
	if err != nil {
		return nil, err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.CheckConstraintInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		out = append(out, dbcore.CheckConstraintInfo{Name: r[0].S, Expression: r[1].S})
	}
	return out, nil
}

// Sequences lists the schema's user sequences (identity and materialized-view
// system sequences are filtered out by the recipe).
func (d *OracleDriver) Sequences(ctx context.Context, schema string) ([]dbcore.SequenceInfo, error) {
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.SequencesQuery("oracle", s)
	if err != nil {
		return nil, err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.SequenceInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		start, _ := dbcore.IntFrom(r[1])
		inc, _ := dbcore.IntFrom(r[2])
		out = append(out, dbcore.SequenceInfo{Name: r[0].S, Start: start, Increment: inc})
	}
	return out, nil
}

// Routines lists standalone procedures and functions (not package members).
func (d *OracleDriver) Routines(ctx context.Context, schema string) ([]dbcore.RoutineInfo, error) {
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.RoutinesQuery("oracle", s)
	if err != nil {
		return nil, err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.RoutineInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		out = append(out, dbcore.RoutineInfo{Name: r[0].S, Kind: r[1].S})
	}
	return out, nil
}

// RoutineDefinition reassembles ALL_SOURCE (one line per row) into the routine's
// source and puts the CREATE OR REPLACE header back in front of it.
func (d *OracleDriver) RoutineDefinition(ctx context.Context, schema, name string) (string, error) {
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return "", err
	}
	q, a, err := sqltext.RoutineDefinitionQuery("oracle", s, name, "")
	if err != nil {
		return "", err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, r := range res.Rows {
		if r[0].Kind != dbcore.ValText {
			continue
		}
		b.WriteString(strings.TrimRight(r[0].S, " \t\r\n"))
		b.WriteString("\n")
	}
	source := strings.TrimSpace(b.String())
	if source == "" {
		return "", fmt.Errorf("oracle: routine %q not found", name)
	}
	if !strings.HasPrefix(strings.ToUpper(source), "CREATE") {
		source = "CREATE OR REPLACE " + source
	}
	return source, nil
}

var _ dbcore.ExtraMetadataReader = (*OracleDriver)(nil)
