package sqltext

// Trigger catalog recipes: one place per engine for "which triggers does this
// table have" and "show me a trigger's definition". The client executes the
// returned query with the returned args through its normal driver, so dialect
// knowledge never leaks past this package.
//
// Engine notes:
//   - SQLite keeps everything in sqlite_master (the definition is free).
//   - MySQL/PostgreSQL answer from information_schema (PG's action_statement
//     is the trigger body; the full CREATE TRIGGER comes from pg_get_triggerdef).
//   - SQL Server carries the definition in OBJECT_DEFINITION.
//   - Oracle's user_triggers has DESCRIPTION + TRIGGER_BODY.
//
// DefinitionQuery returns the text that best reconstructs the CREATE TRIGGER:
// complete DDL where the engine can give it (SQLite/MSSQL), name+timing+body
// where it cannot (MySQL/PG/Oracle users get enough to read the logic).

import "fmt"

// TriggerInfo is one row of TriggersQuery's result.
type TriggerInfo struct {
	Name    string
	Timing  string // BEFORE / AFTER / INSTEAD OF (engine spelling)
	Event   string // INSERT / UPDATE / DELETE, engine spelling
	Enabled string // engine spelling of the status
}

// TriggersQuery returns the catalog query and its positional args listing the
// triggers of one table. Columns come back as name, timing, event, enabled —
// engines that do not expose a column return an empty string there.
func TriggersQuery(dbType, schema, table string) (string, []interface{}, error) {
	switch Normalize(dbType) {
	case SQLite:
		return `SELECT name, '', '', CASE WHEN sql IS NULL THEN 'drop' ELSE 'enabled' END
			FROM sqlite_master WHERE type = 'trigger' AND tbl_name = ? ORDER BY name`,
			[]interface{}{table}, nil
	case MySQL:
		return `SELECT TRIGGER_NAME, ACTION_TIMING, EVENT_MANIPULATION, STATUS
			FROM information_schema.TRIGGERS
			WHERE TRIGGER_SCHEMA = ? AND EVENT_OBJECT_TABLE = ? ORDER BY TRIGGER_NAME`,
			[]interface{}{schema, table}, nil
	case Postgres:
		return `SELECT TRIGGER_NAME, ACTION_TIMING, EVENT_OBJECT_MANIPULATION, ''
			FROM information_schema.TRIGGERS
			WHERE TRIGGER_SCHEMA = ? AND EVENT_OBJECT_TABLE = ? ORDER BY TRIGGER_NAME`,
			[]interface{}{schema, table}, nil
	case SQLServer:
		// Same shape SearchObjectsQuery uses for tiberius-based clients:
		// inline the literal, the driver does not take positional binds here.
		return fmt.Sprintf(`SELECT t.name,
				       CASE WHEN t.is_instead_of_trigger = 1 THEN 'INSTEAD OF' ELSE 'AFTER' END,
				       STUFF((SELECT ',' + te.type_desc FROM sys.trigger_events te
				              WHERE te.object_id = t.object_id FOR XML PATH('')), 1, 1, ''),
				       CASE WHEN t.is_disabled = 1 THEN 'disabled' ELSE 'enabled' END
			       FROM sys.triggers t
			       WHERE t.parent_id = OBJECT_ID(%s)
			       ORDER BY t.name`, QuotedLiteral(SQLServer, table)), nil, nil
	case Oracle:
		// Oracle's catalog stores upper-case names; upperASCII matches what
		// the user typed against what user_triggers holds.
		return fmt.Sprintf(`SELECT TRIGGER_NAME, TRIGGERING_TYPE, TRIGGERING_EVENT, STATUS
			FROM user_triggers WHERE TABLE_NAME = %s ORDER BY TRIGGER_NAME`,
			QuotedLiteral(Oracle, upperASCII(table))), nil, nil
	}
	return "", nil, fmt.Errorf("sqltext: unknown database type %q", dbType)
}

// TriggerDefinitionQuery returns the query and args that produce one column:
// the trigger's definition, as complete DDL where the engine offers it.
func TriggerDefinitionQuery(dbType, schema, trigger string) (string, []interface{}, error) {
	switch Normalize(dbType) {
	case SQLite:
		return `SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = ?`,
			[]interface{}{trigger}, nil
	case MySQL:
		q, err := QuoteIdent(dbType, trigger)
		if err != nil {
			return "", nil, err
		}
		return "SHOW CREATE TRIGGER " + q, nil, nil
	case Postgres:
		return `SELECT 'CREATE TRIGGER ' || tgname || ' ' || pg_get_triggerdef(oid)
			FROM pg_trigger WHERE tgname = ? AND NOT tgisinternal`,
			[]interface{}{trigger}, nil
	case SQLServer:
		return fmt.Sprintf(`SELECT OBJECT_DEFINITION(OBJECT_ID(%s))`,
			QuotedLiteral(SQLServer, trigger)), nil, nil
	case Oracle:
		return fmt.Sprintf(`SELECT DESCRIPTION || CHR(10) || TRIGGER_BODY
			FROM user_triggers WHERE TRIGGER_NAME = %s`,
			QuotedLiteral(Oracle, upperASCII(trigger))), nil, nil
	}
	return "", nil, fmt.Errorf("sqltext: unknown database type %q", dbType)
}
