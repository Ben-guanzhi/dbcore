package sqltext

// Catalog SQL generation for every engine the clients support.
//
// The engine identifier is the type tag; each function is the op side of the
// op-and-type table. Adding an engine means touching the switch arms below,
// not the callers. All functions return the SQL text and the bind args the
// engine's driver expects.

import (
	"fmt"
)

// namePredicate is the inlined-literal form of the exact-name filter, for the
// engines whose drivers make binds unreliable (go-mssqldb, go-ora).
func NamePredicate(dbType, column, exact string, insensitive bool) string {
	if exact == "" {
		return ""
	}
	literal := QuotedLiteral(dbType, exact)
	if insensitive {
		return fmt.Sprintf(` AND LOWER(%s) = LOWER(%s)`, column, literal)
	}
	return fmt.Sprintf(` AND %s = %s`, column, literal)
}

// DatabasesQuery returns the catalog statement that lists every database.
func DatabasesQuery(dbType string) (string, error) {
	switch dbType {
	case MySQL:
		return "SHOW DATABASES", nil
	case Postgres:
		return "SELECT datname FROM pg_database WHERE datistemplate = false", nil
	case SQLite:
		// SQLite has no database list; the caller substitutes the file name.
		return "", nil
	case SQLServer:
		return "SELECT name FROM sys.databases ORDER BY name", nil
	case Oracle:
		return "SELECT name FROM v$database ORDER BY name", nil
	default:
		return "", fmt.Errorf("unsupported type: %s", dbType)
	}
}

// SchemasQuery returns the catalog statement that lists every schema.
func SchemasQuery(dbType, database string) (string, error) {
	switch dbType {
	case SQLServer:
		name, err := QuoteIdent(dbType, database)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf(`SELECT s.name FROM %s.sys.schemas s
			WHERE s.name NOT LIKE 'db[_]%%' AND s.name NOT IN ('sys', 'INFORMATION_SCHEMA', 'guest')
			ORDER BY s.name`, name), nil
	case Postgres:
		return `SELECT nspname FROM pg_namespace
			WHERE nspname NOT LIKE 'pg\_%' AND nspname <> 'information_schema'
			ORDER BY nspname`, nil
	default:
		// mysql uses the database as its schema, sqlite has a single main schema,
		// oracle is introspected through the connected user.
		return "", nil
	}
}

// TablesQuery returns the catalog statement that lists tables and views.
func TablesQuery(dbType, database, schema, exact string, insensitive bool) (string, []interface{}, error) {
	switch dbType {
	case MySQL:
		query := `SELECT TABLE_NAME, TABLE_TYPE, TABLE_COMMENT FROM information_schema.TABLES
			WHERE TABLE_SCHEMA = ?`
		args := []interface{}{database}
		if exact != "" {
			if insensitive {
				query += ` AND LOWER(TABLE_NAME) = LOWER(?)`
			} else {
				query += ` AND TABLE_NAME = ?`
			}
			args = append(args, exact)
		}
		return query + ` ORDER BY TABLE_NAME`, args, nil
	case Postgres:
		tables, views := "", ""
		args := []interface{}{schema}
		if exact != "" {
			if insensitive {
				tables = ` AND LOWER(tablename) = LOWER($2)`
				views = ` AND LOWER(viewname) = LOWER($3)`
			} else {
				tables = ` AND tablename = $2`
				views = ` AND viewname = $3`
			}
			args = append(args, exact, exact)
		}
		query := fmt.Sprintf(`SELECT tablename, 'BASE TABLE', '' FROM pg_tables WHERE schemaname = $1%s
			UNION ALL
			SELECT viewname, 'VIEW', '' FROM pg_views WHERE schemaname = $1%s
			ORDER BY 1`, tables, views)
		return query, args, nil
	case SQLite:
		query := `SELECT name, type, '' FROM sqlite_master
			WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%'`
		var args []interface{}
		if exact != "" {
			if insensitive {
				query += ` AND LOWER(name) = LOWER(?)`
			} else {
				query += ` AND name = ?`
			}
			args = append(args, exact)
		}
		return query + ` ORDER BY name`, args, nil
	case SQLServer:
		name, err := QuoteIdent(dbType, database)
		if err != nil {
			return "", nil, err
		}
		return fmt.Sprintf(`SELECT TABLE_NAME, TABLE_TYPE, '' FROM %s.INFORMATION_SCHEMA.TABLES
			WHERE TABLE_SCHEMA = %s%s ORDER BY TABLE_NAME`, name,
			QuotedLiteral(dbType, schema), NamePredicate(dbType, "TABLE_NAME", exact, insensitive)), nil, nil
	case Oracle:
		return `SELECT OBJECT_NAME, OBJECT_TYPE, '' FROM ALL_OBJECTS
			WHERE OBJECT_TYPE IN ('TABLE', 'VIEW') AND OWNER = USER` +
			NamePredicate(dbType, "OBJECT_NAME", exact, insensitive) + ` ORDER BY OBJECT_NAME`, nil, nil
	default:
		return "", nil, fmt.Errorf("unsupported type: %s", dbType)
	}
}

