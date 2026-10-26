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

// RoutineKindQuery returns the query and args answering one routine's kind
// (FUNCTION or PROCEDURE). MySQL is the only engine whose definition statement
// depends on it (SHOW CREATE PROCEDURE vs SHOW CREATE FUNCTION); every other
// engine answers ErrNotSupported and the driver skips the lookup.
func RoutineKindQuery(dbType, schema, name string) (string, []interface{}, error) {
	if Normalize(dbType) != MySQL {
		return "", nil, fmt.Errorf("sqltext: engine %q does not need a routine kind lookup", dbType)
	}
	return `SELECT ROUTINE_TYPE FROM information_schema.ROUTINES
		WHERE ROUTINE_SCHEMA = ? AND ROUTINE_NAME = ?`,
		[]interface{}{schema, name}, nil
}

// RoutineDefinitionQuery returns the query and args producing a routine's
// definition: one row per definition (PostgreSQL answers one per overload).
// kind disambiguates MySQL's two SHOW CREATE statements and is ignored
// elsewhere.
func RoutineDefinitionQuery(dbType, schema, name, kind string) (string, []interface{}, error) {
	switch Normalize(dbType) {
	case MySQL:
		stmt := "SHOW CREATE PROCEDURE "
		if strings.EqualFold(kind, "FUNCTION") {
			stmt = "SHOW CREATE FUNCTION "
		}
		quoted, err := QuoteIdent(dbType, name)
		if err != nil {
			return "", nil, err
		}
		return stmt + quoted, nil, nil
	case Postgres:
		// pg_get_functiondef renders CREATE OR REPLACE for one overload; the
		// driver joins the rows when a name has several signatures.
		return `SELECT pg_get_functiondef(p.oid)
			FROM pg_proc p
			INNER JOIN pg_namespace n ON n.oid = p.pronamespace
			WHERE n.nspname = $1 AND p.proname = $2
			ORDER BY p.oid`,
			[]interface{}{schema, name}, nil
	case SQLServer:
		return fmt.Sprintf(`SELECT OBJECT_DEFINITION(OBJECT_ID(%s))`,
			QuotedLiteral(SQLServer, objectName(schema, name))), nil, nil
	case Oracle:
		// ALL_SOURCE holds the source one line per row, starting at the
		// "PROCEDURE name ..." header; the driver joins and prefixes CREATE OR
		// REPLACE.
		return fmt.Sprintf(`SELECT TEXT FROM ALL_SOURCE
			WHERE OWNER = %s AND NAME = %s AND TYPE IN ('PROCEDURE', 'FUNCTION')
			ORDER BY TYPE, LINE`,
			QuotedLiteral(Oracle, upperASCII(schema)), QuotedLiteral(Oracle, upperASCII(name))), nil, nil
	}
	return "", nil, fmt.Errorf("sqltext: engine %q has no stored routines", dbType)
}

// PartitionsQuery lists a table's partitions: label, boundary text and an
// approximate row count. SQLite has no partitioning and therefore no recipe.
func PartitionsQuery(dbType, schema, table string) (string, []interface{}, error) {
	switch Normalize(dbType) {
	case Postgres:
		// A partitioned table's partitions are ordinary child tables; relkind
		// 'p' on the parent means the parent itself holds no rows.
		return `SELECT c.relname, COALESCE(pg_get_expr(c.relpartbound, c.oid), ''),
				CAST(GREATEST(c.reltuples, -1) AS bigint)
			FROM pg_inherits i
			INNER JOIN pg_class c ON c.oid = i.inhrelid
			INNER JOIN pg_class p ON p.oid = i.inhparent
			INNER JOIN pg_namespace n ON n.oid = p.relnamespace
			WHERE n.nspname = $1 AND p.relname = $2
			ORDER BY c.relname`,
			[]interface{}{schema, table}, nil
	case MySQL:
		return `SELECT PARTITION_NAME, COALESCE(PARTITION_DESCRIPTION, ''),
				COALESCE(TABLE_ROWS, 0)
			FROM information_schema.PARTITIONS
			WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND PARTITION_NAME IS NOT NULL
			ORDER BY PARTITION_ORDINAL_POSITION`,
			[]interface{}{schema, table}, nil
	case SQLServer:
		// SQL Server has no partition names: the number is the identity. Every
		// table has a partition 1, so the partition scheme (ps.name) tells a
		// really partitioned index from an ordinary one. The boundary value
		// attached to partition N is its upper bound for RANGE LEFT and its
		// exclusive upper bound for RANGE RIGHT (boundary_value_on_right).
		return fmt.Sprintf(`SELECT CAST(p.partition_number AS nvarchar(10)),
				COALESCE(CAST(prv.value AS nvarchar(64)), ''),
				COALESCE(CAST(p.rows AS bigint), 0),
				COALESCE(pf.boundary_value_on_right, 0),
				COALESCE(ps.name, '')
			FROM sys.partitions p
			INNER JOIN sys.indexes i ON i.object_id = p.object_id AND i.index_id = p.index_id
			INNER JOIN sys.tables t ON t.object_id = p.object_id
			INNER JOIN sys.schemas s ON s.schema_id = t.schema_id
			LEFT JOIN sys.partition_schemes ps ON ps.data_space_id = i.data_space_id
			LEFT JOIN sys.partition_functions pf ON pf.function_id = ps.function_id
			LEFT JOIN sys.partition_range_values prv
				ON prv.function_id = pf.function_id AND prv.boundary_id = p.partition_number
			WHERE s.name = %s AND t.name = %s AND i.index_id IN (0, 1)
			ORDER BY p.partition_number`,
			QuotedLiteral(SQLServer, schema), QuotedLiteral(SQLServer, table)), nil, nil
	case Oracle:
		// HIGH_VALUE is a LONG: it must be selected bare (no NVL/concat).
		return fmt.Sprintf(`SELECT PARTITION_NAME, HIGH_VALUE, NVL(NUM_ROWS, 0)
			FROM ALL_TAB_PARTITIONS
			WHERE TABLE_OWNER = %s AND TABLE_NAME = %s
			ORDER BY PARTITION_POSITION`,
			QuotedLiteral(Oracle, upperASCII(schema)), QuotedLiteral(Oracle, upperASCII(table))), nil, nil
	}
	return "", nil, fmt.Errorf("sqltext: engine %q has no table partitioning", dbType)
}

// EnumTypesQuery lists a schema's user-defined enum types one label per row, in
// declaration order (drivers group by type). Only PostgreSQL has them: MySQL's
// ENUM is a per-column type, SQL Server/Oracle/SQLite have no equivalent.
func EnumTypesQuery(dbType, schema string) (string, []interface{}, error) {
	if Normalize(dbType) != Postgres {
		return "", nil, fmt.Errorf("sqltext: engine %q has no user-defined enum types", dbType)
	}
	return `SELECT t.typname, e.enumlabel
		FROM pg_type t
		INNER JOIN pg_enum e ON e.enumtypid = t.oid
		INNER JOIN pg_namespace n ON n.oid = t.typnamespace
		WHERE n.nspname = $1
		ORDER BY t.typname, e.enumsortorder`,
		[]interface{}{schema}, nil
}
