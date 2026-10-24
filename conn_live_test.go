package dbcore_test

// Live smoke tests for the connection layer (ConnSpec/Open), as opposed to the
// per-engine contract tests which exercise drivers through their own Open(dsn).
//
// They only run when the matching environment variable is set, so a plain
// `go test ./...` stays offline:
//
//	DBCORE_LIVE_MYSQL=mysql://root:dbcore@127.0.0.1:53306/dbcore
//	DBCORE_LIVE_PG=postgres://postgres:dbcore@127.0.0.1:55432/dbcore
//	DBCORE_LIVE_MSSQL=sqlserver://sa:Dbcore%232026@127.0.0.1:51433?database=master
//	DBCORE_LIVE_ORACLE=oracle://system:dbcore@127.0.0.1:51521/FREEPDB1
//	DBCORE_LIVE_REDIS=redis://127.0.0.1:36379/0
//
// The URL is only a vehicle for host/port/credentials: the connection itself is
// built by the engine's DSN() from a ConnSpec, so this covers the new layer.

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/dbcore/dbcore"

	_ "github.com/dbcore/dbcore/mssql"
	_ "github.com/dbcore/dbcore/mysql"
	_ "github.com/dbcore/dbcore/oracle"
	_ "github.com/dbcore/dbcore/postgres"
	_ "github.com/dbcore/dbcore/redis"
	_ "github.com/dbcore/dbcore/sqlite"
)

// specFromURL turns a test URL into a ConnSpec (kind from the scheme).
func specFromURL(t *testing.T, raw string, kind dbcore.DatabaseKind) dbcore.ConnSpec {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	spec := dbcore.ConnSpec{Kind: kind, Host: u.Hostname()}
	if p := u.Port(); p != "" {
		spec.Port, err = strconv.Atoi(p)
		if err != nil {
			t.Fatalf("bad port in %q: %v", raw, err)
		}
	}
	if u.User != nil {
		spec.User = u.User.Username()
		spec.Password, _ = u.User.Password()
	}
	q := u.Query()
	switch kind {
	case dbcore.KindMSSQL:
		spec.Database = q.Get("database")
	case dbcore.KindSQLite:
		spec.File = u.Path
	case dbcore.KindRedis:
		spec.Database = ""
		if p := u.Path; len(p) > 1 {
			spec.Params = map[string]string{"db": p[1:]}
		}
	default:
		spec.Database = u.Path
		if len(spec.Database) > 0 && spec.Database[0] == '/' {
			spec.Database = spec.Database[1:]
		}
	}
	if kind == dbcore.KindPostgres {
		spec.SSLMode = q.Get("sslmode")
	}
	spec.ConnectTimeout = 10 * time.Second
	return spec
}

func liveURL(t *testing.T, env string) string {
	t.Helper()
	raw := os.Getenv(env)
	if raw == "" {
		t.Skipf("%s not set; skipping live connection-layer smoke test", env)
	}
	return raw
}

func TestLiveOpenSQL(t *testing.T) {
	cases := []struct {
		env  string
		kind dbcore.DatabaseKind
	}{
		{"DBCORE_LIVE_MYSQL", dbcore.KindMySQL},
		{"DBCORE_LIVE_PG", dbcore.KindPostgres},
		{"DBCORE_LIVE_MSSQL", dbcore.KindMSSQL},
		{"DBCORE_LIVE_ORACLE", dbcore.KindOracle},
	}
	ctx := context.Background()
	for _, c := range cases {
		c := c
		t.Run(string(c.kind), func(t *testing.T) {
			spec := specFromURL(t, liveURL(t, c.env), c.kind)
			t.Logf("ConnSpec: kind=%s addr=%s db=%q ssl=%q", spec.Kind, spec.Addr(), spec.Database, spec.SSLMode)

			d, err := dbcore.Open(ctx, spec, dbcore.PoolConfig{MaxOpen: 3, MaxIdle: 1, MaxLifetime: time.Minute})
			if err != nil {
				t.Fatalf("Open(%s): %v", c.kind, err)
			}
			defer d.Close()
			if err := d.Ping(ctx); err != nil {
				t.Fatalf("Ping: %v", err)
			}
			if d.Kind() != c.kind {
				t.Fatalf("Kind() = %q, want %q", d.Kind(), c.kind)
			}

			drv, ok := d.AsSQL()
			if !ok {
				t.Fatalf("%s: not a SQL driver", c.kind)
			}
			// Creating and dropping a table exercises quote/DDL text and proves
			// the connection is writable (catalog reads alone can hide auth bugs).
			name := fmt.Sprintf("dbcore_live_%d", time.Now().UnixNano()%1_000_000)
			create, drop := liveTableDDL(t, c.kind, name)
			if _, err := drv.Execute(ctx, create); err != nil {
				t.Fatalf("create %s: %v", name, err)
			}
			defer drv.Execute(ctx, drop)
			tables, err := drv.ListTables(ctx, liveSchema(c.kind, spec.Database))
			if err != nil {
				t.Fatalf("ListTables: %v", err)
			}
			if len(tables) == 0 {
				t.Fatalf("ListTables returned nothing; the live database should expose the new table")
			}
		})
	}
}

func TestLiveOpenRedis(t *testing.T) {
	spec := specFromURL(t, liveURL(t, "DBCORE_LIVE_REDIS"), dbcore.KindRedis)
	ctx := context.Background()
	d, err := dbcore.Open(ctx, spec, dbcore.PoolConfig{})
	if err != nil {
		t.Fatalf("Open(redis): %v", err)
	}
	defer d.Close()
	kv, ok := d.AsKV()
	if !ok {
		t.Fatal("redis driver is not a KVDriver")
	}
	page, err := kv.ScanKeys(ctx, 0, "*", 10)
	if err != nil {
		t.Fatalf("ScanKeys: %v", err)
	}
	t.Logf("redis keys sampled: %d (cursor %d)", len(page.Keys), page.Cursor)
}

// liveSchema is the namespace ListTables expects per engine.
func liveSchema(kind dbcore.DatabaseKind, database string) string {
	switch kind {
	case dbcore.KindMySQL:
		return database // MySQL has no separate schema layer
	case dbcore.KindMSSQL:
		return "dbo"
	case dbcore.KindOracle:
		return "" // ALL_OBJECTS-based listing is owner-agnostic
	default:
		return "public"
	}
}

// liveTableDDL returns portable create/drop statements for the engine.
func liveTableDDL(t *testing.T, kind dbcore.DatabaseKind, name string) (string, string) {
	t.Helper()
	switch kind {
	case dbcore.KindMySQL:
		return "CREATE TABLE `" + name + "` (id INT PRIMARY KEY, name VARCHAR(32))",
			"DROP TABLE `" + name + "`"
	case dbcore.KindPostgres:
		return `CREATE TABLE "` + name + `" (id INT PRIMARY KEY, name TEXT)`,
			`DROP TABLE "` + name + `"`
	case dbcore.KindMSSQL:
		return "CREATE TABLE [dbo].[" + name + "] (id INT PRIMARY KEY, name NVARCHAR(32))",
			"DROP TABLE [dbo].[" + name + "]"
	case dbcore.KindOracle:
		return `CREATE TABLE "` + name + `" (id NUMBER PRIMARY KEY, name VARCHAR2(32))`,
			`DROP TABLE "` + name + `"`
	}
	t.Fatalf("no live DDL for %s", kind)
	return "", ""
}
