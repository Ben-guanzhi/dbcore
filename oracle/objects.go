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

// Partitions lists a table's top-level partitions (ALL_TAB_PARTITIONS).
// HIGH_VALUE is a LONG column, so it comes back as its raw text ("10",
// "MAXVALUE", "DATE '2026-01-01'") and is wrapped into LESS THAN (...) here.
// Sub-partitions are not listed; NUM_ROWS is the optimizer's estimate.
func (d *OracleDriver) Partitions(ctx context.Context, schema, table string) ([]dbcore.PartitionInfo, error) {
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.PartitionsQuery("oracle", s, table)
	if err != nil {
		return nil, err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.PartitionInfo, 0, len(res.Rows))
	for _, r := range res.Rows {
		bound := ""
		if high := strings.TrimSpace(r[1].S); high != "" {
			bound = "LESS THAN (" + high + ")"
		}
		rows, _ := dbcore.IntFrom(r[2])
		out = append(out, dbcore.PartitionInfo{Name: r[0].S, Bound: bound, Rows: rows})
	}
	return out, nil
}

// EnumTypes is not supported: Oracle has no user-defined enum types (CHECK
// constraints over VARCHAR2 columns are the usual substitute).
func (d *OracleDriver) EnumTypes(ctx context.Context, schema string) ([]dbcore.EnumTypeInfo, error) {
	return nil, dbcore.ErrNotSupported
}

var _ dbcore.ExtraMetadataReader = (*OracleDriver)(nil)

// InvalidObjects lists the schema's INVALID objects (W9).
//
// An INVALID object is one Oracle failed to compile — typically a procedure whose
// table was dropped, or a view over a changed column. Listing them is the first
// half of the PL/SQL Developer workflow; RecompileObject is the second.
func (d *OracleDriver) InvalidObjects(ctx context.Context, schema string) ([]dbcore.InvalidObject, error) {
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.InvalidObjectsQuery("oracle", s)
	if err != nil {
		return nil, err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.InvalidObject, 0, len(res.Rows))
	for _, r := range res.Rows {
		out = append(out, dbcore.InvalidObject{Name: r[0].S, Type: r[1].S})
	}
	return out, nil
}

// ObjectDependencies lists an object's edges in both directions (W9).
//
// ALL_DEPENDENCIES stores one row per edge with the dependent on the left and the
// referenced object on the right, so a single query answers both questions; the
// split here is which side of the row the named object sits on.
func (d *OracleDriver) ObjectDependencies(ctx context.Context, schema, object string) ([]dbcore.ObjectDependency, error) {
	if strings.TrimSpace(object) == "" {
		return nil, fmt.Errorf("oracle: object name is required")
	}
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.ObjectDependenciesQuery("oracle", s, object)
	if err != nil {
		return nil, err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.ObjectDependency, 0, len(res.Rows))
	for _, r := range res.Rows {
		// Columns: OWNER, NAME, TYPE, REFERENCED_OWNER, REFERENCED_NAME, REFERENCED_TYPE
		owner, name, typ := r[0].S, r[1].S, r[2].S
		refOwner, refName, refType := r[3].S, r[4].S, r[5].S
		if strings.EqualFold(owner, s) && strings.EqualFold(name, object) {
			// The named object is the DEPENDENT: it depends on the referenced one.
			out = append(out, dbcore.ObjectDependency{
				Direction: dbcore.DependencyOut,
				Name:      refName, Type: refType, Owner: refOwner,
			})
			continue
		}
		// The named object is the REFERENCED one: this row's owner/name depends on it.
		out = append(out, dbcore.ObjectDependency{
			Direction: dbcore.DependencyIn,
			Name:      name, Type: typ, Owner: owner,
		})
	}
	return out, nil
}


// CompileErrors lists the diagnostics a failed compilation recorded (W9).
//
// ALTER … COMPILE SUCCEEDS even when the object cannot be compiled: Oracle writes
// the errors to ALL_ERRORS and leaves the object INVALID. That is why this method
// exists — "the statement ran" and "the object is fixed" are different facts, and
// a UI that conflated them would report a broken object as repaired.
func (d *OracleDriver) CompileErrors(ctx context.Context, schema, object string) ([]dbcore.CompileError, error) {
	if strings.TrimSpace(object) == "" {
		return nil, fmt.Errorf("oracle: object name is required")
	}
	s, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.CompileErrorsQuery("oracle", s, object)
	if err != nil {
		return nil, err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.CompileError, 0, len(res.Rows))
	for _, r := range res.Rows {
		line, _ := dbcore.IntFrom(r[0])
		pos, _ := dbcore.IntFrom(r[1])
		out = append(out, dbcore.CompileError{Line: int(line), Position: int(pos), Text: r[2].S})
	}
	return out, nil
}
