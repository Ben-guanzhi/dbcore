package sqlite

import (
	"context"
	"database/sql"

	"github.com/dbcore/dbcore"
)

func init() { dbcore.RegisterFactory(dbcore.KindSQLite, OpenSpec) }

// DSN renders spec as a modernc.org/sqlite DSN. An empty File means an
// in-memory database (":memory:").
func DSN(spec dbcore.ConnSpec) string {
	if spec.File == "" {
		return ":memory:"
	}
	return spec.File
}

// OpenSpec implements dbcore.Factory: it opens SQLite and applies pool settings.
func OpenSpec(ctx context.Context, spec dbcore.ConnSpec, pool dbcore.PoolConfig) (dbcore.Driver, error) {
	db, err := sql.Open(DriverName, DSN(spec))
	if err != nil {
		return nil, err
	}
	pool.Apply(db)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &SQLiteDriver{db: db, dialect: &SQLiteDialect{}, owned: true}, nil
}
