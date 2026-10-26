package mysql

// TLS wiring for the two shapes go-sql-driver's named modes cannot express:
// a client certificate, and "verify the chain but not the hostname" (verify-ca,
// which the driver's "true" mode silently upgrades to verify-full).

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/dbcore/dbcore"
	drvmysql "github.com/go-sql-driver/mysql"
)

// tlsSeq names the tls.Config registered per connection, so two connections
// with different certificates never share one.
var tlsSeq atomic.Int64

// needsCustomTLS reports whether the spec asks for something the driver's named
// modes cannot express.
func needsCustomTLS(spec dbcore.ConnSpec) bool {
	if spec.ClientCert != "" || spec.ClientKey != "" || spec.RootCert != "" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(spec.SSLMode), "verify-ca")
}

// registerTLS builds the tls.Config, registers it with the driver, and returns
// the name to put in the DSN. "" means the named modes already cover the spec.
func registerTLS(spec dbcore.ConnSpec) (string, error) {
	if !needsCustomTLS(spec) {
		return "", nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if spec.RootCert != "" {
		pem, err := os.ReadFile(spec.RootCert)
		if err != nil {
			return "", fmt.Errorf("mysql: read root certificate: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return "", fmt.Errorf("mysql: %q contains no certificates", spec.RootCert)
		}
		cfg.RootCAs = pool
	}
	if spec.ClientCert != "" || spec.ClientKey != "" {
		if spec.ClientCert == "" || spec.ClientKey == "" {
			return "", fmt.Errorf("mysql: a client certificate needs both ClientCert and ClientKey")
		}
		cert, err := tls.LoadX509KeyPair(spec.ClientCert, spec.ClientKey)
		if err != nil {
			return "", fmt.Errorf("mysql: load client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	if strings.EqualFold(strings.TrimSpace(spec.SSLMode), "verify-ca") {
		// verify-ca means "the chain is trusted", not "the hostname matches".
		// The driver's "true" mode would also check the hostname, silently
		// upgrading the user's choice, so the chain is verified here by hand.
		cfg.InsecureSkipVerify = true
		cfg.VerifyPeerCertificate = verifyChainOnly(cfg.RootCAs)
	}
	name := fmt.Sprintf("dbcore-tls-%d", tlsSeq.Add(1))
	if err := drvmysql.RegisterTLSConfig(name, cfg); err != nil {
		return "", err
	}
	return name, nil
}

// verifyChainOnly verifies the presented chain against roots, without a
// hostname check. roots may be nil, in which case the system pool is used.
func verifyChainOnly(roots *x509.CertPool) func([][]byte, [][]*x509.Certificate) error {
	return func(raw [][]byte, _ [][]*x509.Certificate) error {
		if len(raw) == 0 {
			return fmt.Errorf("mysql: server presented no certificate")
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
		_, err = leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates})
		return err
	}
}
