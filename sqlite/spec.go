package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	"github.com/dbcore/dbcore"
)

func init() { dbcore.RegisterFactory(dbcore.KindSQLite, OpenSpec) }

// SpecFor builds a ConnSpec from a bare file path (or ":memory:"), the shape
// callers that never saw a full ConnSpec need. The pragmas ride in the DSN
// (see DSN), so every pooled physical connection gets them.
func SpecFor(path string) dbcore.ConnSpec {
	return dbcore.ConnSpec{Kind: dbcore.KindSQLite, File: path}
}

// DSN renders spec as a modernc.org/sqlite DSN. An empty File means an
// in-memory database (":memory:").
//
// File databases carry `_pragma` query parameters: modernc runs them on every
// physical connection the pool opens, not just the first one. Without them,
// busy_timeout/synchronous (both per-connection state) silently revert on the
// pool's secondary connections — the first connection had them, a concurrent
// writer borrowing the second got SQLITE_BUSY immediately. journal_mode=WAL
// persists in the file, so it is redundant here but harmless.
func DSN(spec dbcore.ConnSpec) string {
	if spec.File == "" {
		return ":memory:"
	}
	pragmas := []string{
		"journal_mode(WAL)",
		"busy_timeout(5000)",
		"synchronous(NORMAL)",
	}
	// A file path may already carry query parameters (e.g. `file:x.db?mode=ro`);
	// append instead of starting a second '?'.
	sep := "?"
	if strings.Contains(spec.File, "?") {
		sep = "&"
	}
	q := spec.File + sep
	for i, p := range pragmas {
		if i > 0 {
			q += "&"
		}
		q += "_pragma=" + url.QueryEscape(p)
	}
	return q
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
	// Enable WAL and a busy timeout on file-based databases. Without these, the
	// driver defaults to rollback journal (slower, holds exclusive locks longer)
	// and no busy timeout (concurrent writers fail immediately instead of waiting).
	// :memory: databases are not affected (they have no journal mode to set).
	if spec.File != "" {
		for _, pragma := range []string{
			"PRAGMA journal_mode=WAL",
			"PRAGMA busy_timeout=5000",
			"PRAGMA synchronous=NORMAL",
		} {
			if _, err := db.ExecContext(ctx, pragma); err != nil {
				_ = db.Close()
				return nil, fmt.Errorf("sqlite: %s: %w", pragma, err)
			}
		}
	}
	return &SQLiteDriver{db: db, dialect: &SQLiteDialect{}, owned: true}, nil
}
