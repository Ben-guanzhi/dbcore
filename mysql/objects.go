package mysql

// The ExtraMetadataReader surface for MySQL/MariaDB: view definitions, CHECK
// constraints and routines. Sequences do not exist as standalone objects
// (AUTO_INCREMENT is a table property), so that method answers ErrNotSupported.

import (
	"context"
	"fmt"
	"strings"

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

// RoutineDefinition returns SHOW CREATE PROCEDURE/FUNCTION text. MySQL has two
// different SHOW statements, so the routine's kind is read from
// information_schema first.
func (d *MySQLDriver) RoutineDefinition(ctx context.Context, schema, name string) (string, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return "", err
	}
	kind := dbcore.RoutineProcedure
	kq, ka, err := sqltext.RoutineKindQuery("mysql", schema, name)
	if err != nil {
		return "", err
	}
	if kres, kerr := dbcore.RunQuery(ctx, d.db, kq, dbcore.ValuesOf(ka)); kerr == nil && len(kres.Rows) > 0 {
		if kres.Rows[0][0].S != "" {
			kind = kres.Rows[0][0].S
		}
	}
	q, a, err := sqltext.RoutineDefinitionQuery("mysql", schema, name, kind)
	if err != nil {
		return "", err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 || len(res.Rows[0]) < 3 {
		return "", fmt.Errorf("mysql: routine %q not found", name)
	}
	// SHOW CREATE returns (name, sql_mode, Create <kind>, ...); the definition
	// column arrives as text or bytes depending on the server's column type.
	switch v := res.Rows[0][2]; v.Kind {
	case dbcore.ValText:
		return v.S, nil
	case dbcore.ValBlob:
		return string(v.B), nil
	}
	return "", fmt.Errorf("mysql: routine %q has no readable definition", name)
}

// Partitions lists a partitioned table's partitions (information_schema
// reports one row per partition; a plain table has none). TABLE_ROWS is an
// estimate from the storage engine, and arrives as text.
func (d *MySQLDriver) Partitions(ctx context.Context, schema, table string) ([]dbcore.PartitionInfo, error) {
	schema, err := d.resolveSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	q, a, err := sqltext.PartitionsQuery("mysql", schema, table)
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
		bound := ""
		if desc := strings.TrimSpace(r[1].S); desc != "" {
			bound = "LESS THAN (" + desc + ")"
		}
		out = append(out, dbcore.PartitionInfo{Name: r[0].S, Bound: bound, Rows: rows})
	}
	return out, nil
}

// EnumTypes is not supported: MySQL's ENUM is a column type, not a schema
// object. It already reaches consumers as the column's type text ("enum('a','b')").
func (d *MySQLDriver) EnumTypes(ctx context.Context, schema string) ([]dbcore.EnumTypeInfo, error) {
	return nil, dbcore.ErrNotSupported
}

var _ dbcore.ExtraMetadataReader = (*MySQLDriver)(nil)
