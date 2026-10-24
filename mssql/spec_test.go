package mssql

import (
	"net/url"
	"testing"

	"github.com/dbcore/dbcore"
)

func TestDSNShapeAndTLS(t *testing.T) {
	spec := dbcore.ConnSpec{
		Kind:     dbcore.KindMSSQL,
		Host:     "sql.internal",
		Port:     1433,
		User:     "sa",
		Password: "p@ss/w:rd",
		Database: "UFTData",
		SSLMode:  "require",
	}
	u, err := url.Parse(DSN(spec))
	if err != nil {
		t.Fatalf("url.Parse: %v (%s)", err, DSN(spec))
	}
	if u.Scheme != "sqlserver" || u.Host != "sql.internal:1433" {
		t.Fatalf("unexpected DSN: %s", DSN(spec))
	}
	pw, _ := u.User.Password()
	if u.User.Username() != "sa" || pw != "p@ss/w:rd" {
		t.Fatalf("credentials escaped incorrectly: %s", DSN(spec))
	}
	q := u.Query()
	if q.Get("database") != "UFTData" {
		t.Errorf("database = %q", q.Get("database"))
	}
	if q.Get("encrypt") != "true" || q.Get("TrustServerCertificate") != "true" {
		t.Errorf("require should encrypt but tolerate a self-signed cert: %v", q)
	}
}

func TestDSNVerifyFullTrustsNoSelfSignedCert(t *testing.T) {
	u, err := url.Parse(DSN(dbcore.ConnSpec{Kind: dbcore.KindMSSQL, Host: "h", SSLMode: "verify-full"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("TrustServerCertificate"); got != "false" {
		t.Fatalf("TrustServerCertificate = %q, want false", got)
	}
}

func TestDSNDisableTurnsEncryptionOff(t *testing.T) {
	u, err := url.Parse(DSN(dbcore.ConnSpec{Kind: dbcore.KindMSSQL, Host: "h", SSLMode: "disable"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("encrypt"); got != "false" {
		t.Fatalf("encrypt = %q, want false", got)
	}
	// The login packet is still encrypted, so a self-signed certificate must not
	// abort the handshake even when bulk encryption is disabled.
	if got := u.Query().Get("TrustServerCertificate"); got != "true" {
		t.Fatalf("TrustServerCertificate = %q, want true", got)
	}
}
