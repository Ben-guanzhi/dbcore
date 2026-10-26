package postgres

import (
	"net/url"
	"testing"
	"time"

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

// P3-6: the statement cap travels as a runtime GUC in libpq's options, and the
// client-certificate paths under their libpq names.
func TestDSNCarriesStatementTimeoutAndCertPaths(t *testing.T) {
	spec := dbcore.ConnSpec{
		Kind:             dbcore.KindPostgres,
		Host:             "h",
		Database:         "app",
		SSLMode:          "verify-ca",
		StatementTimeout: 5 * time.Second,
		ClientCert:       "/certs/client.pem",
		ClientKey:        "/certs/client.key",
		RootCert:         "/certs/root.pem",
		Params:           map[string]string{"application_name": "t"},
	}
	u, err := url.Parse(DSN(spec))
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	q := u.Query()
	if got := q.Get("options"); got != "-c statement_timeout=5000" {
		t.Errorf("options = %q, want the 5000ms statement timeout", got)
	}
	if q.Get("sslcert") != "/certs/client.pem" || q.Get("sslkey") != "/certs/client.key" {
		t.Errorf("client certificate paths missing: %v", q)
	}
	if q.Get("sslrootcert") != "/certs/root.pem" {
		t.Errorf("root certificate path missing: %v", q)
	}
	if q.Get("sslmode") != "verify-ca" {
		t.Errorf("sslmode = %q, want verify-ca (not upgraded)", q.Get("sslmode"))
	}
	// A caller-supplied options value must be kept, not replaced.
	spec.Params["options"] = "-c search_path=public"
	u2, err := url.Parse(DSN(spec))
	if err != nil {
		t.Fatal(err)
	}
	if got := u2.Query().Get("options"); got != "-c search_path=public -c statement_timeout=5000" {
		t.Errorf("options = %q, want the caller's setting kept", got)
	}
}
