package postgres

// The ExtraMetadataReader surface for PostgreSQL: view definitions, CHECK
// constraints, sequences and routines. All four concepts exist, so every
// method answers real data; the recipes (and their binds) live in sqltext.

import (
	"context"
	"fmt"
	"strings"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/sqltext"
)

// ViewDefinition returns re-runnable DDL: pg_views stores only the SELECT body,
// so the CREATE OR REPLACE VIEW prefix is composed on top of it.
func (d *PostgresDriver) ViewDefinition(ctx context.Context, schema, view string) (string, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return "", err
	}
	q, a, err := sqltext.ViewDefinitionQuery("postgresql", schema, view)
	if err != nil {
		return "", err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return "", fmt.Errorf("postgres: view %q not found", view)
	}
	return sqltext.ComposeViewDDL("postgresql", schema, view, res.Rows[0][0].String()), nil
}

// CheckConstraints lists the table's user-declared CHECK constraints.
func (d *PostgresDriver) CheckConstraints(ctx context.Context, schema, table string) ([]dbcore.CheckConstraintInfo, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.CheckConstraintsQuery("postgresql", schema, table)
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

// Sequences lists the schema's sequences, including the ones PostgreSQL
// creates for serial/identity columns (they are ordinary sequences).
func (d *PostgresDriver) Sequences(ctx context.Context, schema string) ([]dbcore.SequenceInfo, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.SequencesQuery("postgresql", schema)
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

// Routines lists functions and procedures (pg_proc prokind f/p).
func (d *PostgresDriver) Routines(ctx context.Context, schema string) ([]dbcore.RoutineInfo, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.RoutinesQuery("postgresql", schema)
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

// RoutineDefinition returns pg_get_functiondef for every overload of name,
// joined by a blank line (PostgreSQL allows several functions with one name).
func (d *PostgresDriver) RoutineDefinition(ctx context.Context, schema, name string) (string, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return "", err
	}
	// The kind is not needed: pg_get_functiondef renders it.
	q, a, err := sqltext.RoutineDefinitionQuery("postgresql", schema, name, "")
	if err != nil {
		return "", err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(res.Rows))
	for _, r := range res.Rows {
		if r[0].Kind == dbcore.ValText && strings.TrimSpace(r[0].S) != "" {
			parts = append(parts, strings.TrimRight(r[0].S, "\n"))
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("postgres: routine %q not found", name)
	}
	return strings.Join(parts, "\n\n"), nil
}

// Partitions lists a partitioned table's partitions. The parent itself is a
// relkind='p' shell that holds no rows, so its children are what a structure
// page shows; a plain table answers an empty list.
func (d *PostgresDriver) Partitions(ctx context.Context, schema, table string) ([]dbcore.PartitionInfo, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.PartitionsQuery("postgresql", schema, table)
	if err != nil {
		return nil, err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.PartitionInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		rows, _ := dbcore.IntFrom(r[2])
		out = append(out, dbcore.PartitionInfo{Name: r[0].S, Bound: strings.TrimSpace(r[1].S), Rows: rows})
	}
	return out, nil
}

// EnumTypes groups pg_enum's one-row-per-label into one entry per type,
// preserving declaration order.
func (d *PostgresDriver) EnumTypes(ctx context.Context, schema string) ([]dbcore.EnumTypeInfo, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.EnumTypesQuery("postgresql", schema)
	if err != nil {
		return nil, err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.EnumTypeInfo, 0, len(res.Rows))
	index := make(map[string]int, len(res.Rows))
	for _, r := range res.Rows {
		name := r[0].S
		if i, ok := index[name]; ok {
			out[i].Labels = append(out[i].Labels, r[1].S)
			continue
		}
		index[name] = len(out)
		out = append(out, dbcore.EnumTypeInfo{Name: name, Labels: []string{r[1].S}})
	}
	return out, nil
}

var _ dbcore.ExtraMetadataReader = (*PostgresDriver)(nil)
