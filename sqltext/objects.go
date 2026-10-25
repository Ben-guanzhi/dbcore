package sqltext

// Catalog recipes for the facts beyond tables/columns/FKs: view definitions,
// CHECK constraints, sequences and routines (functions/procedures). Same
// discipline as triggers.go — the SQL lives here so consumers never hand-write
// dialect branches.
//
// Two engine families appear here:
//   - SQLite/MySQL/PostgreSQL take positional binds, each in its own spelling
//     (?/$n), exactly like the driver-level CatalogQuery methods.
//   - SQL Server and Oracle inline quoted literals on this path (their catalog
//     queries address objects by OBJECT_ID/name text).
//
// View definitions are returned as the engine's body text where the catalog
// only stores the SELECT; ComposeViewDDL turns that into re-runnable DDL.

import (
	"fmt"
	"strings"
)

// ViewDefinitionQuery returns the query and args producing one column: the
// text that recreates the view. SQLite and SQL Server keep the original
// statement (complete DDL); MySQL, PostgreSQL and Oracle store only the SELECT
// body, which ComposeViewDDL wraps into CREATE OR REPLACE VIEW ... AS ....
//
// PostgreSQL materialized views are not covered (pg_views lists plain views
// only); callers asking for one get "not found" rather than a wrong body.
func ViewDefinitionQuery(dbType, schema, view string) (string, []interface{}, error) {
	switch Normalize(dbType) {
	case SQLite:
		return `SELECT sql FROM sqlite_master WHERE type = 'view' AND name = ?`,
			[]interface{}{view}, nil
	case MySQL:
		return `SELECT VIEW_DEFINITION FROM information_schema.VIEWS
			WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`,
			[]interface{}{schema, view}, nil
	case Postgres:
		return `SELECT definition FROM pg_views WHERE schemaname = $1 AND viewname = $2`,
			[]interface{}{schema, view}, nil
	case SQLServer:
		qualified := view
		if schema != "" && !strings.EqualFold(schema, "dbo") {
			qualified = schema + "." + view
		}
		return fmt.Sprintf(`SELECT OBJECT_DEFINITION(OBJECT_ID(%s))`,
			QuotedLiteral(SQLServer, qualified)), nil, nil
	case Oracle:
		// ALL_VIEWS.TEXT is a LONG column: it cannot be concatenated in SQL,
		// so the body comes back alone and ComposeViewDDL writes the prefix.
		return fmt.Sprintf(`SELECT TEXT FROM ALL_VIEWS WHERE OWNER = %s AND VIEW_NAME = %s`,
			QuotedLiteral(Oracle, upperASCII(schema)), QuotedLiteral(Oracle, upperASCII(view))), nil, nil
	}
	return "", nil, fmt.Errorf("sqltext: unknown database type %q", dbType)
}

// ComposeViewDDL turns a view body into DDL. SQLite and SQL Server already
// hand back a complete CREATE VIEW statement, which is returned unchanged;
// the engines that store only the SELECT get "CREATE OR REPLACE VIEW <name>
// AS" in front of it. An empty body stays empty (the object was not found).
func ComposeViewDDL(dbType, schema, view, body string) string {
	text := strings.TrimSpace(body)
	if text == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToUpper(text), "CREATE") {
		return text
	}
	name := view
	if q, err := QualifiedName(dbType, schema, view); err == nil {
		name = q
	} else if schema != "" {
		name = schema + "." + view
	}
	return "CREATE OR REPLACE VIEW " + name + " AS\n" + text
}

// CheckConstraintsQuery lists one table's CHECK constraints: constraint name
// and predicate, in name order. SQLite has no check-constraint catalog (the
// text only exists inside the CREATE TABLE statement), so it has no recipe and
// the driver answers ErrNotSupported.
func CheckConstraintsQuery(dbType, schema, table string) (string, []interface{}, error) {
	switch Normalize(dbType) {
	case MySQL:
		// CHECK_CONSTRAINTS arrived in MySQL 8.0.16; MariaDB lacks the view.
		return `SELECT tc.CONSTRAINT_NAME, cc.CHECK_CLAUSE
			FROM information_schema.TABLE_CONSTRAINTS tc
			INNER JOIN information_schema.CHECK_CONSTRAINTS cc
				ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA
				AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
			WHERE tc.TABLE_SCHEMA = ? AND tc.TABLE_NAME = ? AND tc.CONSTRAINT_TYPE = 'CHECK'
			ORDER BY tc.CONSTRAINT_NAME`,
			[]interface{}{schema, table}, nil
	case Postgres:
		// conbin is an expression tree node; pg_get_expr renders it without the
		// wrapping CHECK (...) that pg_get_constraintdef adds.
		return `SELECT conname, pg_get_expr(conbin, conrelid)
			FROM pg_constraint
			WHERE contype = 'c' AND conrelid = (
				SELECT c.oid FROM pg_class c
				INNER JOIN pg_namespace n ON n.oid = c.relnamespace
				WHERE n.nspname = $1 AND c.relname = $2)
			ORDER BY conname`,
			[]interface{}{schema, table}, nil
	case SQLServer:
		return fmt.Sprintf(`SELECT name, definition FROM sys.check_constraints
			WHERE parent_object_id = OBJECT_ID(%s) ORDER BY name`,
			QuotedLiteral(SQLServer, objectName(schema, table))), nil, nil
	case Oracle:
		// Oracle stores a NOT NULL column as a system-named CHECK constraint
		// ("COL" IS NOT NULL). Those are column nullability, not user checks:
		// system names plus the bare IS NOT NULL shape identify them, and a
		// user-declared check that happens to end in IS NOT NULL keeps its own
		// name, so it is not filtered out.
		return fmt.Sprintf(`SELECT CONSTRAINT_NAME, SEARCH_CONDITION FROM ALL_CONSTRAINTS
			WHERE OWNER = %s AND TABLE_NAME = %s AND CONSTRAINT_TYPE = 'C'
				AND NOT (CONSTRAINT_NAME LIKE 'SYS\_C%%' ESCAPE '\'
					AND SEARCH_CONDITION_VC LIKE '%%" IS NOT NULL')
			ORDER BY CONSTRAINT_NAME`,
			QuotedLiteral(Oracle, upperASCII(schema)), QuotedLiteral(Oracle, upperASCII(table))), nil, nil
	}
	return "", nil, fmt.Errorf("sqltext: engine %q has no check-constraint catalog", dbType)
}