// ColumnsQuery returns the catalog statement that lists a table's columns.
func ColumnsQuery(dbType, database, schema, table string) (string, []interface{}, error) {
	switch dbType {
	case MySQL:
		query := `SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT,
			CASE WHEN COLUMN_KEY = 'PRI' THEN 1 ELSE 0 END, COLUMN_COMMENT
			FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
			ORDER BY ORDINAL_POSITION`
		return query, []interface{}{schema, table}, nil
	case Postgres:
		query := `SELECT a.attname,
			format_type(a.atttypid, a.atttypmod),
			CASE WHEN a.attnotnull THEN 'NO' ELSE 'YES' END,
			COALESCE(pg_get_expr(d.adbin, d.adrelid), ''),
			CASE WHEN i.indisprimary THEN 1 ELSE 0 END,
			COALESCE(col_description(c.oid, a.attnum), '')
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
			LEFT JOIN pg_attrdef d ON d.adrelid = c.oid AND d.adnum = a.attnum
			LEFT JOIN pg_index i ON i.indrelid = c.oid AND i.indisprimary AND a.attnum = ANY(i.indkey)
			WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind IN ('r', 'v', 'm', 'p')
			ORDER BY a.attnum`
		return query, []interface{}{schema, table}, nil
	case SQLite:
		name, err := QuoteIdent(dbType, table)
		if err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("PRAGMA table_info(%s)", name), nil, nil
	case SQLServer:
		name, err := QuoteIdent(dbType, database)
		if err != nil {
			return "", nil, err
		}
		dbLiteral := QuotedLiteral(SQLServer, name)
		objectID := fmt.Sprintf("OBJECT_ID(%s + '.' + QUOTENAME(c.TABLE_SCHEMA) + '.' + QUOTENAME(c.TABLE_NAME))", dbLiteral)
		comment := fmt.Sprintf(`ISNULL((SELECT TOP 1 CAST(ep.value AS NVARCHAR(4000))
			FROM %s.sys.extended_properties ep
			INNER JOIN %s.sys.columns sc
				ON sc.object_id = ep.major_id AND sc.column_id = ep.minor_id
			WHERE ep.class = 1 AND ep.name = 'MS_Description'
				AND sc.object_id = %s AND sc.name = c.COLUMN_NAME), '')`, name, name, objectID)
		query := fmt.Sprintf(`SELECT c.COLUMN_NAME, c.DATA_TYPE, c.IS_NULLABLE, c.COLUMN_DEFAULT,
			CASE WHEN pk.COLUMN_NAME IS NOT NULL THEN 1 ELSE 0 END, %s
			FROM %s.INFORMATION_SCHEMA.COLUMNS c
			LEFT JOIN (
				SELECT ku.TABLE_SCHEMA, ku.TABLE_NAME, ku.COLUMN_NAME
				FROM %s.INFORMATION_SCHEMA.TABLE_CONSTRAINTS tc
				INNER JOIN %s.INFORMATION_SCHEMA.KEY_COLUMN_USAGE ku
					ON ku.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
					AND ku.TABLE_NAME = tc.TABLE_NAME AND ku.TABLE_SCHEMA = tc.TABLE_SCHEMA
				WHERE tc.CONSTRAINT_TYPE = 'PRIMARY KEY'
			) pk ON pk.TABLE_SCHEMA = c.TABLE_SCHEMA AND pk.TABLE_NAME = c.TABLE_NAME
				AND pk.COLUMN_NAME = c.COLUMN_NAME
			WHERE c.TABLE_NAME = %s AND c.TABLE_SCHEMA = %s
			ORDER BY c.ORDINAL_POSITION`, comment, name, name, name, QuotedLiteral(SQLServer, table), QuotedLiteral(SQLServer, schema))
		return query, nil, nil
	case Oracle:
		return fmt.Sprintf(`SELECT c.COLUMN_NAME, c.DATA_TYPE, c.NULLABLE, c.DATA_DEFAULT,
			CASE WHEN cc.COLUMN_NAME IS NOT NULL THEN 1 ELSE 0 END, c.COMMENTS
			FROM ALL_TAB_COLUMNS c
			LEFT JOIN (
				SELECT cols.COLUMN_NAME
				FROM ALL_CONSTRAINTS cons
				INNER JOIN ALL_CONS_COLUMNS cols
					ON cons.CONSTRAINT_NAME = cols.CONSTRAINT_NAME
				WHERE cons.TABLE_NAME = %s AND cons.CONSTRAINT_TYPE = 'P'
			) cc ON c.COLUMN_NAME = cc.COLUMN_NAME
			WHERE c.TABLE_NAME = %s AND c.OWNER = USER
			ORDER BY c.COLUMN_ID`,
			QuotedLiteral(Oracle, table), QuotedLiteral(Oracle, table)), nil, nil
	default:
		return "", nil, fmt.Errorf("unsupported type: %s", dbType)
	}
}

