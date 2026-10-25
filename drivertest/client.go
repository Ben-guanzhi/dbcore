package drivertest

// Client-facing entry point for the shared engine contract.
//
// The per-engine tests inside dbcore call Harness directly. Clients (TableProGo,
// fynequery, db-dev, …) own their connection glue instead: pools, tunnels,
// DSN assembly, session wrappers. Spec lets such a client hand over *its* Open
// function and still run the identical assertions, so a regression in the glue
// (a dropped parameter, a lost pool setting, a dialect mismatch) fails the same
// contract the kernel drivers are held to.
//
// Typical use:
//
//	drivertest.Run(t, drivertest.Spec{
//	    Kind:   dbcore.KindMySQL,
//	    Schema: "app",
//	    Open: func(ctx context.Context) (dbcore.SQLTxDriver, error) {
//	        db, err := myClient.openConnection(cfg) // client glue
//	        if err != nil { return nil, err }
//	        return dbcoremysql.NewFromDB(db), nil
//	    },
//	})

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/dbcore/dbcore"
)

// Spec describes how to open the client's driver for the contract run.
type Spec struct {
	// Kind selects the per-engine default placeholder style and DDL.
	Kind dbcore.DatabaseKind
	// Schema is the catalog namespace ListTables/ListColumns take.
	Schema string
	// Open must return a transaction-capable driver. The suite closes it.
	Open func(ctx context.Context) (dbcore.SQLTxDriver, error)

	// Optional overrides; nil means "use the per-kind default".
	Placeholder func(index int) string
	CreateTable func(name string) string
	DropTable   func(name string) string
	TableName   func(name string) string
}

// Run executes the full contract against spec.
func Run(t *testing.T, spec Spec) {
	t.Helper()
	if spec.Open == nil {
		t.Fatalf("drivertest.Spec.Open must be set")
	}
	schema := spec.Schema
	if schema == "" {
		schema = DefaultSchema(spec.Kind)
	}
	placeholder := spec.Placeholder
	if placeholder == nil {
		placeholder = func(i int) string { return Placeholder(spec.Kind, i) }
	}
	create := spec.CreateTable
	if create == nil {
		create = func(name string) string { return CreateTableSQL(spec.Kind, name) }
	}
	drop := spec.DropTable
	if drop == nil {
		drop = func(name string) string { return DropTableSQL(spec.Kind, name) }
	}
	tableName := spec.TableName
	if tableName == nil {
		tableName = func(name string) string { return FoldName(spec.Kind, name) }
	}
	Harness{
		Make:        spec.Open,
		Schema:      schema,
		Placeholder: placeholder,
		CreateTable: create,
		DropTable:   drop,
		TableName:   tableName,
		ColumnFold:  func(s string) string { return FoldName(spec.Kind, s) },
		Kind:        spec.Kind,
	}.Run(t)
}

// DefaultSchema is the namespace the contract should inspect when the caller
// does not care (MySQL: connected database, which the caller must supply).
func DefaultSchema(kind dbcore.DatabaseKind) string {
	switch kind {
	case dbcore.KindPostgres:
		return "public"
	case dbcore.KindSQLite:
		return "main"
	case dbcore.KindMSSQL:
		return "dbo"
	}
	return ""
}

// Placeholder renders the idx-th bind marker in the engine's syntax.
//
// SQL Server goes through "?" because the dbcore driver rewrites it to @pN;
// callers that talk to the server without that rewrite should pass
// Spec.Placeholder explicitly.
func Placeholder(kind dbcore.DatabaseKind, idx int) string {
	switch kind {
	case dbcore.KindPostgres:
		return fmt.Sprintf("$%d", idx)
	case dbcore.KindOracle:
		return fmt.Sprintf(":%d", idx)
	case dbcore.KindMSSQL:
		return fmt.Sprintf("@p%d", idx)
	}
	return "?"
}

// CreateTableSQL is the portable two-column table the contract provisions.
func CreateTableSQL(kind dbcore.DatabaseKind, name string) string {
	switch kind {
	case dbcore.KindMSSQL:
		// TEXT is deprecated and not comparable in SQL Server.
		return fmt.Sprintf("CREATE TABLE [%s] (id INT, name NVARCHAR(255))", name)
	case dbcore.KindOracle:
		return fmt.Sprintf(`CREATE TABLE "%s" (id NUMBER, name VARCHAR2(255))`, name)
	}
	return fmt.Sprintf("CREATE TABLE %s(id INTEGER, name TEXT)", quoteBare(kind, name))
}

// DropTableSQL is the idempotent teardown statement.
func DropTableSQL(kind dbcore.DatabaseKind, name string) string {
	switch kind {
	case dbcore.KindOracle:
		// DROP TABLE IF EXISTS only exists from 23c onwards.
		return fmt.Sprintf(`BEGIN EXECUTE IMMEDIATE 'DROP TABLE "%s"'; EXCEPTION WHEN OTHERS THEN NULL; END;`, name)
	}
	return "DROP TABLE IF EXISTS " + quoteBare(kind, name)
}

// FoldName maps the generated name to the form the engine stores. Oracle folds
// unquoted identifiers to upper case, so every later reference must match.
func FoldName(kind dbcore.DatabaseKind, name string) string {
	if kind == dbcore.KindOracle {
		return strings.ToUpper(name)
	}
	return name
}

// quoteBare quotes a table name for engines whose CREATE/DROP statements above
// do not already do it.
func quoteBare(kind dbcore.DatabaseKind, name string) string {
	switch kind {
	case dbcore.KindMySQL:
		return "`" + name + "`"
	case dbcore.KindPostgres, dbcore.KindSQLite:
		return `"` + name + `"`
	}
	return name
}
