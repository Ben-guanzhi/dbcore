package mssql

import (
	"context"
	"database/sql"
	"net/url"
	"strconv"
	"strings"

	"github.com/dbcore/dbcore"
)

func init() { dbcore.RegisterFactory(dbcore.KindMSSQL, OpenSpec) }

// DSN renders spec as a sqlserver:// URL understood by go-mssqldb.
//
// TLS follows the portable SSLMode: "disable" turns encryption off, "require"
// keeps encryption but tolerates a self-signed server certificate, and
// "verify-full"/"verify-ca" demand a trusted certificate.
func DSN(spec dbcore.ConnSpec) string {
	u := url.URL{
		Scheme: "sqlserver",
		User:   url.UserPassword(spec.User, spec.Password),
		Host:   spec.Addr(),
	}
	q := url.Values{}
	if spec.Database != "" {
		q.Set("database", spec.Database)
	}
	switch strings.ToLower(strings.TrimSpace(spec.SSLMode)) {
	case "", "require", "true", "on", "skip-verify", "insecure":
		q.Set("encrypt", "true")
		q.Set("TrustServerCertificate", "true")
	case "disable", "false", "off":
		// Encryption is off, but SQL Server still encrypts the login packet, so
		// the driver may still start TLS and verify the server certificate.
		q.Set("encrypt", "false")
		q.Set("TrustServerCertificate", "true")
	case "verify-ca", "verify-full":
		q.Set("encrypt", "true")
		q.Set("TrustServerCertificate", "false")
	}
	if spec.ConnectTimeout > 0 {
		q.Set("connection timeout", strconv.Itoa(int(spec.ConnectTimeout.Seconds())+1))
	}
	for k, v := range spec.Params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// OpenSpec implements dbcore.Factory. go-mssqldb exposes no per-connection
// dialer hook, so an SSH tunnel must be terminated by the caller and pointed at
// via Host/Port; a non-nil Dialer is rejected instead of being ignored.
func OpenSpec(ctx context.Context, spec dbcore.ConnSpec, pool dbcore.PoolConfig) (dbcore.Driver, error) {
	if spec.Dialer != nil {
		return nil, &dbcore.Error{Code: dbcore.ErrCodeNotSupported,
			Message: "mssql: custom Dialer is not supported; terminate the SSH tunnel and set Host/Port instead"}
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
	return &MSSQLDriver{db: db, dialect: &MSSQLDialect{}, owned: true}, nil
}