// IndexesQuery returns the catalog statement that lists a table's indexes.
// Every statement answers the same five columns in the same order - name, kind,
// is-unique, is-primary, column - because that is what the readers scan: the
// root client's tableIndexes and the Fyne client's scanIndexRow both declare
// five destinations, so a statement with fewer fails every table on that
// engine before a row is read. The flags are named for what they mean, not for
// the catalog column they come from: information_schema.STATISTICS carries
// NON_UNIQUE, and handing that through would report a plain index as a unique
// one.
func IndexesQuery(dbType, database, schema, table string) (string, []interface{}, error) {
	switch dbType {
	case MySQL:
		// STATISTICS has no is-primary column either: MySQL marks the key by
		// naming it PRIMARY, which is what the fourth column answers with.
		query := `SELECT INDEX_NAME, INDEX_TYPE, CASE WHEN NON_UNIQUE = 0 THEN 1 ELSE 0 END,
			CASE WHEN INDEX_NAME = 'PRIMARY' THEN 1 ELSE 0 END, COLUMN_NAME
			FROM information_schema.STATISTICS
			WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
			ORDER BY INDEX_NAME, SEQ_IN_INDEX`
		return query, []interface{}{schema, table}, nil
	case Postgres:
		query := `SELECT c.relname, am.amname, CASE WHEN i.indisunique THEN 1 ELSE 0 END,
			CASE WHEN i.indisprimary THEN 1 ELSE 0 END, x.attname
			FROM pg_index i
			JOIN pg_class c ON c.oid = i.indexrelid
			JOIN pg_am am ON am.oid = c.relam
			JOIN pg_class t ON t.oid = i.indrelid
			JOIN pg_namespace n ON n.oid = t.relnamespace
			JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord) ON true
			JOIN pg_attribute x ON x.attrelid = i.indrelid AND x.attnum = k.attnum
			WHERE n.nspname = $1 AND t.relname = $2
			ORDER BY c.relname, k.ord`
		return query, []interface{}{schema, table}, nil
	case SQLServer:
		quoted, err := QuoteIdent(dbType, database)
		if err != nil {
			return "", nil, err
		}
		objectID, err := QualifiedName(dbType, database, schema, table)
		if err != nil {
			return "", nil, err
		}
		literal := QuotedLiteral(SQLServer, objectID)
		query := fmt.Sprintf(`SELECT i.name, i.type_desc, CAST(i.is_unique AS int),
			CAST(i.is_primary_key AS int), col.name
			FROM %s.sys.indexes i
			INNER JOIN %s.sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
			INNER JOIN %s.sys.columns col ON col.object_id = ic.object_id AND col.column_id = ic.column_id
			WHERE i.object_id = OBJECT_ID(%s) AND i.is_hypothetical = 0
			ORDER BY i.name, ic.key_ordinal`, quoted, quoted, quoted, literal)
		return query, nil, nil
	case Oracle:
		return fmt.Sprintf(`SELECT i.index_name, i.index_type,
			CASE WHEN i.uniqueness = 'UNIQUE' THEN 1 ELSE 0 END,
			CASE WHEN EXISTS (SELECT 1 FROM ALL_CONSTRAINTS ac
				WHERE ac.constraint_type = 'P' AND ac.owner = i.owner AND ac.index_name = i.index_name)
			THEN 1 ELSE 0 END,
			c.column_name
			FROM ALL_INDEXES i
			JOIN ALL_IND_COLUMNS c ON c.index_owner = i.owner AND c.index_name = i.index_name
				AND c.table_owner = i.owner AND c.table_name = i.table_name
			WHERE i.table_name = %s AND i.owner = USER
			ORDER BY i.index_name, c.column_position`, QuotedLiteral(Oracle, table)), nil, nil
	case SQLite:
		return "", nil, nil
	default:
		return "", nil, fmt.Errorf("unsupported type: %s", dbType)
	}
}

