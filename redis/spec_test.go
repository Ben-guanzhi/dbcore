package redis

import (
	"testing"
	"time"

	"github.com/dbcore/dbcore"
)

func TestOptionsFromSpec(t *testing.T) {
	spec := dbcore.ConnSpec{
		Kind:     dbcore.KindRedis,
		Host:     "cache.internal",
		Port:     6380,
		User:     "default",
		Password: "p@ss",
		Params:   map[string]string{"db": "3"},
		SSLMode:  "require",
	}
	opts, err := Options(spec)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Addr != "cache.internal:6380" || opts.Username != "default" || opts.Password != "p@ss" {
		t.Fatalf("options = %+v", opts)
	}
	if opts.DB != 3 {
		t.Fatalf("DB = %d, want 3", opts.DB)
	}
	if opts.TLSConfig == nil {
		t.Fatal("SSLMode=require should configure TLS")
	}
}

func TestOptionsRejectBadDB(t *testing.T) {
	_, err := Options(dbcore.ConnSpec{Kind: dbcore.KindRedis, Host: "h", Params: map[string]string{"db": "x"}})
	if err == nil {
		t.Fatal("non-numeric db must be rejected")
	}
}

func TestDSNSchemeFollowsTLS(t *testing.T) {
	if got := DSN(dbcore.ConnSpec{Kind: dbcore.KindRedis, Host: "h"}); got != "redis://h:6379" {
		t.Fatalf("DSN = %q", got)
	}
	if got := DSN(dbcore.ConnSpec{Kind: dbcore.KindRedis, Host: "h", SSLMode: "require"}); got[:8] != "rediss:/" {
		t.Fatalf("DSN = %q, want rediss://", got)
	}
}

// P3-6: the pool knobs used to be ignored entirely, so a caller asking for a
// small pool still got the client's default of 10·CPU connections.
func TestOptionsWithPoolMapsKnobs(t *testing.T) {
	spec := dbcore.ConnSpec{Kind: dbcore.KindRedis, Host: "h"}
	pool := dbcore.PoolConfig{
		MaxOpen:     3,
		MaxIdle:     1,
		MaxLifetime: 90 * time.Second,
		MaxIdleTime: 30 * time.Second,
	}
	opts, err := OptionsWithPool(spec, pool)
	if err != nil {
		t.Fatalf("OptionsWithPool: %v", err)
	}
	if opts.PoolSize != 3 || opts.MinIdleConns != 1 {
		t.Errorf("pool = %d/%d, want 3/1", opts.PoolSize, opts.MinIdleConns)
	}
	if opts.ConnMaxLifetime != 90*time.Second || opts.ConnMaxIdleTime != 30*time.Second {
		t.Errorf("lifetimes = %v/%v", opts.ConnMaxLifetime, opts.ConnMaxIdleTime)
	}
	// Zero knobs keep the client defaults, so an unconfigured caller is unchanged.
	plain, err := Options(spec)
	if err != nil {
		t.Fatal(err)
	}
	if plain.PoolSize != 0 || plain.MinIdleConns != 0 {
		t.Errorf("Options with no pool config set sizes: %d/%d", plain.PoolSize, plain.MinIdleConns)
	}
}
