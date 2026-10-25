package mysql

// The ExtraMetadataReader surface for MySQL/MariaDB: view definitions, CHECK
// constraints and routines. Sequences do not exist as standalone objects
// (AUTO_INCREMENT is a table property), so that method answers ErrNotSupported.

import (
	"context"
	"fmt"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/sqltext"
)

// ViewDefinition returns re-runnable DDL: information_schema.VIEWS stores only
// the normalized SELECT body, so the CREATE OR REPLACE VIEW prefix is composed
// on top of it.
func (d *MySQLDriver) ViewDefinition(ctx context.Context, schema, view string) (string, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return "", err
	}
	q, a, err := sqltext.ViewDefinitionQuery("mysql", schema, view)
	if err != nil {
		return "", err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return "", fmt.Errorf("mysql: view %q not found", view)
	}
	return sqltext.ComposeViewDDL("mysql", schema, view, res.Rows[0][0].String()), nil
}

// CheckConstraints reads information_schema.CHECK_CONSTRAINTS (MySQL 8.0.16+
// and MariaDB 10.2+). On a server without that view the query fails and the
// error is returned rather than hidden: an empty list would claim the table has
// no checks.
func (d *MySQLDriver) CheckConstraints(ctx context.Context, schema, table string) ([]dbcore.CheckConstraintInfo, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.CheckConstraintsQuery("mysql", schema, table)
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

// Sequences is not supported: MySQL has no standalone sequence objects.
func (d *MySQLDriver) Sequences(ctx context.Context, schema string) ([]dbcore.SequenceInfo, error) {
	return nil, dbcore.ErrNotSupported
}

// Routines lists stored functions and procedures from information_schema.
func (d *MySQLDriver) Routines(ctx context.Context, schema string) ([]dbcore.RoutineInfo, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.RoutinesQuery("mysql", schema)
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

var _ dbcore.ExtraMetadataReader = (*MySQLDriver)(nil)
