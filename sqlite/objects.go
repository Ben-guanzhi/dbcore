package sqlite

// The ExtraMetadataReader surface for SQLite: views keep their complete DDL in
// sqlite_master, while CHECK constraints (only inside the CREATE TABLE text)
// and the sequence/routine concepts do not exist at all, so those methods
// answer ErrNotSupported for the client to hide the section.

import (
	"context"
	"fmt"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/sqltext"
)

// ViewDefinition returns the view's CREATE VIEW statement as stored in
// sqlite_master (SQLite keeps the original text, so this is exact DDL).
func (d *SQLiteDriver) ViewDefinition(ctx context.Context, schema, view string) (string, error) {
	q, a, err := sqltext.ViewDefinitionQuery("sqlite", schema, view)
	if err != nil {
		return "", err
	}
	res, err := dbcore.RunQuery(ctx, d.db, q, dbcore.ValuesOf(a))
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return "", fmt.Errorf("sqlite: view %q not found", view)
	}
	return sqltext.ComposeViewDDL("sqlite", schema, view, res.Rows[0][0].String()), nil
}

// CheckConstraints is not supported: SQLite stores CHECK clauses only inside
// the CREATE TABLE text and has no constraint catalog to read them from.
func (d *SQLiteDriver) CheckConstraints(ctx context.Context, schema, table string) ([]dbcore.CheckConstraintInfo, error) {
	return nil, dbcore.ErrNotSupported
}

// Sequences is not supported: SQLite has no standalone sequence objects
// (AUTOINCREMENT lives on the table).
func (d *SQLiteDriver) Sequences(ctx context.Context, schema string) ([]dbcore.SequenceInfo, error) {
	return nil, dbcore.ErrNotSupported
}

// Routines is not supported: SQLite has no stored functions or procedures.
func (d *SQLiteDriver) Routines(ctx context.Context, schema string) ([]dbcore.RoutineInfo, error) {
	return nil, dbcore.ErrNotSupported
}

// RoutineDefinition is not supported for the same reason as Routines.
func (d *SQLiteDriver) RoutineDefinition(ctx context.Context, schema, name string) (string, error) {
	return "", dbcore.ErrNotSupported
}

// Partitions is not supported: SQLite has no declarative table partitioning.
func (d *SQLiteDriver) Partitions(ctx context.Context, schema, table string) ([]dbcore.PartitionInfo, error) {
	return nil, dbcore.ErrNotSupported
}

// EnumTypes is not supported: SQLite has no user-defined enum types (a CHECK
// constraint is the closest equivalent, and it has no catalog either).
func (d *SQLiteDriver) EnumTypes(ctx context.Context, schema string) ([]dbcore.EnumTypeInfo, error) {
	return nil, dbcore.ErrNotSupported
}

var _ dbcore.ExtraMetadataReader = (*SQLiteDriver)(nil)
