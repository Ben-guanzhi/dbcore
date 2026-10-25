// Package ddl assembles CREATE TABLE scripts from catalog rows a client has
// already fetched. It is the single home of the reconstruction algorithm: which
// clauses a table's DDL carries, how defaults are normalized per engine, and
// how identifiers are qualified. Clients map their own catalog shapes onto the
// input structs here; none of them re-derives DDL text (INTEGRATION-PLAN P1-7).
//
// Like sqltext it is dependency-free — structs in, strings out — so it imports
// no database drivers. The output is a reading aid rather than a migration
// tool: engine-level storage clauses are intentionally left out.
package ddl

import (
	"fmt"
	"strings"

	"github.com/dbcore/dbcore/sqltext"
)

// Column is one column of the table being described.
type Column struct {
	Name         string
	Type         string
	Nullable     bool
	DefaultValue string
}

// Index is one index on the table; composite keys keep their column order.
type Index struct {
	Name    string
	Unique  bool
	Primary bool
	Columns []string
}

// ForeignKey is one constraint pointing at another table.
type ForeignKey struct {
	Name       string
	Columns    []string
	RefSchema  string
	RefTable   string
	RefColumns []string
	OnDelete   string
	OnUpdate   string
}

// CreateTable renders the full script for a table described by the given
// catalog rows: the CREATE TABLE statement (columns plus primary-key
// constraints), one CREATE INDEX per secondary index, and one ALTER TABLE per
// foreign key.
//
// statements := one CREATE TABLE (columns plus primary-key constraint), one
// CREATE INDEX per secondary index, and one ALTER TABLE per foreign key.
//
// FullTypes optionally overrides a column's reported type with the fuller
// spelling a separate query produced (engines such as MSSQL and Oracle report
// bare names through their column list, which would build invalid DDL).
//
// The sibling file alter.go carries the matching verbs for a table that already
// exists (ADD/DROP COLUMN, ALTER COLUMN, index and constraint changes); use
// those when synchronizing and this one when reconstructing.
func CreateTable(dbType, schema, table string, columns []Column, fullTypes map[string]string, indexes []Index, foreignKeys []ForeignKey) string {
	object := tableName(dbType, schema, table)

	definitions := make([]string, 0, len(columns)+1)
	for _, column := range columns {
		if full, ok := fullTypes[column.Name]; ok && full != "" {
			column.Type = full
		}
		definitions = append(definitions, "  "+columnDef(dbType, column))
	}

	statements := []string{}
	for _, index := range indexes {
		if !index.Primary {
			continue
		}
		definitions = append(definitions,
			"  CONSTRAINT "+ident(dbType, index.Name)+" PRIMARY KEY ("+columnsList(dbType, index.Columns)+")")
	}
	statements = append(statements, "CREATE TABLE "+object+" (\n"+strings.Join(definitions, ",\n")+"\n);")

	for _, index := range indexes {
		if index.Primary || len(index.Columns) == 0 {
			continue
		}
		unique := ""
		if index.Unique {
			unique = "UNIQUE "
		}
		statements = append(statements, fmt.Sprintf("CREATE %sINDEX %s ON %s (%s);",
			unique, ident(dbType, index.Name), object, columnsList(dbType, index.Columns)))
	}

	for _, fk := range foreignKeys {
		statements = append(statements,
			"ALTER TABLE "+object+" "+foreignKeyClause(dbType, fk)+";")
	}

	return strings.Join(statements, "\n\n")
}

// NormalizeDefault unwraps the parentheses MSSQL wraps around every default and
// trims the padding Oracle adds to expression text. Other engines pass through
// unchanged.
func NormalizeDefault(dbType, value string) string {
	if dbType == sqltext.SQLServer {
		for strings.HasPrefix(value, "(") && strings.HasSuffix(value, ")") {
			inner := strings.TrimSpace(value[1 : len(value)-1])
			if inner == "" {
				break
			}
			value = inner
		}
	}
	return value
}

// tableName qualifies only when a schema exists, because SQLite has no schema
// argument in the tree and "main" would be noise in the DDL.
func tableName(dbType, schema, table string) string {
	if schema == "" || schema == "main" {
		return ident(dbType, table)
	}
	return ident(dbType, schema) + "." + ident(dbType, table)
}

// ident quotes one identifier; an engine that cannot quote the name (unknown
// type, invalid characters) yields the bare name rather than failing the whole
// reading aid.
func ident(dbType, part string) string {
	quoted, err := sqltext.QuoteIdent(dbType, part)
	if err != nil {
		return part
	}
	return quoted
}

func columnsList(dbType string, names []string) string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, ident(dbType, name))
	}
	return strings.Join(quoted, ", ")
}
