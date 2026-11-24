// Package trino is the Trino driver (W19).
//
// Trino is not a database. It is a distributed SQL engine in front of a set of
// catalogs (each one a connector to something else — Hive, PostgreSQL,
// memory), and that shape decides how the driver answers the kernel's
// questions:
//
//   - the kernel's "schema" is a CATALOG here, and its "table" is
//     catalog.schema.table. ListSchemas lists catalogs; ListTables takes
//     "catalog" or "catalog.schema" and lists what is inside;
//   - there are no foreign keys, no indexes and no enforced constraints beyond
//     what a connector's underlying store enforces: Trino answers
//     ErrNotSupported rather than an empty list, which a client would read as
//     "this table has none";
//   - the information_schema exists per catalog, which is what the catalog
//     reads use.
//
// Everything goes over the client's HTTP protocol, so there is no CGO and no
// per-platform build matrix.
package trino

import (
	"context"
	"database/sql"
	"net/url"
	"strings"
	"time"

	"github.com/Ben-guanzhi/dbcore"
	_ "github.com/trinodb/trino-go-client/trino"
)

func init() { dbcore.RegisterFactory(dbcore.KindTrino, OpenSpec) }

// DriverName is the database/sql name the client registers.
const DriverName = "trino"

// DSN renders spec as a trino:// URL.
//
// Trino addresses (catalog, schema) on the CONNECTION, not per statement, so
// both ride the URL's path; the catalog is also reported back as the kernel's
// "schema" (see ListSchemas), which is what makes the tree browse one level
// deeper than a single-database engine.
func DSN(spec dbcore.ConnSpec) string {
	u := url.URL{
		Scheme: "trino",
		Host:   spec.Addr(),
	}
	if spec.User != "" {
		if spec.Password != "" {
			u.User = url.UserPassword(spec.User, spec.Password)
		} else {
			u.User = url.User(spec.User)
		}
	}
	// catalog[.schema] in the path.
	path := strings.TrimSpace(spec.Database)
	if sch := strings.TrimSpace(spec.Schema); sch != "" {
		if path != "" {
			path += "." + sch
		} else {
			path = sch
		}
	}
	if path != "" {
		u.Path = "/" + path
	}
	if enabled, mode := tlsMode(spec.SSLMode); enabled {
		q := url.Values{}
		if mode != "" {
			q.Set("custom_client", mode)
		}
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// tlsMode maps the portable SSLMode onto the client's TLS needs. Trino has no
// sslmode ladder; the client takes the scheme (https) or a custom client, so
// the portable spelling decides whether the scheme is upgraded.
func tlsMode(mode string) (enabled bool, custom string) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "disable", "false", "off":
		return false, ""
	default:
		return true, ""
	}
}

// OpenSpec opens a connection from a ConnSpec.
func OpenSpec(ctx context.Context, spec dbcore.ConnSpec, pool dbcore.PoolConfig) (dbcore.Driver, error) {
	if spec.Dialer != nil {
		return nil, &dbcore.Error{Code: dbcore.ErrCodeNotSupported,
			Message: "trino: custom Dialer is not supported; terminate the SSH tunnel and set Host/Port instead"}
	}
	db, err := sql.Open(DriverName, DSN(spec))
	if err != nil {
		return nil, err
	}
	pool.Apply(db)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &TrinoDriver{db: db, owned: true}, nil
}

// Open connects with a DSN.
func Open(dsn string) (*TrinoDriver, error) {
	db, err := sql.Open(DriverName, dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &TrinoDriver{db: db, owned: true}, nil
}

// dialTimeout keeps time imported for the spec's connect timeout helper.
var _ = time.Second
