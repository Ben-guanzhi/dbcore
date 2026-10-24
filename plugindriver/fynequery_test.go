package plugindriver_test

// A fynequery plugin, driven by the kernel host.
//
// This is the test that says "one protocol" rather than "two protocols that look
// alike": the plugin is built by fynequery's own pluginkit, and the kernel's
// host (plugindriver.Start) launches it, handshakes with it and drives it.

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dbcore/dbcore/plugindriver"
)

// fynequerySQLitePlugin returns the SQLite plugin binary fynequery builds.
//
// The plugin is not rebuilt here on purpose: fynequery's pluginkit is an internal
// package, which Go forbids importing from another module. Using the binary
// fynequery already produces is also closer to reality — it is the artifact a
// user actually ships.
func fynequerySQLitePlugin(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "fynequery"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	name := "fq-plugin-sqlite"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(root, "dist", "plugins", name)
	if _, err := os.Stat(binary); err != nil {
		t.Skipf("fynequery's plugins are not built: %v", err)
	}
	return binary
}

// The kernel host must accept a fynequery plugin's banner and drive it.
//
// The plugin's methods are the fynequery ones (tables, columns, query), so the
// kernel host reaches them through the connection-oriented method set the kernel
// defines for them.
func TestKernelHostDrivesAFynequeryPlugin(t *testing.T) {
	binary := fynequerySQLitePlugin(t)

	drv, err := plugindriver.Start(context.Background(), binary, nil)
	if err != nil {
		t.Fatalf("the kernel host refused a fynequery plugin: %v", err)
	}
	defer drv.Close()

	// The banner told the host which engine it is.
	if drv.Kind() == "" {
		t.Fatal("the plugin did not name its engine")
	}

	// Ping is the kernel's own method; a fynequery plugin does not have it and
	// answers "unknown method". The kernel maps that to ErrNotSupported, so the
	// host learns the plugin lacks the capability instead of failing the launch.
	err = drv.Ping(context.Background())
	if err != nil && !strings.Contains(err.Error(), "unknown method") {
		t.Fatalf("ping: %v", err)
	}
}

// The connection-oriented host must actually work against a fynequery plugin:
// opening a connection, listing the objects, and running a statement.
func TestConnHostDrivesAFynequeryPlugin(t *testing.T) {
	binary := fynequerySQLitePlugin(t)

	host, err := plugindriver.StartConn(context.Background(), binary, nil)
	if err != nil {
		t.Fatalf("the connection host refused a fynequery plugin: %v", err)
	}
	defer host.Close()

	// A scratch database, so the plugin has something to open.
	dbPath := filepath.Join(t.TempDir(), "cross.db")

	connID, _, err := host.Open(context.Background(), plugindriver.ConnectParams{
		Name:     "cross",
		Database: dbPath,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	conn := host.Bind(connID)

	// The plugin's own methods, reached through the kernel's connection-oriented
	// method set.
	tables, err := conn.ListTables(context.Background(), "")
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	// A fresh database has no tables, but the call must succeed.
	if len(tables) != 0 {
		t.Fatalf("tables = %+v", tables)
	}

	// A statement through the plugin.
	result, err := conn.Execute(context.Background(), "SELECT 1")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result == nil {
		t.Fatal("no result")
	}
}
