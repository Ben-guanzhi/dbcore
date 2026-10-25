package mssql

// The ExtraMetadataReader surface for SQL Server: view definitions, CHECK
// constraints, sequences and routines all exist; the recipes live in sqltext.

import (
	"context"
	"fmt"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/sqltext"
)

// ViewDefinition returns the stored CREATE VIEW statement (OBJECT_DEFINITION).
func (d *MSSQLDriver) ViewDefinition(ctx context.Context, schema, view string) (string, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return "", err
	}
	q, a, err := sqltext.ViewDefinitionQuery("sqlserver", schema, view)
	if err != nil {
		return "", err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 || res.Rows[0][0].Kind == dbcore.ValNull {
		return "", fmt.Errorf("mssql: view %q not found", view)
	}
	return sqltext.ComposeViewDDL("sqlserver", schema, view, res.Rows[0][0].String()), nil
}

// CheckConstraints lists the table's CHECK constraints (sys.check_constraints).
func (d *MSSQLDriver) CheckConstraints(ctx context.Context, schema, table string) ([]dbcore.CheckConstraintInfo, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.CheckConstraintsQuery("sqlserver", schema, table)
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

// Sequences lists the schema's sequence objects (sys.sequences).
func (d *MSSQLDriver) Sequences(ctx context.Context, schema string) ([]dbcore.SequenceInfo, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.SequencesQuery("sqlserver", schema)
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

// Routines lists procedures and functions, excluding MS-shipped objects.
func (d *MSSQLDriver) Routines(ctx context.Context, schema string) ([]dbcore.RoutineInfo, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.RoutinesQuery("sqlserver", schema)
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

var _ dbcore.ExtraMetadataReader = (*MSSQLDriver)(nil)
