package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strings"
	"sync/atomic"

	"github.com/dbcore/dbcore"
	drvmysql "github.com/go-sql-driver/mysql"
)

func init() { dbcore.RegisterFactory(dbcore.KindMySQL, OpenSpec) }

// tlsParam maps the portable SSLMode onto go-sql-driver's tls= values.
func tlsParam(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "disable", "false", "off":
		return ""
	case "allow", "preferred":
		return "preferred"
	case "require", "true", "on":
		// MySQL's tls=true verifies the server certificate; "skip-verify" is the
		// usual choice for self-signed servers, so "require" stays strict here.
		return "true"
	case "verify-ca", "verify-full":
		return "true"
	case "skip-verify", "insecure":
		return "skip-verify"
	default:
		return mode // custom TLS config name registered by the caller
	}
}

// customDialSeq names the go-sql-driver dialer registered per Dialer instance.
var customDialSeq atomic.Int64

// DSN renders spec as a go-sql-driver DSN. The password is escaped by
// Config.FormatDSN, so characters such as '@' or '/' are safe.
func DSN(spec dbcore.ConnSpec) string {
	cfg := drvmysql.NewConfig()
	cfg.User = spec.User
	cfg.Passwd = spec.Password
	cfg.Net = "tcp"
	cfg.Addr = spec.Addr()
	cfg.DBName = spec.Database
	cfg.ParseTime = true
	cfg.Timeout = spec.ConnectTimeout
	cfg.ReadTimeout = spec.ReadTimeout
	cfg.WriteTimeout = spec.WriteTimeout
	if tls := tlsParam(spec.SSLMode); tls != "" {
		cfg.TLSConfig = tls
	}
	for k, v := range spec.Params {
		if cfg.Params == nil {
			cfg.Params = map[string]string{}
		}
		cfg.Params[k] = v
	}
	if spec.Dialer != nil {
		// go-sql-driver resolves custom transports by registered network name.
		name := fmt.Sprintf("dbcore-dial-%d", customDialSeq.Add(1))
		drvmysql.RegisterDialContext(name, func(ctx context.Context, addr string) (net.Conn, error) {
			return spec.Dialer(ctx, "tcp", addr)
		})
		cfg.Net = name
	}
	return cfg.FormatDSN()
}

// OpenSpec implements dbcore.Factory.
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
	return &MySQLDriver{db: db, dialect: &MySQLDialect{}, owned: true}, nil
}
