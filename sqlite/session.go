package sqlite

// The wide session on the built-in SQLite driver: metrics from the page
// pragmas, and CreateDatabase overridden to create a database file (SQLite has
// no server-side CREATE DATABASE).

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/dbcore/dbcore"
)

var _ dbcore.WideSession = (*SQLiteDriver)(nil)

func (d *SQLiteDriver) initWide() {
	d.SessionCore = dbcore.NewSessionCore(d.db, d.dialect)
	d.SessionCore.SetMetricsQuery("SQLite", `
		SELECT 'Page count', CAST(page_count AS TEXT) FROM pragma_page_count()
		UNION ALL SELECT 'Page size', CAST(page_size AS TEXT) FROM pragma_page_size()
		UNION ALL SELECT 'Freelist pages', CAST(freelist_count AS TEXT) FROM pragma_freelist_count()`)
	d.SessionCore.SetCreateDatabase(createDatabaseFile)
}

// createDatabaseFile treats "name" as a file path; opening with mode=rwc
// creates the file when absent and is a no-op when it already exists.
func createDatabaseFile(ctx context.Context, name string, _ map[string]string) error {
	path := strings.TrimSpace(name)
	if path == "" {
		return fmt.Errorf("sqlite: no database file specified")
	}
	dsn := path
	if dsn != ":memory:" && !strings.HasPrefix(dsn, "file:") {
		dsn = "file:" + path + "?mode=rwc"
	}
	db, err := sql.Open(DriverName, dsn)
	if err != nil {
		return err
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return fmt.Errorf("sqlite create database %q: %w", path, err)
	}
	return db.Close()
}
