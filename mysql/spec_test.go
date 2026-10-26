package mysql

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	drvmysql "github.com/go-sql-driver/mysql"

	"github.com/dbcore/dbcore"
)

func TestDSNBuildsEscapedConfig(t *testing.T) {
	spec := dbcore.ConnSpec{
		Kind:     dbcore.KindMySQL,
		Host:     "127.0.0.1",
		Port:     3307,
		User:     "root",
		Password: "p@ss/w:rd?x",
		Database: "app",
		SSLMode:  "require",
		Params:   map[string]string{"sql_mode": "ANSI_QUOTES"},
	}
	cfg, err := drvmysql.ParseDSN(DSN(spec))
	if err != nil {
		t.Fatalf("ParseDSN: %v (%s)", err, DSN(spec))
	}
	if cfg.User != "root" || cfg.Passwd != "p@ss/w:rd?x" {
		t.Fatalf("credentials round-trip failed: %+v", cfg)
	}
	if cfg.Addr != "127.0.0.1:3307" || cfg.DBName != "app" {
		t.Fatalf("addr/db = %q/%q", cfg.Addr, cfg.DBName)
	}
	if !cfg.ParseTime {
		t.Error("ParseTime should be enabled so timestamps scan into time.Time")
	}
	if cfg.TLSConfig != "true" {
		t.Errorf("TLSConfig = %q, want true for SSLMode=require", cfg.TLSConfig)
	}
	if got := cfg.Params["sql_mode"]; got != "ANSI_QUOTES" {
		t.Errorf("sql_mode param = %q", got)
	}
}

func TestDSNDefaultPort(t *testing.T) {
	cfg, err := drvmysql.ParseDSN(DSN(dbcore.ConnSpec{Kind: dbcore.KindMySQL, Host: "db"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "db:3306" {
		t.Fatalf("Addr = %q, want db:3306", cfg.Addr)
	}
	if cfg.TLSConfig != "" {
		t.Fatalf("TLSConfig = %q, want empty when SSLMode is unset", cfg.TLSConfig)
	}
}

func TestTLSModeMapping(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"disable":     "",
		"preferred":   "preferred",
		"require":     "true",
		"verify-full": "true",
		"skip-verify": "skip-verify",
	}
	for mode, want := range cases {
		if got := tlsParam(mode); got != want {
			t.Errorf("tlsParam(%q) = %q, want %q", mode, got, want)
		}
	}
}

func TestDSNWithDialerRoutesThroughRegisteredNetwork(t *testing.T) {
	dsn := DSN(dbcore.ConnSpec{
		Kind: dbcore.KindMySQL, Host: "h", Port: 3306, User: "u",
		Dialer: func(ctx context.Context, network, addr string) (net.Conn, error) { return nil, nil },
	})
	cfg, err := drvmysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("ParseDSN: %v (%s)", err, dsn)
	}
	if !strings.HasPrefix(cfg.Net, "dbcore-dial-") {
		t.Fatalf("Net = %q, want a dbcore-dial-* network so the custom dialer is used", cfg.Net)
	}
	if cfg.Addr != "h:3306" {
		t.Fatalf("Addr = %q", cfg.Addr)
	}
}

// P3-6: max_execution_time is the only per-statement cap MySQL has, and it
// travels as a session variable through the DSN.
func TestDSNCarriesStatementTimeoutAndInterpolation(t *testing.T) {
	cfg, err := drvmysql.ParseDSN(DSN(dbcore.ConnSpec{
		Kind:              dbcore.KindMySQL,
		Host:              "h",
		Port:              3306,
		User:              "u",
		StatementTimeout:  30 * time.Second,
		InterpolateParams: true,
	}))
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	if got := cfg.Params["max_execution_time"]; got != "30000" {
		t.Errorf("max_execution_time = %q, want 30000", got)
	}
	if !cfg.InterpolateParams {
		t.Error("InterpolateParams was dropped")
	}
}

// verify-ca must not become verify-full: go-sql-driver's "true" mode checks the
// hostname, so the mode needs a registered config that verifies the chain only.
func TestVerifyCAUsesCustomTLSConfig(t *testing.T) {
	if !needsCustomTLS(dbcore.ConnSpec{Kind: dbcore.KindMySQL, SSLMode: "verify-ca"}) {
		t.Fatal("verify-ca should need a custom config")
	}
	name, err := registerTLS(dbcore.ConnSpec{Kind: dbcore.KindMySQL, SSLMode: "verify-ca"})
	if err != nil {
		t.Fatalf("registerTLS: %v", err)
	}
	if !strings.HasPrefix(name, "dbcore-tls-") {
		t.Fatalf("name = %q, want a registered dbcore-tls-* config", name)
	}
	// A plain require mode keeps the driver's own named mode.
	if needsCustomTLS(dbcore.ConnSpec{Kind: dbcore.KindMySQL, SSLMode: "require"}) {
		t.Error("require should not need a custom config")
	}
	// Half a client certificate is refused rather than silently ignored.
	if _, err := registerTLS(dbcore.ConnSpec{
		Kind: dbcore.KindMySQL, ClientCert: "/certs/client.pem",
	}); err == nil {
		t.Error("a client certificate without a key was accepted")
	}
}
