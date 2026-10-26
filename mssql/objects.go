package mssql

// The ExtraMetadataReader surface for SQL Server: view definitions, CHECK
// constraints, sequences and routines all exist; the recipes live in sqltext.

import (
	"context"
	"fmt"
	"strings"

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

// RoutineDefinition returns the stored CREATE text (OBJECT_DEFINITION), which
// SQL Server keeps verbatim for procedures and functions.
func (d *MSSQLDriver) RoutineDefinition(ctx context.Context, schema, name string) (string, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return "", err
	}
	q, a, err := sqltext.RoutineDefinitionQuery("sqlserver", schema, name, "")
	if err != nil {
		return "", err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 || res.Rows[0][0].Kind != dbcore.ValText ||
		strings.TrimSpace(res.Rows[0][0].S) == "" {
		return "", fmt.Errorf("mssql: routine %q not found", name)
	}
	return res.Rows[0][0].S, nil
}

// Partitions lists a partitioned table's partitions. SQL Server numbers them
// and has no partition names, so Name is the number as text. Every table has a
// partition 1 on the heap/clustered index, so a row only counts as a partition
// when the index actually lives on a partition scheme (r[4] non-empty).
// The boundary value attached to partition N is its upper bound for RANGE LEFT
// and its exclusive upper bound for RANGE RIGHT.
func (d *MSSQLDriver) Partitions(ctx context.Context, schema, table string) ([]dbcore.PartitionInfo, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.PartitionsQuery("sqlserver", schema, table)
	if err != nil {
		return nil, err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.PartitionInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		if strings.TrimSpace(r[4].S) == "" {
			continue // not partitioned: the table's single default partition
		}
		bound := "unbounded"
		if value := strings.TrimSpace(r[1].S); value != "" {
			op := "<= "
			if r[3].I != 0 {
				op = "< "
			}
			bound = op + value
		}
		rows, _ := dbcore.IntFrom(r[2])
		out = append(out, dbcore.PartitionInfo{Name: r[0].S, Bound: bound, Rows: rows})
	}
	return out, nil
}

// EnumTypes is not supported: SQL Server has no user-defined enum types (a
// CHECK constraint or a lookup table is the usual substitute).
func (d *MSSQLDriver) EnumTypes(ctx context.Context, schema string) ([]dbcore.EnumTypeInfo, error) {
	return nil, dbcore.ErrNotSupported
}

var _ dbcore.ExtraMetadataReader = (*MSSQLDriver)(nil)
