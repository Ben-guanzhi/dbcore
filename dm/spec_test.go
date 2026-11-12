package dm

// These tests need no server: they pin the parts of the driver that are decided
// here rather than by 达梦 — the DSN the vendored driver will parse, the dialect
// DM inherits from Oracle, and the fact that the kernel's contracts are satisfied.
//
// The catalog reads themselves are NOT covered here: they ask DM's
// Oracle-compatible dictionary views, and only a live instance can say whether a
// given installation exposes them. dm_live_test.go runs that half when
// DBCORE_TEST_DM_DSN is set.

import (
	"context"
	"database/sql"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dbcore/dbcore"
)

func TestDSNMatchesTheVendoredDriverFormat(t *testing.T) {
	// The vendored driver's parseDSN requires scheme "dm", takes the credentials
	// from the URL's userinfo and everything else from the query.
	dsn := DSN(dbcore.ConnSpec{
		Host: "dm.local", Port: 5236, User: "SYSDBA", Password: "pw", Database: "APP",
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("the DSN must be a URL: %v (%s)", err, dsn)
	}
	if u.Scheme != "dm" {
		t.Fatalf("scheme = %q, want dm", u.Scheme)
	}
	if u.User.Username() != "SYSDBA" {
		t.Fatalf("user = %q", u.User.Username())
	}
	if pw, _ := u.User.Password(); pw != "pw" {
		t.Fatalf("password = %q", pw)
	}
	if u.Host != "dm.local:5236" {
		t.Fatalf("host = %q", u.Host)
	}
	// The database travels as the schema parameter: a DM session has no separate
	// database layer.
	if got := u.Query().Get("schema"); got != "APP" {
		t.Fatalf("schema = %q", got)
	}
}

func TestDSNEscapesCredentials(t *testing.T) {
	// A password with @ / : / ? must not break the URL.
	dsn := DSN(dbcore.ConnSpec{
		Host: "h", Port: 5236, User: "u", Password: "p@ss:w/rd?x",
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse: %v (%s)", err, dsn)
	}
	if pw, _ := u.User.Password(); pw != "p@ss:w/rd?x" {
		t.Fatalf("password round trip lost characters: %q", pw)
	}
	if u.Host != "h:5236" {
		t.Fatalf("host = %q", u.Host)
	}
}

func TestDSNCarriesParamsAndTimeout(t *testing.T) {
	dsn := DSN(dbcore.ConnSpec{
		Host: "h", Port: 5236, User: "u",
		ConnectTimeout: 7 * time.Second,
		Params:         map[string]string{"compatibleMode": "oracle"},
	})
	u, _ := url.Parse(dsn)
	q := u.Query()
	if q.Get("connectTimeout") == "" {
		t.Fatalf("connectTimeout missing: %s", dsn)
	}
	if got := q.Get("compatibleMode"); got != "oracle" {
		t.Fatalf("params not forwarded: %q", got)
	}
}

func TestDialectIsOracleCompatibleButDMKinded(t *testing.T) {
	var d DMDialect
	// DM8's compatibility mode takes Oracle's quoting and placeholders.
	if got := d.QuoteIdent("S", "T"); !strings.Contains(got, `"T"`) {
		t.Fatalf("quote = %q", got)
	}
	if got := d.Placeholder(2); got != ":2" {
		t.Fatalf("placeholder = %q", got)
	}
	// The kind must stay DM: an Oracle kind would make the Oracle-only panels
	// claim a DM connection.
	if got := d.Kind(); got != dbcore.KindDM {
		t.Fatalf("kind = %q, want %q", got, dbcore.KindDM)
	}
}

func TestDMRegistersItsFactory(t *testing.T) {
	// OpenSpec is reachable through the kernel registry, which is how a consumer
	// opens a connection from a spec without importing this package's details.
	if !dbcore.HasFactory(dbcore.KindDM) {
		t.Fatal("dm is not registered with dbcore")
	}
}

func TestOpenSpecRefusesACustomDialer(t *testing.T) {
	// Silently ignoring a dialer would connect to the wrong host (an SSH tunnel's
	// target instead of the tunnel itself), so it is refused before any socket.
	spec := dbcore.ConnSpec{
		Host:   "h",
		Port:   5236,
		Dialer: func(ctx context.Context, network, addr string) (net.Conn, error) { return nil, nil },
	}
	_, err := OpenSpec(context.Background(), spec, dbcore.PoolConfig{})
	if err == nil {
		t.Fatal("a custom dialer must be refused")
	}
	if !strings.Contains(err.Error(), "Dialer") {
		t.Fatalf("the error should name the dialer: %v", err)
	}
}

// The contracts the kernel expects, asserted at compile time in dm.go; this test
// documents them so a change to the interface set is noticed here too.
func TestDriverSatisfiesTheKernelContracts(t *testing.T) {
	var drv dbcore.Driver = (*DMDriver)(nil)
	if _, ok := drv.AsSQL(); !ok {
		t.Fatal("a DM connection is an SQL driver")
	}
	// An unused *sql.DB reference keeps the import honest for readers scanning the
	// file for "how does this connect".
	var _ *sql.DB
}