// ForeignKeysQuery returns the catalog statement that lists a table's foreign keys.
func ForeignKeysQuery(dbType, database, schema, table string) (string, []interface{}, error) {
	switch dbType {
	case MySQL:
		query := `SELECT k.CONSTRAINT_NAME, k.COLUMN_NAME, k.REFERENCED_TABLE_SCHEMA,
			k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME,
			IFNULL(r.DELETE_RULE, ''), IFNULL(r.UPDATE_RULE, '')
			FROM information_schema.KEY_COLUMN_USAGE k
			LEFT JOIN information_schema.REFERENTIAL_CONSTRAINTS r
				ON r.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA AND r.CONSTRAINT_NAME = k.CONSTRAINT_NAME
			WHERE k.TABLE_SCHEMA = ? AND k.TABLE_NAME = ? AND k.REFERENCED_TABLE_NAME IS NOT NULL
			ORDER BY k.CONSTRAINT_NAME, k.ORDINAL_POSITION`
		return query, []interface{}{schema, table}, nil
	case Postgres:
		query := `SELECT con.conname, xg.attname, confnsp.nspname, confcls.relname, yg.attname,
			COALESCE(CASE con.confdeltype WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT'
				WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL' WHEN 'd' THEN 'SET DEFAULT' END, ''),
			COALESCE(CASE con.confupdtype WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT'
				WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL' WHEN 'd' THEN 'SET DEFAULT' END, '')
			FROM pg_constraint con
			JOIN pg_class cls ON cls.oid = con.conrelid
			JOIN pg_namespace nsp ON nsp.oid = cls.relnamespace
			JOIN LATERAL unnest(con.conkey) WITH ORDINALITY AS x(attnum, ord) ON true
			JOIN LATERAL unnest(con.confkey) WITH ORDINALITY AS y(attnum, ord) ON y.ord = x.ord
			JOIN pg_attribute xg ON xg.attrelid = con.conrelid AND xg.attnum = x.attnum
			JOIN pg_attribute yg ON yg.attrelid = con.confrelid AND yg.attnum = y.attnum
			JOIN pg_class confcls ON confcls.oid = con.confrelid
			JOIN pg_namespace confnsp ON confnsp.oid = confcls.relnamespace
			WHERE con.contype = 'f' AND nsp.nspname = $1 AND cls.relname = $2
			ORDER BY con.conname, x.ord`
		return query, []interface{}{schema, table}, nil
	case SQLServer:
		quoted, err := QuoteIdent(dbType, database)
		if err != nil {
			return "", nil, err
		}
		objectID, err := QualifiedName(dbType, database, schema, table)
		if err != nil {
			return "", nil, err
		}
		literal := QuotedLiteral(SQLServer, objectID)
		query := fmt.Sprintf(`SELECT fk.name, col.name, rs.name, ref.name, refcol.name,
			fk.delete_referential_action_desc, fk.update_referential_action_desc
			FROM %s.sys.foreign_keys fk
			INNER JOIN %s.sys.foreign_key_columns fkc ON fkc.constraint_object_id = fk.object_id
			INNER JOIN %s.sys.columns col ON col.object_id = fkc.parent_object_id
				AND col.column_id = fkc.parent_column_id
			INNER JOIN %s.sys.tables ref ON ref.object_id = fk.referenced_object_id
			INNER JOIN %s.sys.schemas rs ON rs.schema_id = ref.schema_id
			INNER JOIN %s.sys.columns refcol ON refcol.object_id = fkc.referenced_object_id
				AND refcol.column_id = fkc.referenced_column_id
			WHERE fk.parent_object_id = OBJECT_ID(%s)
			ORDER BY fk.name, fkc.constraint_column_id`,
			quoted, quoted, quoted, quoted, quoted, quoted, literal)
		return query, nil, nil
	case Oracle:
		return fmt.Sprintf(`SELECT ac.constraint_name, cc.column_name, rac.owner, rac.table_name,
			rcc.column_name, ac.delete_rule, ''
			FROM ALL_CONSTRAINTS ac
			JOIN ALL_CONS_COLUMNS cc ON cc.owner = ac.owner AND cc.constraint_name = ac.constraint_name
			JOIN ALL_CONSTRAINTS rac ON rac.owner = ac.r_owner AND rac.constraint_name = ac.r_constraint_name
			JOIN ALL_CONS_COLUMNS rcc ON rcc.owner = rac.owner AND rcc.constraint_name = rac.constraint_name
				AND rcc.position = cc.position
			WHERE ac.constraint_type = 'R' AND ac.owner = USER AND ac.table_name = %s
			ORDER BY ac.constraint_name, cc.position`, QuotedLiteral(Oracle, table)), nil, nil
	case SQLite:
		return "", nil, nil
	default:
		return "", nil, fmt.Errorf("unsupported type: %s", dbType)
	}
}

