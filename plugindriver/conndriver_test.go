package plugindriver_test

// One plugin process serves several connections: the connection-oriented host
// opens more than one connection against a single child and addresses each by
// id. This is the property the TableProGo integration relies on — a second
// tab of the same engine does not spawn a second child — so it is pinned here
// against the real fynequery SQLite plugin.

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/plugindriver"
)

// TestOneProcessServesTwoConnections opens two distinct database files on one
// ConnDriver and proves both bound connections answer independently.
func TestOneProcessServesTwoConnections(t *testing.T) {
	binary := fynequerySQLitePlugin(t)

	host, err := plugindriver.StartConn(context.Background(), binary, nil)
	if err != nil {
		t.Fatalf("the connection host refused a fynequery plugin: %v", err)
	}
	defer host.Close()

	firstDB := filepath.Join(t.TempDir(), "first.db")
	secondDB := filepath.Join(t.TempDir(), "second.db")

	firstID, _, err := host.Open(context.Background(), plugindriver.ConnectParams{
		Name:     "first",
		Database: firstDB,
	})
	if err != nil {
		t.Fatalf("open first: %v", err)
	}
	secondID, _, err := host.Open(context.Background(), plugindriver.ConnectParams{
		Name:     "second",
		Database: secondDB,
	})
	if err != nil {
		t.Fatalf("open second: %v", err)
	}
	// Two opens against one host returned two connection ids.
	if firstID == secondID {
		t.Fatalf("the two connections share an id: %q", firstID)
	}

	first := host.Bind(firstID, "first")
	second := host.Bind(secondID, "second")

	// Both connections answer through the same process; the schema each reports
	// is the database it was opened on.
	if schemas, err := first.ListSchemas(context.Background()); err != nil {
		t.Fatalf("first schemas: %v", err)
	} else if len(schemas) != 1 || schemas[0].Name != "first" {
		t.Fatalf("first schemas = %+v", schemas)
	}
	if schemas, err := second.ListSchemas(context.Background()); err != nil {
		t.Fatalf("second schemas: %v", err)
	} else if len(schemas) != 1 || schemas[0].Name != "second" {
		t.Fatalf("second schemas = %+v", schemas)
	}

	// A statement runs on each connection without mixing them up.
	for name, conn := range map[string]dbcore.SQLDriver{"first": first, "second": second} {
		result, err := conn.Execute(context.Background(), "SELECT 1")
		if err != nil {
			t.Fatalf("%s execute: %v", name, err)
		}
		if result == nil {
			t.Fatalf("%s returned no result", name)
		}
	}

	// Dropping one connection leaves the other usable; the process is still up.
	if err := host.CloseConn(context.Background(), firstID); err != nil {
		t.Fatalf("close first: %v", err)
	}
	if _, err := second.Execute(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("second after first closed: %v", err)
	}
}
