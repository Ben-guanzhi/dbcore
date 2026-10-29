package redis

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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
	if strings.EqualFold(spec.SSLMode, "require") || strings.EqualFold(spec.SSLMode, "verify-full") ||
		strings.EqualFold(spec.SSLMode, "verify-ca") {
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
	if mode := strings.ToLower(spec.SSLMode); mode == "require" || mode == "verify-full" || mode == "verify-ca" {
		// verify-ca used to fall through to a PLAINTEXT connection: the
		// portability layer lists it as a real mode (conn.go), and a
		// silent plaintext dial would send every key name and value in
		// clear text to a server the user asked to talk TLS to. The chain is
		// verified by hand below — the driver's hostname check stays opt-in
		// so the user's verify-ca choice stands.
		if mode == "verify-ca" {
			opts.TLSConfig = &tls.Config{
				MinVersion:            tls.VersionTLS12,
				InsecureSkipVerify:    true,
				VerifyPeerCertificate: redisVerifyChainOnly,
			}
		} else {
			opts.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		}
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

// redisVerifyChainOnly is the verify-ca counterpart of the MySQL driver's
// verifyChainOnly: trust the certificate chain, skip the hostname check.
func redisVerifyChainOnly(raw [][]byte, _ [][]*x509.Certificate) error {
	if len(raw) == 0 {
		return fmt.Errorf("redis: server presented no certificate")
	}
	leaf, err := x509.ParseCertificate(raw[0])
	if err != nil {
		return err
	}
	intermediates := x509.NewCertPool()
	for _, der := range raw[1:] {
		if cert, err := x509.ParseCertificate(der); err == nil {
			intermediates.AddCert(cert)
		}
	}
	_, err = leaf.Verify(x509.VerifyOptions{Intermediates: intermediates})
	return err
}
