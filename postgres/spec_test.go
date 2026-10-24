package postgres

import (
	"net/url"
	"testing"

	"github.com/dbcore/dbcore"
)

func TestDSNShapeAndEscaping(t *testing.T) {
	spec := dbcore.ConnSpec{
		Kind:     dbcore.KindPostgres,
		Host:     "db.internal",
		Port:     5433,
		User:     "app",
		Password: "p@ss:w/rd",
		Database: "orders",
		Schema:   "tenant_1",
		SSLMode:  "verify-full",
		Params:   map[string]string{"application_name": "dbcore-test"},
	}
	raw := DSN(spec)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse: %v (%s)", err, raw)
	}
	if u.Scheme != "postgres" || u.Host != "db.internal:5433" || u.Path != "/orders" {
		t.Fatalf("unexpected DSN: %s", raw)
	}
	pw, _ := u.User.Password()
	if u.User.Username() != "app" || pw != "p@ss:w/rd" {
		t.Fatalf("credentials escaped incorrectly: %s", raw)
	}
	q := u.Query()
	if q.Get("sslmode") != "verify-full" {
		t.Errorf("sslmode = %q", q.Get("sslmode"))
	}
	if q.Get("search_path") != "tenant_1" {
		t.Errorf("search_path = %q", q.Get("search_path"))
	}
	if q.Get("application_name") != "dbcore-test" {
		t.Errorf("application_name = %q", q.Get("application_name"))
	}
}

func TestDSNDefaultPortAndNoSSL(t *testing.T) {
	raw := DSN(dbcore.ConnSpec{Kind: dbcore.KindPostgres, Host: "db", Database: "d"})
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "db:5432" {
		t.Fatalf("host = %q, want db:5432", u.Host)
	}
	if u.Query().Get("sslmode") != "" {
		t.Fatalf("sslmode should be omitted when SSLMode is empty: %s", raw)
	}
}

func TestSSLModeMapping(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"disable":     "disable",
		"require":     "require",
		"skip-verify": "require",
		"verify-ca":   "verify-ca",
		"verify-full": "verify-full",
		"preferred":   "prefer",
	}
	for mode, want := range cases {
		if got := sslMode(mode); got != want {
			t.Errorf("sslMode(%q) = %q, want %q", mode, got, want)
		}
	}
}
