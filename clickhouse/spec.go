// Package clickhouse is the ClickHouse driver (W18).
//
// ClickHouse is an OLAP column store, and the differences from an OLTP engine
// are not cosmetic: it has no foreign keys and no enforced primary keys, its
// "indexes" are skip indexes rather than B-tree keys, and a row update is a
// mutation rather than an in-place write. The driver therefore refuses what
// the engine cannot express instead of pretending the catalog is relational.
//
// It speaks clickhouse-go's native protocol rather than the PostgreSQL port:
// the compatibility port exists, but it rejects a large part of the catalog
// queries a client needs (system.tables joins, skip-index metadata), so it is
// not a shortcut worth taking.
package clickhouse

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Ben-guanzhi/dbcore"
	"github.com/ClickHouse/clickhouse-go/v2"
)

func init() { dbcore.RegisterFactory(dbcore.KindClickHouse, OpenSpec) }

// DriverName is the database/sql name this package registers.
const DriverName = "clickhouse"

// spec.go — the DSN, the factory and the dialect.

// sslEnabled reports whether the portable SSLMode asks for TLS. ClickHouse has
// no sslmode ladder (no verify-ca/verify-full distinction): the connection is
// either secure or it is not, and certificate verification follows from the
// Go TLS defaults. A mode that asks for verification is honoured by leaving
// InsecureSkipVerify false, which is also the default.
func sslEnabled(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "disable", "false", "off":
		return false
	default:
		return true
	}
}

// DSN renders spec as a clickhouse:// URL. The database rides the path, the
// way ClickHouse's own URL form expects it.
func DSN(spec dbcore.ConnSpec) string {
	u := url.URL{
		Scheme: "clickhouse",
		Host:   spec.Addr(),
	}
	if spec.User != "" {
		if spec.Password != "" {
			u.User = url.UserPassword(spec.User, spec.Password)
		} else {
			u.User = url.User(spec.User)
		}
	}
	if spec.Database != "" {
		u.Path = "/" + spec.Database
	}
	q := url.Values{}
	// The native protocol's port: 9000 by default, 9440 when TLS is on.
	// Leaving it to the driver would silently pick the wrong one under TLS.
	q.Set("dial_timeout", dialTimeout(spec.ConnectTimeout))
	if sslEnabled(spec.SSLMode) {
		q.Set("secure", "true")
	}
	for k, v := range spec.Params {
		// sslmode is a portable spelling, not a ClickHouse setting.
		if strings.EqualFold(k, "sslmode") {
			continue
		}
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// dialTimeout renders the connect timeout the driver understands.
func dialTimeout(d time.Duration) string {
	if d <= 0 {
		d = 10 * time.Second
	}
	return d.String()
}

// OpenSpec opens a connection from a ConnSpec.
//
// A custom Dialer is refused rather than ignored: ClickHouse's driver takes a
// dialer through its own options, and silently dropping the caller's would
// route traffic around a tunnel the user asked for.
func OpenSpec(ctx context.Context, spec dbcore.ConnSpec, pool dbcore.PoolConfig) (dbcore.Driver, error) {
	if spec.Dialer != nil {
		return nil, &dbcore.Error{Code: dbcore.ErrCodeNotSupported,
			Message: "clickhouse: custom Dialer is not supported; terminate the SSH tunnel and set Host/Port instead"}
	}
	opts, err := clickhouse.ParseDSN(DSN(spec))
	if err != nil {
		return nil, fmt.Errorf("clickhouse: bad DSN: %w", err)
	}
	opts.MaxOpenConns = pool.MaxOpen
	opts.MaxIdleConns = pool.MaxIdle
	if pool.MaxLifetime > 0 {
		opts.ConnMaxLifetime = pool.MaxLifetime
	}
	db := clickhouse.OpenDB(opts)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &ClickHouseDriver{db: db, owned: true}, nil
}

// Open connects with a DSN (the form tests and callers that already have one
// use).
func Open(dsn string) (*ClickHouseDriver, error) {
	opts, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: bad DSN: %w", err)
	}
	db := clickhouse.OpenDB(opts)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &ClickHouseDriver{db: db, owned: true}, nil
}

// ensure sql.DB is kept imported for the driver struct's field type.
var _ *sql.DB
