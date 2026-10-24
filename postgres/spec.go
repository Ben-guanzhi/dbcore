package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/dbcore/dbcore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func init() { dbcore.RegisterFactory(dbcore.KindPostgres, OpenSpec) }

// sslMode maps the portable SSLMode onto libpq's sslmode values.
func sslMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "":
		return ""
	case "disable", "false", "off":
		return "disable"
	case "allow":
		return "allow"
	case "prefer", "preferred":
		return "prefer"
	case "require", "true", "on", "skip-verify", "insecure":
		return "require"
	case "verify-ca":
		return "verify-ca"
	case "verify-full":
		return "verify-full"
	default:
		return mode
	}
}

// DSN renders spec as a postgres:// URL. User/password are escaped by net/url.
func DSN(spec dbcore.ConnSpec) string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(spec.User, spec.Password),
		Host:   spec.Addr(),
	}
	if spec.Database != "" {
		u.Path = "/" + spec.Database
	}
	q := url.Values{}
	if m := sslMode(spec.SSLMode); m != "" {
		q.Set("sslmode", m)
	}
	if spec.Schema != "" {
		q.Set("search_path", spec.Schema)
	}
	if spec.ConnectTimeout > 0 {
		q.Set("connect_timeout", strconv.Itoa(int(spec.ConnectTimeout.Seconds())+1))
	}
	for k, v := range spec.Params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// OpenSpec implements dbcore.Factory. When spec.Dialer is set the connection is
// opened through it (e.g. an SSH tunnel), otherwise the plain pgx dialer is used.
func OpenSpec(ctx context.Context, spec dbcore.ConnSpec, pool dbcore.PoolConfig) (dbcore.Driver, error) {
	dsn := DSN(spec)
	var (
		db  *sql.DB
		err error
	)
	if spec.Dialer != nil {
		cfg, cerr := pgx.ParseConfig(dsn)
		if cerr != nil {
			return nil, fmt.Errorf("postgres: parse dsn: %w", cerr)
		}
		cfg.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return spec.Dialer(ctx, network, addr)
		}
		db = stdlib.OpenDB(*cfg)
	} else {
		db, err = sql.Open(DriverName, dsn)
		if err != nil {
			return nil, err
		}
	}
	pool.Apply(db)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &PostgresDriver{db: db, dialect: &PostgresDialect{}, owned: true}, nil
}
