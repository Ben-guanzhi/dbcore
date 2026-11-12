package dm

// W14: DM8 against the SHARED contract suite.
//
// dm_live_test.go covers the surfaces this client actually uses; drivertest covers
// the contract every engine is held to (paging, filters, keyset, batches, writes,
// type round-trips). Running it matters because the two suites ask different
// questions: mine proved "the client's paths work", this one proves "DM behaves
// like the other engines where the kernel assumes they all do".
//
// Skipped unless DBCORE_TEST_DM_DSN is set:
//
//	export DBCORE_TEST_DM_DSN='dm://SYSDBA:SYSDBA001@localhost:5236?schema=SYSDBA'
//	go test ./dm/ -run TestDMContract -v

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/drivertest"
)

func TestDMContract(t *testing.T) {
	dsn := os.Getenv("DBCORE_TEST_DM_DSN")
	if dsn == "" {
		t.Skip("set DBCORE_TEST_DM_DSN to run the DM contract suite")
	}
	h := drivertest.Harness{
		Make: func(ctx context.Context) (dbcore.SQLTxDriver, error) {
			return Open(dsn)
		},
		Schema:      os.Getenv("DBCORE_TEST_DM_SCHEMA"),
		Placeholder: func(idx int) string { return fmt.Sprintf(":%d", idx) },
		Kind:        dbcore.KindDM,
		// DM has no TEXT type; VARCHAR is its portable string column (Oracle's
		// VARCHAR2 is also accepted, but VARCHAR is the one DM documents).
		CreateTable: func(name string) string {
			return fmt.Sprintf(`CREATE TABLE "%s" (id NUMBER, name VARCHAR(255))`, name)
		},
		TimestampDDL: func(table string) []string {
			return []string{fmt.Sprintf(`CREATE TABLE "%s" (id NUMBER, at TIMESTAMP)`, table)}
		},
		// Verified against a live DM8: DROP TABLE IF EXISTS is supported, and the
		// harness needs an idempotent drop (it treats a cleanup error as fatal).
		DropTable: func(name string) string {
			return fmt.Sprintf(`DROP TABLE IF EXISTS "%s"`, name)
		},
		// Unquoted DM identifiers fold to upper case (Oracle-compatible), so every
		// reference has to use the stored name.
		TableName:  strings.ToUpper,
		ColumnFold: strings.ToUpper,
	}
	h.Run(t)
}
