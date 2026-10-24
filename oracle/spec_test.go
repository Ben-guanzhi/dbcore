package oracle

import (
	"net/url"
	"testing"

	"github.com/dbcore/dbcore"
)

func TestDSNShape(t *testing.T) {
	spec := dbcore.ConnSpec{
		Kind:     dbcore.KindOracle,
		Host:     "ora.internal",
		Port:     1521,
		User:     "scott",
		Password: "tiger@1",
		Database: "ORCLPDB1",
	}
	u, err := url.Parse(DSN(spec))
	if err != nil {
		t.Fatalf("url.Parse: %v (%s)", err, DSN(spec))
	}
	if u.Scheme != "oracle" || u.Host != "ora.internal:1521" || u.Path != "/ORCLPDB1" {
		t.Fatalf("unexpected DSN: %s", DSN(spec))
	}
	pw, _ := u.User.Password()
	if u.User.Username() != "scott" || pw != "tiger@1" {
		t.Fatalf("credentials escaped incorrectly: %s", DSN(spec))
	}
}

func TestDSNFallsBackToSchemaAsService(t *testing.T) {
	u, err := url.Parse(DSN(dbcore.ConnSpec{Kind: dbcore.KindOracle, Host: "h", Schema: "XE"}))
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/XE" {
		t.Fatalf("path = %q, want /XE", u.Path)
	}
	if u.Host != "h:1521" {
		t.Fatalf("host = %q, want h:1521", u.Host)
	}
}

func TestDSNSSLParam(t *testing.T) {
	u, err := url.Parse(DSN(dbcore.ConnSpec{Kind: dbcore.KindOracle, Host: "h", SSLMode: "require"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("SSL"); got != "true" {
		t.Fatalf("SSL = %q, want true", got)
	}
}
