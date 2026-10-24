package redis

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	goredis "github.com/redis/go-redis/v9"

	"github.com/dbcore/dbcore"
)

func init() { dbcore.RegisterFactory(dbcore.KindRedis, OpenSpec) }

// DSN renders spec as a redis:// or rediss:// URL (diagnostics and tests).
func DSN(spec dbcore.ConnSpec) string {
	scheme := "redis"
	if strings.EqualFold(spec.SSLMode, "require") || strings.EqualFold(spec.SSLMode, "verify-full") {
		scheme = "rediss"
	}
	u := url.URL{
		Scheme: scheme,
		Host:   spec.Addr(),
	}
	if spec.User != "" || spec.Password != "" {
		u.User = url.UserPassword(spec.User, spec.Password)
	}
	if db := spec.Param("db", ""); db != "" && db != "0" {
		u.Path = "/" + db
	}
	return u.String()
}

// Options builds go-redis options for spec. Logical database selection comes
// from Params["db"] (default 0).
func Options(spec dbcore.ConnSpec) (*goredis.Options, error) {
	opts := &goredis.Options{
		Addr:     spec.Addr(),
		Username: spec.User,
		Password: spec.Password,
	}
	if raw := spec.Param("db", ""); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("redis: invalid db %q: %w", raw, err)
		}
		opts.DB = n
	}
	if mode := strings.ToLower(spec.SSLMode); mode == "require" || mode == "verify-full" {
		opts.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return opts, nil
}

// OpenSpec implements dbcore.Factory (pool settings do not apply to a KV client).
func OpenSpec(ctx context.Context, spec dbcore.ConnSpec, _ dbcore.PoolConfig) (dbcore.Driver, error) {
	if spec.Dialer != nil {
		return nil, &dbcore.Error{Code: dbcore.ErrCodeNotSupported,
			Message: "redis: custom Dialer is not supported; terminate the SSH tunnel and set Host/Port instead"}
	}
	opts, err := Options(spec)
	if err != nil {
		return nil, err
	}
	c := goredis.NewClient(opts)
	if err := c.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		return nil, err
	}
	cp := *opts
	return &RedisDriver{client: c, opts: &cp}, nil
}
