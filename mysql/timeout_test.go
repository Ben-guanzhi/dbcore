package mysql

import (
	"strings"
	"testing"
	"time"

	"github.com/dbcore/dbcore"
)

// Read/write timeouts land in the DSN the same place the connect timeout
// does, so a hung server releases the connection instead of pinning it.
func TestDSNCarriesTimeouts(t *testing.T) {
	spec := dbcore.ConnSpec{
		Kind:           dbcore.KindMySQL,
		Host:           "db.example.com",
		Port:           3306,
		User:           "app",
		Password:       "p#ss",
		Database:       "shop",
		ConnectTimeout: 3 * time.Second,
		ReadTimeout:    30 * time.Second,
		WriteTimeout:   10 * time.Second,
	}
	dsn := DSN(spec)
	for _, want := range []string{"timeout=3s", "readTimeout=30s", "writeTimeout=10s"} {
		if !strings.Contains(dsn, want) {
			t.Fatalf("dsn %q misses %s", dsn, want)
		}
	}
	// Zero keeps the engine default (no knob in the DSN at all).
	zero := DSN(dbcore.ConnSpec{Kind: dbcore.KindMySQL, Host: "h", Port: 3306, User: "u", Password: "p", Database: "d"})
	if strings.Contains(zero, "readTimeout") || strings.Contains(zero, "writeTimeout") {
		t.Fatalf("zero timeouts must not appear: %q", zero)
	}
}