// FullColumnTypesQuery returns the catalog statement that reads name -> full
// type (with length/precision) for engines that report bare type names.
func FullColumnTypesQuery(dbType, database, schema, table string) (string, []interface{}, error) {
	switch dbType {
	case SQLServer:
		quoted, err := QuoteIdent(dbType, database)
		if err != nil {
			return "", nil, err
		}
		query := fmt.Sprintf(`SELECT COLUMN_NAME,
			DATA_TYPE + CASE
				WHEN DATA_TYPE IN ('varchar', 'char', 'varbinary', 'binary', 'nchar') THEN
					CASE WHEN CHARACTER_MAXIMUM_LENGTH = -1 THEN '(max)'
						ELSE '(' + CAST(CHARACTER_MAXIMUM_LENGTH AS varchar(20)) + ')' END
				WHEN DATA_TYPE = 'nvarchar' THEN
					CASE WHEN CHARACTER_MAXIMUM_LENGTH = -1 THEN '(max)'
						ELSE '(' + CAST(CHARACTER_MAXIMUM_LENGTH / 2 AS varchar(20)) + ')' END
				WHEN DATA_TYPE IN ('decimal', 'numeric') THEN
					'(' + CAST(NUMERIC_PRECISION AS varchar(20)) + ', ' + CAST(NUMERIC_SCALE AS varchar(20)) + ')'
				WHEN DATA_TYPE = 'float' AND NUMERIC_PRECISION IS NOT NULL AND NUMERIC_PRECISION < 53 THEN
					'(' + CAST(NUMERIC_PRECISION AS varchar(20)) + ')'
				ELSE '' END
			FROM %s.INFORMATION_SCHEMA.COLUMNS
			WHERE TABLE_NAME = %s AND TABLE_SCHEMA = %s`,
			quoted, QuotedLiteral(SQLServer, table), QuotedLiteral(SQLServer, schema))
		return query, nil, nil
	case Oracle:
		return fmt.Sprintf(`SELECT COLUMN_NAME,
			DATA_TYPE || CASE
				WHEN DATA_TYPE IN ('VARCHAR2', 'NVARCHAR2', 'CHAR', 'NCHAR') THEN '(' || DATA_LENGTH || ')'
				WHEN DATA_TYPE = 'NUMBER' AND DATA_PRECISION IS NOT NULL THEN
					'(' || DATA_PRECISION || CASE WHEN DATA_SCALE > 0 THEN ', ' || DATA_SCALE ELSE '' END || ')'
				ELSE '' END
			FROM ALL_TAB_COLUMNS
			WHERE TABLE_NAME = %s AND OWNER = USER`, QuotedLiteral(Oracle, table)), nil, nil
	default:
		return "", nil, nil
	}
}

// PreviewPageQuery returns the catalog statement that reads one page of a table.
// where and orderBy are pre-built clauses (without leading WHERE/ORDER BY keywords).
func PreviewPageQuery(dbType, database, schema, table, where, orderBy string, limit, offset int) (string, []interface{}, error) {
	name, err := QualifiedTable(dbType, database, schema, table)
	if err != nil {
		return "", nil, err
	}
	switch dbType {
	case MySQL, Postgres, SQLite:
		return fmt.Sprintf("SELECT * FROM %s%s%s LIMIT %d OFFSET %d", name, where, orderBy, limit, offset), nil, nil
	case SQLServer, Oracle:
		return fmt.Sprintf("SELECT * FROM %s%s%s OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", name, where, orderBy, offset, limit), nil, nil
	default:
		return "", nil, fmt.Errorf("unsupported type: %s", dbType)
	}
}
