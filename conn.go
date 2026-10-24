package dbcore

// 连接层（契约 §7.2 的补位）：把"怎么连"从各客户端收敛到内核。
//
// 设计取舍：
//   - 根包仍然不依赖任何驱动：ConnSpec 只是数据，真正的拨号由各引擎子包
//     （dbcore/mysql、dbcore/postgres、…）通过 init 时注册的 Factory 完成。
//     因此只 import dbcore 的消费者不会被迫引入 6 套驱动。
//   - 连接池参数（database/sql 的四个旋钮）由内核统一应用，避免每个客户端
//     自己决定 MaxOpen/MaxIdle 后行为不一致。
//   - SSH 隧道不引入内核依赖：调用方（客户端）负责建立隧道，把 Host/Port
//     指向本地转发端点即可；需要自定义传输时用 Dialer（目前 MySQL / PostgreSQL
//     支持，其余引擎返回 ErrNotSupported 而不是静默忽略）。

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// ConnSpec is the engine-neutral description of one connection. Only the fields
// meaningful for the chosen Kind are used; engines ignore the rest.
type ConnSpec struct {
	// Name is display-only (used in error messages); it is not part of the DSN.
	Name string
	// Kind selects the engine, e.g. KindPostgres.
	Kind DatabaseKind

	// Network endpoint (ignored by SQLite).
	Host string
	Port int
	User string
	Password string
	// Database is the database to select. For MySQL it doubles as the catalog
	// schema; for PostgreSQL it is the connecting database.
	Database string
	// Schema optionally pins the default schema (e.g. "public", "dbo").
	Schema string
	// File is the SQLite database path; "" means an in-memory database.
	File string
	// SSLMode is the portability layer for TLS: "disable", "require",
	// "verify-ca", "verify-full" or engine-specific values ("" = engine default).
	SSLMode string
	// Params carries engine-specific extras (e.g. "charset", "application_name").
	// Values are appended verbatim to the DSN, so they must already be escaped.
	Params map[string]string
	// ConnectTimeout bounds the initial dial (0 = engine default).
	ConnectTimeout time.Duration
	// Dialer overrides the TCP transport, e.g. to route through an in-process
	// SSH tunnel. Engines that cannot honour it return ErrNotSupported.
	Dialer Dialer
}

// Addr renders host:port with the engine's default port when Port is 0.
func (s ConnSpec) Addr() string {
	port := s.Port
	if port == 0 {
		port = DefaultPort(s.Kind)
	}
	return net.JoinHostPort(s.Host, fmt.Sprint(port))
}

// DefaultPort returns the conventional port for a network engine (0 for file engines).
func DefaultPort(kind DatabaseKind) int {
	switch kind {
	case KindMySQL:
		return 3306
	case KindPostgres:
		return 5432
	case KindMSSQL:
		return 1433
	case KindOracle:
		return 1521
	case KindRedis:
		return 6379
	}
	return 0
}

// Param returns Params[key] or def when absent.
func (s ConnSpec) Param(key, def string) string {
	if v, ok := s.Params[key]; ok {
		return v
	}
	return def
}

// Dialer opens a transport connection to addr (already host:port).
type Dialer func(ctx context.Context, network, addr string) (net.Conn, error)

// PoolConfig mirrors the four database/sql pool knobs. Zero values keep the
// database/sql defaults, so an empty PoolConfig is always valid.
type PoolConfig struct {
	MaxOpen     int
	MaxIdle     int
	MaxLifetime time.Duration
	MaxIdleTime time.Duration
}

// Apply configures db. It is a no-op for zero fields.
func (p PoolConfig) Apply(db *sql.DB) {
	if p.MaxOpen > 0 {
		db.SetMaxOpenConns(p.MaxOpen)
	}
	if p.MaxIdle > 0 {
		db.SetMaxIdleConns(p.MaxIdle)
	}
	if p.MaxLifetime > 0 {
		db.SetConnMaxLifetime(p.MaxLifetime)
	}
	if p.MaxIdleTime > 0 {
		db.SetConnMaxIdleTime(p.MaxIdleTime)
	}
}

// Factory opens a Driver for spec. Engine packages register one during init.
type Factory func(ctx context.Context, spec ConnSpec, pool PoolConfig) (Driver, error)

var factories = struct {
	sync.RWMutex
	m map[DatabaseKind]Factory
}{m: map[DatabaseKind]Factory{}}

// RegisterFactory installs the Factory for kind. It is meant to be called from
// an engine package's init; registering the same kind twice panics, matching
// database/sql.Register's behaviour for programming errors.
func RegisterFactory(kind DatabaseKind, f Factory) {
	if f == nil {
		panic("dbcore: nil factory for " + string(kind))
	}
	factories.Lock()
	defer factories.Unlock()
	if _, dup := factories.m[kind]; dup {
		panic("dbcore: factory already registered for " + string(kind))
	}
	factories.m[kind] = f
}

// HasFactory reports whether an engine package for kind has been imported.
func HasFactory(kind DatabaseKind) bool {
	factories.RLock()
	defer factories.RUnlock()
	_, ok := factories.m[kind]
	return ok
}

// RegisteredKinds lists engines whose package has been imported, sorted.
func RegisteredKinds() []DatabaseKind {
	factories.RLock()
	defer factories.RUnlock()
	out := make([]DatabaseKind, 0, len(factories.m))
	for k := range factories.m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Open connects using the Factory registered for spec.Kind.
//
// Callers opt into engines by importing their package, e.g. `_ "github.com/dbcore/dbcore/postgres"`,
// which keeps binaries free of unused drivers.
func Open(ctx context.Context, spec ConnSpec, pool PoolConfig) (Driver, error) {
	if strings.TrimSpace(string(spec.Kind)) == "" {
		return nil, &Error{ErrCodeNotSupported, "open: empty DatabaseKind"}
	}
	factories.RLock()
	f, ok := factories.m[spec.Kind]
	factories.RUnlock()
	if !ok {
		return nil, &Error{ErrCodeNotSupported, fmt.Sprintf(
			"open: no factory for %q (import the engine package, e.g. dbcore/%s)", spec.Kind, enginePackage(spec.Kind))}
	}
	d, err := f(ctx, spec, pool)
	if err != nil {
		return nil, err
	}
	return d, nil
}

// enginePackage maps a kind to its subpackage name for error messages.
func enginePackage(kind DatabaseKind) string {
	switch kind {
	case KindSQLite, KindPostgres, KindMySQL, KindMSSQL, KindOracle, KindRedis:
		return string(kind)
	}
	return "<engine>"
}
