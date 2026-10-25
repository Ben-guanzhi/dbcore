package ddl

import (
	"strings"

	"github.com/dbcore/dbcore/sqltext"
)

// This file holds the statement verbs a schema synchronization needs beyond
// CREATE TABLE: dropping a table, adding/dropping/changing a column, and
// creating/dropping indexes, primary keys and foreign keys. They live next to
// CreateTable for the same reason it does — the quoting rules, the per-engine
// ALTER spellings and the "which engines can do this at all" answers are one
// piece of knowledge, and every client that reconstructs DDL reads it from
// here instead of re-deriving it.
//
// The verbs return text only. Whether a statement is safe to run, whether the
// engine runs DDL inside a transaction, and what to do when it cannot express
// a change at all are the caller's decisions; every "cannot express" case is
// reported by a Can* predicate rather than by emitting a plausible-looking
// statement.

// DropTable renders DROP TABLE. No IF EXISTS: Oracle has no such form, and a
// comparison already knows the table is there.
func DropTable(dbType, schema, table string) string {
	return "DROP TABLE " + tableName(dbType, schema, table) + ";"
}

// AddColumn renders ALTER TABLE ... ADD. The COLUMN keyword is not universal
// (SQL Server and Oracle reject it), so it is emitted only where it parses.
func AddColumn(dbType, schema, table string, c Column) string {
	keyword := "COLUMN "
	switch sqltext.Normalize(dbType) {
	case sqltext.SQLServer, sqltext.Oracle:
		keyword = ""
	}
	return "ALTER TABLE " + tableName(dbType, schema, table) + " ADD " + keyword + columnDef(dbType, c) + ";"
}

// DropColumn renders ALTER TABLE ... DROP COLUMN; every supported engine
// accepts this spelling (SQLite from 3.35 onwards).
func DropColumn(dbType, schema, table, column string) string {
	return "ALTER TABLE " + tableName(dbType, schema, table) + " DROP COLUMN " + ident(dbType, column) + ";"
}

// CanAlterColumn reports whether the engine can change a column's type,
// nullability and default in place. SQLite cannot: it only supports renaming and
// appending a column, so every other column change is a table rebuild, which is
// a different (and destructive) operation that this package will not pretend to
// emit.
func CanAlterColumn(dbType string) bool {
	return sqltext.Normalize(dbType) != sqltext.SQLite
}

// CanAlterColumnDefault reports whether the engine can change an existing
// column's default in place. SQL Server stores a default as a named constraint,
// so replacing one means knowing (or dropping) that constraint's name — out of
// reach of a text-only renderer, and reporting it beats inventing a name.
func CanAlterColumnDefault(dbType string) bool {
	switch sqltext.Normalize(dbType) {
	case sqltext.SQLite, sqltext.SQLServer:
		return false
	}
	return true
}

// AlterColumn renders the statements that turn column "from" into column "to".
// It returns one statement per aspect because the engines disagree on how many
// a single ALTER TABLE may carry: SQL Server and Oracle fold type and
// nullability into one clause, MySQL folds type, nullability and default into
// one MODIFY, and PostgreSQL needs a separate ALTER COLUMN action per aspect.
//
// It returns no statements when CanAlterColumn is false or when the two columns
// are already equivalent — the caller decides what to say about that.
func AlterColumn(dbType, schema, table string, from, to Column) []string {
	if !CanAlterColumn(dbType) {
		return nil
	}
	object := tableName(dbType, schema, table)
	column := ident(dbType, to.Name)

	typeChanged := normSpace(from.Type) != normSpace(to.Type)
	nullChanged := from.Nullable != to.Nullable
	defaultChanged := CanAlterColumnDefault(dbType) && defaultOf(from) != defaultOf(to)

	if !typeChanged && !nullChanged && !defaultChanged {
		return nil
	}

	switch sqltext.Normalize(dbType) {
	case sqltext.Postgres:
		var out []string
		if typeChanged {
			out = append(out, "ALTER TABLE "+object+" ALTER COLUMN "+column+" TYPE "+strings.TrimSpace(to.Type)+";")
		}
		if defaultChanged {
			if value := strings.TrimSpace(to.DefaultValue); value == "" {
				out = append(out, "ALTER TABLE "+object+" ALTER COLUMN "+column+" DROP DEFAULT;")
			} else {
				out = append(out, "ALTER TABLE "+object+" ALTER COLUMN "+column+" SET DEFAULT "+NormalizeDefault(dbType, value)+";")
			}
		}
		if nullChanged {
			if to.Nullable {
				out = append(out, "ALTER TABLE "+object+" ALTER COLUMN "+column+" DROP NOT NULL;")
			} else {
				out = append(out, "ALTER TABLE "+object+" ALTER COLUMN "+column+" SET NOT NULL;")
			}
		}
		return out

	case sqltext.MySQL:
		if !typeChanged && !nullChanged && !defaultChanged {
			return nil
		}
		// MODIFY restates the whole column, so an unchanged aspect still has to
		// be written out — leaving it off would silently reset it.
		return []string{"ALTER TABLE " + object + " MODIFY COLUMN " + columnDef(dbType, to) + ";"}

	default: // SQL Server and Oracle
		var out []string
		if typeChanged || nullChanged {
			clause := "ALTER TABLE " + object + " ALTER COLUMN " + column
			if sqltext.Normalize(dbType) == sqltext.Oracle {
				clause = "ALTER TABLE " + object + " MODIFY (" + column
			}
			clause += " " + strings.TrimSpace(to.Type)
			if to.Nullable {
				clause += " NULL"
			} else {
				clause += " NOT NULL"
			}
			if sqltext.Normalize(dbType) == sqltext.Oracle {
				clause += ")"
			}
			out = append(out, clause+";")
		}
		return out
	}
}

