package dm

// Connection spec → DSN for the vendored DM driver, and the factory registration.

import (
	"context"
	"database/sql"
	"net/url"
	"strconv"
	"strings"

	"github.com/dbcore/dbcore"
)

func init() { dbcore.RegisterFactory(dbcore.KindDM, OpenSpec) }

// DSN renders spec as the `dm://user:pass@host:port?param=value` URL the vendored
// driver parses (see its parseDSN: scheme must be "dm", user comes from the URL's
// userinfo, everything else from the query).
//
// The database name travels as the driver's `schema` parameter: DM8 has no
// separate database layer inside a session, so what a user calls "the database" is
// a schema.
func DSN(spec dbcore.ConnSpec) string {
	u := url.URL{
		Scheme: DriverName,
		User:   url.UserPassword(spec.User, spec.Password),
		Host:   spec.Addr(),
	}
	q := url.Values{}
	if spec.Database != "" {
		q.Set("schema", spec.Database)
	}
	if spec.ConnectTimeout > 0 {
		// The driver's timeout is in seconds.
		q.Set("connectTimeout", strconv.Itoa(int(spec.ConnectTimeout.Seconds())+1))
	}
	for k, v := range spec.Params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// OpenSpec implements dbcore.Factory.
//
// A custom Dialer is refused rather than ignored: the vendored driver has no
// per-connection dial hook, so an SSH tunnel must be terminated by the caller and
// pointed at via Host/Port. Silently dropping the dialer would connect to the
// wrong place.
func OpenSpec(ctx context.Context, spec dbcore.ConnSpec, pool dbcore.PoolConfig) (dbcore.Driver, error) {
	if spec.Dialer != nil {
		return nil, &dbcore.Error{Code: dbcore.ErrCodeNotSupported,
			Message: "dm: custom Dialer is not supported; terminate the SSH tunnel and set Host/Port instead"}
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
	d := &DMDriver{db: db, dialect: &DMDialect{}, owned: true}
	d.initWide()
	return d, nil
}

// Open opens a connection from a dm:// DSN directly (tests, tools).
func Open(dsn string) (*DMDriver, error) {
	db, err := sql.Open(DriverName, dsn)
	if err != nil {
		return nil, err
	}
	d := &DMDriver{db: db, dialect: &DMDialect{}, owned: true}
	d.initWide()
	return d, nil
}

// normalizeSSLMode keeps the spelling; DM's SSL is configured through its own
// driver parameters (a certificate directory), not a boolean, so this client does
// not translate an SSL mode into a guess.
func normalizeSSLMode(mode string) string { return strings.ToLower(strings.TrimSpace(mode)) }
