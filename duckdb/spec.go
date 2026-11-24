// Package duckdb is the DuckDB driver (W20).
//
// DuckDB is an EMBEDDED columnar database: like SQLite it is a file, not a
// server, so ConnSpec.File carries the path and an empty path is an
// in-memory database. Unlike ClickHouse — the other column store here — it is
// a full relational engine: it enforces primary keys and foreign keys and it
// keeps real indexes, so the driver reports them instead of refusing.
//
// The CGO requirement is the one deployment cost: the engine is a C++ library
// bound through cgo, so a build needs a C toolchain. That is the same trade
// SQLite makes with its own driver, and it is why this engine is built in
// rather than loaded as a plugin.
package duckdb

// spec.go — the DSN and the factory.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/Ben-guanzhi/dbcore"
	_ "github.com/marcboeker/go-duckdb"
)

func init() { dbcore.RegisterFactory(dbcore.KindDuckDB, OpenSpec) }

// DriverName is the database/sql name the cgo binding registers.
const DriverName = "duckdb"

// inMemory is the DSN for an in-memory database. DuckDB spells it as an empty
// path or ":memory:"; the empty string is what the driver documents.
const inMemory = ""

// DSN renders spec as a DuckDB path. A file path is used verbatim; an empty
// File means an in-memory database.
func DSN(spec dbcore.ConnSpec) string {
	path := strings.TrimSpace(spec.File)
	if path == "" {
		return inMemory
	}
	return path
}

// OpenSpec opens a DuckDB database from a ConnSpec.
//
// A custom Dialer is refused: an embedded engine has no wire transport, so a
// caller that asked for one has misunderstood the engine, and ignoring the
// request would silently ignore the tunnel they named.
func OpenSpec(ctx context.Context, spec dbcore.ConnSpec, pool dbcore.PoolConfig) (dbcore.Driver, error) {
	if spec.Dialer != nil {
		return nil, &dbcore.Error{Code: dbcore.ErrCodeNotSupported,
			Message: "duckdb: 嵌入式引擎没有网络传输，Dialer 不适用"}
	}
	db, err := sql.Open(DriverName, DSN(spec))
	if err != nil {
		return nil, err
	}
	// An embedded database is single-writer; a large pool would only queue on
	// DuckDB's own lock, so the pool stays small by default.
	if pool.MaxOpen > 0 {
		db.SetMaxOpenConns(pool.MaxOpen)
	} else {
		db.SetMaxOpenConns(1)
	}
	pool.Apply(db)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &DuckDBDriver{db: db, owned: true}, nil
}

// Open connects with a path ("" = in-memory).
func Open(path string) (*DuckDBDriver, error) {
	db, err := sql.Open(DriverName, strings.TrimSpace(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &DuckDBDriver{db: db, owned: true}, nil
}

// ensure fmt stays imported for the error paths in the driver file.
var _ = fmt.Sprintf