// CreateIndex renders CREATE [UNIQUE] INDEX for a secondary index. Primary keys
// are not indexes here — they are table constraints, see AddPrimaryKey.
func CreateIndex(dbType, schema, table string, idx Index) string {
	unique := ""
	if idx.Unique {
		unique = "UNIQUE "
	}
	return "CREATE " + unique + "INDEX " + ident(dbType, idx.Name) + " ON " +
		tableName(dbType, schema, table) + " (" + columnsList(dbType, idx.Columns) + ");"
}

// DropIndex renders DROP INDEX. MySQL names the index relative to its table;
// the others drop it by (optionally schema-qualified) name.
func DropIndex(dbType, schema, table, index string) string {
	if sqltext.Normalize(dbType) == sqltext.MySQL {
		return "DROP INDEX " + ident(dbType, index) + " ON " + tableName(dbType, schema, table) + ";"
	}
	if schema == "" || schema == "main" {
		return "DROP INDEX " + ident(dbType, index) + ";"
	}
	return "DROP INDEX " + ident(dbType, schema) + "." + ident(dbType, index) + ";"
}

// AddPrimaryKey renders the constraint form of a primary key, which every
// supported engine accepts on an existing table.
func AddPrimaryKey(dbType, schema, table string, pk Index) string {
	return "ALTER TABLE " + tableName(dbType, schema, table) + " ADD CONSTRAINT " +
		ident(dbType, pk.Name) + " PRIMARY KEY (" + columnsList(dbType, pk.Columns) + ");"
}

// DropPrimaryKey renders the removal of a primary key. MySQL has a dedicated
// form because its primary key is not a named constraint; the others drop the
// constraint by name. SQLite cannot drop a primary key at all, so this returns
// an empty string for it — CanDropPrimaryKey is the caller's check.
func DropPrimaryKey(dbType, schema, table, name string) string {
	if !CanDropPrimaryKey(dbType) {
		return ""
	}
	if sqltext.Normalize(dbType) == sqltext.MySQL {
		return "ALTER TABLE " + tableName(dbType, schema, table) + " DROP PRIMARY KEY;"
	}
	return "ALTER TABLE " + tableName(dbType, schema, table) + " DROP CONSTRAINT " + ident(dbType, name) + ";"
}

// CanDropPrimaryKey reports whether the engine can remove a primary key from an
// existing table. SQLite cannot without rebuilding the table.
func CanDropPrimaryKey(dbType string) bool {
	return sqltext.Normalize(dbType) != sqltext.SQLite
}

// AddForeignKey renders the ALTER TABLE form of a foreign key constraint, the
// same clause CreateTable emits for a table it is building from scratch.
func AddForeignKey(dbType, schema, table string, fk ForeignKey) string {
	return "ALTER TABLE " + tableName(dbType, schema, table) + " " + foreignKeyClause(dbType, fk) + ";"
}

// DropForeignKey renders the removal of a foreign key constraint. MySQL names it
// a FOREIGN KEY, the others a CONSTRAINT; SQLite cannot drop one at all, so
// CanDropForeignKey is the caller's check.
func DropForeignKey(dbType, schema, table, name string) string {
	if !CanDropForeignKey(dbType) {
		return ""
	}
	if sqltext.Normalize(dbType) == sqltext.MySQL {
		return "ALTER TABLE " + tableName(dbType, schema, table) + " DROP FOREIGN KEY " + ident(dbType, name) + ";"
	}
	return "ALTER TABLE " + tableName(dbType, schema, table) + " DROP CONSTRAINT " + ident(dbType, name) + ";"
}

// CanDropForeignKey reports whether the engine can remove a foreign key from an
// existing table. SQLite cannot without rebuilding the table.
func CanDropForeignKey(dbType string) bool {
	return sqltext.Normalize(dbType) != sqltext.SQLite
}

// columnDef renders one column's definition: name, type, default and
// nullability. Shared by CreateTable and AddColumn so an appended column reads
// exactly like the column the full script would have produced.
func columnDef(dbType string, c Column) string {
	dataType := strings.TrimSpace(c.Type)
	line := ident(dbType, c.Name) + " " + dataType
	if value := strings.TrimSpace(c.DefaultValue); value != "" {
		line += " DEFAULT " + NormalizeDefault(dbType, value)
	}
	if !c.Nullable {
		line += " NOT NULL"
	}
	return line
}

// foreignKeyClause renders the FOREIGN KEY clause without its leading ALTER
// TABLE, so CreateTable and AddForeignKey share one spelling.
func foreignKeyClause(dbType string, fk ForeignKey) string {
	referenced := tableName(dbType, fk.RefSchema, fk.RefTable)
	line := "ADD CONSTRAINT " + ident(dbType, fk.Name) + " FOREIGN KEY (" +
		columnsList(dbType, fk.Columns) + ") REFERENCES " + referenced +
		" (" + columnsList(dbType, fk.RefColumns) + ")"
	if fk.OnDelete != "" && fk.OnDelete != "NO ACTION" {
		line += " ON DELETE " + fk.OnDelete
	}
	if fk.OnUpdate != "" && fk.OnUpdate != "NO ACTION" {
		line += " ON UPDATE " + fk.OnUpdate
	}
	return line
}

// defaultOf reduces a column's default to the comparable form: some engines
// report the absence of a default as the text "NULL", which must not read as a
// real default.
func defaultOf(c Column) string {
	if value := strings.TrimSpace(c.DefaultValue); value != "" {
		return value
	}
	return ""
}

func normSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
