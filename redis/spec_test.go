package redis

import (
	"testing"

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
