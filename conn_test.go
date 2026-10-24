package dbcore_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/dbcore/dbcore"
)

// fakeDriver is a minimal Driver used to test the registry without importing a
// real engine package (the root package must stay driver-free).
type fakeDriver struct{ kind dbcore.DatabaseKind }

func (d *fakeDriver) Kind() dbcore.DatabaseKind       { return d.kind }
func (d *fakeDriver) Ping(ctx context.Context) error  { return nil }
func (d *fakeDriver) Close() error                    { return nil }
func (d *fakeDriver) AsSQL() (dbcore.SQLDriver, bool) { return nil, false }
func (d *fakeDriver) AsKV() (dbcore.KVDriver, bool)   { return nil, false }

const fakeKind dbcore.DatabaseKind = "fake-engine"

func TestOpenDispatchesToRegisteredFactory(t *testing.T) {
	var gotSpec dbcore.ConnSpec
	var gotPool dbcore.PoolConfig
	dbcore.RegisterFactory(fakeKind, func(ctx context.Context, spec dbcore.ConnSpec, pool dbcore.PoolConfig) (dbcore.Driver, error) {
		gotSpec, gotPool = spec, pool
		return &fakeDriver{kind: spec.Kind}, nil
	})

	spec := dbcore.ConnSpec{Kind: fakeKind, Host: "h", Port: 1234, Database: "d"}
	pool := dbcore.PoolConfig{MaxOpen: 7, MaxIdle: 3}
	d, err := dbcore.Open(context.Background(), spec, pool)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if d.Kind() != fakeKind {
		t.Fatalf("kind = %q", d.Kind())
	}
	if gotSpec.Database != "d" || gotPool.MaxOpen != 7 || gotPool.MaxIdle != 3 {
		t.Fatalf("factory received %+v / %+v", gotSpec, gotPool)
	}
}

func TestOpenReportsUnknownEngine(t *testing.T) {
	_, err := dbcore.Open(context.Background(), dbcore.ConnSpec{Kind: "no-such-engine"}, dbcore.PoolConfig{})
	if err == nil {
		t.Fatal("expected an error for an unregistered engine")
	}
	var de *dbcore.Error
	if !errors.As(err, &de) || de.Code != dbcore.ErrCodeNotSupported {
		t.Fatalf("error = %v, want a dbcore.Error with code %s", err, dbcore.ErrCodeNotSupported)
	}
	// The message must tell the caller how to fix it (import the engine package).
	if !strings.Contains(err.Error(), "import the engine package") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

func TestOpenRejectsEmptyKind(t *testing.T) {
	if _, err := dbcore.Open(context.Background(), dbcore.ConnSpec{}, dbcore.PoolConfig{}); err == nil {
		t.Fatal("empty kind must be rejected")
	}
}

func TestRegisterFactoryRejectsDuplicates(t *testing.T) {
	const k dbcore.DatabaseKind = "dup-engine"
	dbcore.RegisterFactory(k, func(context.Context, dbcore.ConnSpec, dbcore.PoolConfig) (dbcore.Driver, error) {
		return &fakeDriver{kind: k}, nil
	})
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate registration must panic")
		}
	}()
	dbcore.RegisterFactory(k, func(context.Context, dbcore.ConnSpec, dbcore.PoolConfig) (dbcore.Driver, error) {
		return &fakeDriver{kind: k}, nil
	})
}

func TestAddrUsesEngineDefaultPort(t *testing.T) {
	cases := []struct {
		kind dbcore.DatabaseKind
		port int
		want string
	}{
		{dbcore.KindMySQL, 0, "db.example:3306"},
		{dbcore.KindPostgres, 0, "db.example:5432"},
		{dbcore.KindMSSQL, 0, "db.example:1433"},
		{dbcore.KindOracle, 0, "db.example:1521"},
		{dbcore.KindMySQL, 13306, "db.example:13306"},
	}
	for _, c := range cases {
		got := dbcore.ConnSpec{Kind: c.kind, Host: "db.example", Port: c.port}.Addr()
		if got != c.want {
			t.Errorf("%s/%d: Addr() = %q, want %q", c.kind, c.port, got, c.want)
		}
	}
}

func TestPoolConfigApplyIsAdditive(t *testing.T) {
	// Apply must tolerate a zero PoolConfig (database/sql defaults stay in place).
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Skipf("sqlite driver unavailable: %v", err)
	}
	defer db.Close()
	dbcore.PoolConfig{}.Apply(db)
	dbcore.PoolConfig{MaxOpen: 2, MaxIdle: 1}.Apply(db)
	if got := db.Stats().MaxOpenConnections; got != 2 {
		t.Fatalf("MaxOpenConnections = %d, want 2", got)
	}
}
