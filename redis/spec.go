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
// from Params["db"] (default 0). Pool knobs are left at the client's defaults;
// use OptionsWithPool to set them.
func Options(spec dbcore.ConnSpec) (*goredis.Options, error) {
	return options(spec, dbcore.PoolConfig{})
}

func options(spec dbcore.ConnSpec, pool dbcore.PoolConfig) (*goredis.Options, error) {
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
	// go-redis names the same knobs differently; the mapping is one-to-one.
	if pool.MaxOpen > 0 {
		opts.PoolSize = pool.MaxOpen
	}
	if pool.MaxIdle > 0 {
		opts.MinIdleConns = pool.MaxIdle
	}
	if pool.MaxLifetime > 0 {
		opts.ConnMaxLifetime = pool.MaxLifetime
	}
	if pool.MaxIdleTime > 0 {
		opts.ConnMaxIdleTime = pool.MaxIdleTime
	}
	return opts, nil
}

// OptionsWithPool is Options plus the connection-pool knobs. Redis used to
// ignore PoolConfig entirely, so a caller asking for a small pool still got the
// client's default of 10·CPU connections — on a busy server that is exactly the
// amplification pool settings exist to prevent.
func OptionsWithPool(spec dbcore.ConnSpec, pool dbcore.PoolConfig) (*goredis.Options, error) {
	return options(spec, pool)
}

// OpenSpec implements dbcore.Factory.
func OpenSpec(ctx context.Context, spec dbcore.ConnSpec, pool dbcore.PoolConfig) (dbcore.Driver, error) {
	if spec.Dialer != nil {
		return nil, &dbcore.Error{Code: dbcore.ErrCodeNotSupported,
			Message: "redis: custom Dialer is not supported; terminate the SSH tunnel and set Host/Port instead"}
	}
	opts, err := options(spec, pool)
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