// SequencesQuery lists a schema's sequences: name, start value, increment.
// SQLite and MySQL have no standalone sequence objects (MySQL's AUTO_INCREMENT
// is a table property), so they have no recipe and the driver answers
// ErrNotSupported.
func SequencesQuery(dbType, schema string) (string, []interface{}, error) {
	switch Normalize(dbType) {
	case Postgres:
		// pg_sequences reports NULL start_value/increment_by for sequences the
		// role may not read; 0 keeps the "unknown" signal in one place.
		return `SELECT sequencename, COALESCE(start_value, 0), COALESCE(increment_by, 0)
			FROM pg_sequences WHERE schemaname = $1 ORDER BY sequencename`,
			[]interface{}{schema}, nil
	case SQLServer:
		// start_value/increment are sql_variant; cast for a stable wire shape.
		return fmt.Sprintf(`SELECT name, CAST(start_value AS bigint), CAST(increment AS bigint)
			FROM sys.sequences WHERE schema_id = SCHEMA_ID(%s) ORDER BY name`,
			QuotedLiteral(SQLServer, schema)), nil, nil
	case Oracle:
		// Identity columns and materialized-view refresh own system sequences
		// whose names are reserved prefixes; a client's sequence list is about
		// the objects the user can actually use.
		return fmt.Sprintf(`SELECT SEQUENCE_NAME, MIN_VALUE, INCREMENT_BY FROM ALL_SEQUENCES
			WHERE SEQUENCE_OWNER = %s
				AND SEQUENCE_NAME NOT LIKE 'ISEQ$$%%'
				AND SEQUENCE_NAME NOT LIKE 'MVIEW$%%'
			ORDER BY SEQUENCE_NAME`,
			QuotedLiteral(Oracle, upperASCII(schema))), nil, nil
	}
	return "", nil, fmt.Errorf("sqltext: engine %q has no sequences", dbType)
}

// RoutinesQuery lists a schema's stored functions and procedures: name and
// kind, in kind/name order. Kind is normalized to RoutineFunction /
// RoutineProcedure spellings by the query. SQLite has no stored routines, so
// it has no recipe and the driver answers ErrNotSupported.
func RoutinesQuery(dbType, schema string) (string, []interface{}, error) {
	switch Normalize(dbType) {
	case MySQL:
		return `SELECT ROUTINE_NAME, ROUTINE_TYPE FROM information_schema.ROUTINES
			WHERE ROUTINE_SCHEMA = ? ORDER BY ROUTINE_TYPE, ROUTINE_NAME`,
			[]interface{}{schema}, nil
	case Postgres:
		// prokind: f = function, p = procedure. Aggregates/window functions
		// (a/w) are internal machinery, not what a routine tree lists.
		return `SELECT p.proname,
				CASE p.prokind WHEN 'p' THEN 'PROCEDURE' ELSE 'FUNCTION' END
			FROM pg_proc p
			INNER JOIN pg_namespace n ON n.oid = p.pronamespace
			WHERE n.nspname = $1 AND p.prokind IN ('f', 'p')
			ORDER BY 2, 1`,
			[]interface{}{schema}, nil
	case SQLServer:
		// P = procedure, FN/IF/TF = scalar/inline/table-valued function.
		// is_ms_shipped excludes the replication/system procedures SQL Server
		// installs into dbo (they would otherwise top the list).
		return fmt.Sprintf(`SELECT name,
				CASE WHEN type = 'P' THEN 'PROCEDURE' ELSE 'FUNCTION' END
			FROM sys.objects
			WHERE type IN ('P', 'FN', 'IF', 'TF') AND schema_id = SCHEMA_ID(%s)
				AND is_ms_shipped = 0
			ORDER BY 2, name`,
			QuotedLiteral(SQLServer, schema)), nil, nil
	case Oracle:
		return fmt.Sprintf(`SELECT OBJECT_NAME, OBJECT_TYPE FROM ALL_OBJECTS
			WHERE OWNER = %s AND OBJECT_TYPE IN ('PROCEDURE', 'FUNCTION')
			ORDER BY OBJECT_TYPE, OBJECT_NAME`,
			QuotedLiteral(Oracle, upperASCII(schema))), nil, nil
	}
	return "", nil, fmt.Errorf("sqltext: engine %q has no stored routines", dbType)
}

// objectName renders schema.table for engines that address objects by name.
func objectName(schema, name string) string {
	if schema == "" {
		return name
	}
	return schema + "." + name
}
