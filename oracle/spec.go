package oracle

import (
	"context"
	"database/sql"
	"net/url"
	"strings"

	"github.com/dbcore/dbcore"
)

func init() { dbcore.RegisterFactory(dbcore.KindOracle, OpenSpec) }

// DSN renders spec as an oracle:// URL understood by go-ora. The database part
// is the service name (or SID), i.e. "oracle://user:pass@host:1521/ORCLPDB1".
func DSN(spec dbcore.ConnSpec) string {
	u := url.URL{
		Scheme: "oracle",
		User:   url.UserPassword(spec.User, spec.Password),
		Host:   spec.Addr(),
	}
	service := spec.Database
	if service == "" {
		service = spec.Schema
	}
	if service != "" {
		u.Path = "/" + service
	}
	q := url.Values{}
	for k, v := range spec.Params {
		q.Set(k, v)
	}
	if strings.EqualFold(spec.SSLMode, "require") || strings.EqualFold(spec.SSLMode, "verify-full") {
		// go-ora enables TLS via the SSL parameter.
		q.Set("SSL", "true")
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// OpenSpec implements dbcore.Factory. go-ora has no per-connection dialer hook;
// an SSH tunnel must be terminated by the caller and pointed at via Host/Port.
func OpenSpec(ctx context.Context, spec dbcore.ConnSpec, pool dbcore.PoolConfig) (dbcore.Driver, error) {
	if spec.Dialer != nil {
		return nil, &dbcore.Error{Code: dbcore.ErrCodeNotSupported,
			Message: "oracle: custom Dialer is not supported; terminate the SSH tunnel and set Host/Port instead"}
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
	return &OracleDriver{db: db, dialect: &OracleDialect{}, owned: true}, nil
}
