package drivertest

// Per-engine fixture DDL for the catalog contract subtests (P3-8) and the
// extra-metadata subtests (P3-4). It lives here — not in each engine test —
// so every engine is held to the same fixture, and a client running Spec gets
// the same coverage as the in-kernel tests.

import (
	"fmt"
	"strings"

	"github.com/dbcore/dbcore"
)

// fixtures is one engine's DDL for the contract's extra objects. A nil
// function means the engine has no such concept: the subtest asserts
// ErrNotSupported instead of provisioning it.
type fixtures struct {
	// childTable creates a table with an FK to parent and two indexes: ux
	// (unique, on tag) and ix (on pid, tag).
	childTable func(child, parent, fk, ux, ix string) []string
	// view creates a view over table exposing id and name.
	view func(view, table string) string
	// checkTable creates a table whose only CHECK constraint is named.
	checkTable func(table, constraint string) []string
	sequence   func(name string) []string
	routine    func(name string) []string
	// routineKind is the kind routine() creates (FUNCTION or PROCEDURE).
	routineKind string
	dropView    func(view string) []string
	dropSeq     func(name string) []string
	dropRoutine func(name string) []string
	// partitioned creates a partitioned table and reports the partition names
	// the engine stores, plus teardown statements in execution order.
	partitioned func(table string) (ddl []string, names []string, drop []string)
	// enumType creates one user-defined enum type and reports its labels in
	// declaration order.
	enumType func(name string) (ddl []string, labels []string, drop []string)
}

// fixturesFor returns the fixture set for kind. Unknown kinds get the SQLite
// shape, which is the portable baseline (INTEGER/TEXT, no sequences/routines).
func fixturesFor(kind dbcore.DatabaseKind) fixtures {
	switch kind {
	case dbcore.KindMySQL:
		return fixtures{
			childTable: func(child, parent, fk, ux, ix string) []string {
				return []string{
					fmt.Sprintf("CREATE TABLE %s (id INT PRIMARY KEY, pid INT, tag VARCHAR(64), CONSTRAINT %s FOREIGN KEY (pid) REFERENCES %s(id) ON DELETE CASCADE)", child, fk, parent),
					fmt.Sprintf("CREATE UNIQUE INDEX %s ON %s (tag)", ux, child),
					fmt.Sprintf("CREATE INDEX %s ON %s (pid, tag)", ix, child),
				}
			},
			view: func(view, table string) string {
				return fmt.Sprintf("CREATE VIEW %s AS SELECT id, name FROM %s", view, table)
			},
			checkTable: func(table, constraint string) []string {
				return []string{fmt.Sprintf("CREATE TABLE %s (id INT, qty INT, CONSTRAINT %s CHECK (qty > 0))", table, constraint)}
			},
			routine: func(name string) []string {
				return []string{fmt.Sprintf("CREATE FUNCTION %s() RETURNS INT DETERMINISTIC RETURN 1", name)}
			},
			routineKind: dbcore.RoutineFunction,
			partitioned: func(table string) ([]string, []string, []string) {
				return []string{fmt.Sprintf("CREATE TABLE %s (id INT, qty INT) PARTITION BY RANGE (qty) (PARTITION p0 VALUES LESS THAN (10), PARTITION p1 VALUES LESS THAN MAXVALUE)", table)},
					[]string{"p0", "p1"},
					[]string{DropTableSQL(dbcore.KindMySQL, table)}
			},
			dropView:    func(v string) []string { return []string{"DROP VIEW IF EXISTS " + v} },
			dropRoutine: func(n string) []string { return []string{"DROP FUNCTION IF EXISTS " + n} },
		}
	case dbcore.KindPostgres:
		return fixtures{
			childTable: func(child, parent, fk, ux, ix string) []string {
				return []string{
					fmt.Sprintf("CREATE TABLE %s (id INTEGER PRIMARY KEY, pid INTEGER, tag TEXT, CONSTRAINT %s FOREIGN KEY (pid) REFERENCES %s(id) ON DELETE CASCADE)", child, fk, parent),
					fmt.Sprintf("CREATE UNIQUE INDEX %s ON %s (tag)", ux, child),
					fmt.Sprintf("CREATE INDEX %s ON %s (pid, tag)", ix, child),
				}
			},
			view: func(view, table string) string {
				return fmt.Sprintf("CREATE VIEW %s AS SELECT id, name FROM %s", view, table)
			},
			checkTable: func(table, constraint string) []string {
				return []string{fmt.Sprintf("CREATE TABLE %s (id INTEGER, qty INTEGER, CONSTRAINT %s CHECK (qty > 0))", table, constraint)}
			},
			sequence: func(name string) []string {
				return []string{fmt.Sprintf("CREATE SEQUENCE %s START 5 INCREMENT 3", name)}
			},
			routine: func(name string) []string {
				return []string{fmt.Sprintf("CREATE FUNCTION %s() RETURNS int LANGUAGE sql AS 'SELECT 1'", name)}
			},
			routineKind: dbcore.RoutineFunction,
			partitioned: func(table string) ([]string, []string, []string) {
				return []string{
						fmt.Sprintf("CREATE TABLE %s (id INTEGER, qty INTEGER) PARTITION BY RANGE (qty)", table),
						fmt.Sprintf("CREATE TABLE %s_p0 PARTITION OF %s FOR VALUES FROM (0) TO (10)", table, table),
						fmt.Sprintf("CREATE TABLE %s_p1 PARTITION OF %s FOR VALUES FROM (10) TO (100)", table, table),
					},
					[]string{table + "_p0", table + "_p1"},
					[]string{"DROP TABLE IF EXISTS " + quoteBare(dbcore.KindPostgres, table) + " CASCADE"}
			},
			enumType: func(name string) ([]string, []string, []string) {
				return []string{fmt.Sprintf("CREATE TYPE %s AS ENUM ('sad', 'ok', 'happy')", name)},
					[]string{"sad", "ok", "happy"},
					[]string{"DROP TYPE IF EXISTS " + quoteBare(dbcore.KindPostgres, name)}
			},
			dropView:    func(v string) []string { return []string{"DROP VIEW IF EXISTS " + v} },
			dropSeq:     func(n string) []string { return []string{"DROP SEQUENCE IF EXISTS " + n} },
			dropRoutine: func(n string) []string { return []string{"DROP FUNCTION IF EXISTS " + n + "()"} },
		}
	case dbcore.KindMSSQL:
		return fixtures{
			childTable: func(child, parent, fk, ux, ix string) []string {
				return []string{
					fmt.Sprintf("CREATE TABLE %s (id INT PRIMARY KEY, pid INT, tag NVARCHAR(64), CONSTRAINT %s FOREIGN KEY (pid) REFERENCES %s(id) ON DELETE CASCADE)", child, fk, parent),
					fmt.Sprintf("CREATE UNIQUE INDEX %s ON %s (tag)", ux, child),
					fmt.Sprintf("CREATE INDEX %s ON %s (pid, tag)", ix, child),
				}
			},
			view: func(view, table string) string {
				return fmt.Sprintf("CREATE VIEW %s AS SELECT id, name FROM %s", view, table)
			},
			checkTable: func(table, constraint string) []string {
				return []string{fmt.Sprintf("CREATE TABLE %s (id INT, qty INT CONSTRAINT %s CHECK (qty > 0))", table, constraint)}
			},
			sequence: func(name string) []string {
				return []string{fmt.Sprintf("CREATE SEQUENCE %s START WITH 5 INCREMENT BY 3", name)}
			},
			routine: func(name string) []string {
				return []string{fmt.Sprintf("CREATE PROCEDURE %s AS SELECT 1", name)}
			},
			routineKind: dbcore.RoutineProcedure,
			partitioned: func(table string) ([]string, []string, []string) {
				pf := "pf_" + table
				ps := "ps_" + table
				return []string{
						fmt.Sprintf("CREATE PARTITION FUNCTION %s (int) AS RANGE LEFT FOR VALUES (10)", pf),
						fmt.Sprintf("CREATE PARTITION SCHEME %s AS PARTITION %s ALL TO ([PRIMARY])", ps, pf),
						fmt.Sprintf("CREATE TABLE %s (id INT, qty INT) ON %s(qty)", table, ps),
					},
					[]string{"1", "2"},
					[]string{
						"IF OBJECT_ID('" + table + "', 'U') IS NOT NULL DROP TABLE " + table,
						fmt.Sprintf("IF EXISTS (SELECT 1 FROM sys.partition_schemes WHERE name = '%s') DROP PARTITION SCHEME %s", ps, ps),
						fmt.Sprintf("IF EXISTS (SELECT 1 FROM sys.partition_functions WHERE name = '%s') DROP PARTITION FUNCTION %s", pf, pf),
					}
			},
			dropView: func(v string) []string {
				return []string{fmt.Sprintf("IF OBJECT_ID('%s', 'V') IS NOT NULL DROP VIEW %s", v, v)}
			},
			dropSeq: func(n string) []string {
				return []string{fmt.Sprintf("IF OBJECT_ID('%s', 'SO') IS NOT NULL DROP SEQUENCE %s", n, n)}
			},
			dropRoutine: func(n string) []string {
				return []string{fmt.Sprintf("IF OBJECT_ID('%s', 'P') IS NOT NULL DROP PROCEDURE %s", n, n)}
			},
		}
	case dbcore.KindOracle:
		return fixtures{
			childTable: func(child, parent, fk, ux, ix string) []string {
				return []string{
					fmt.Sprintf("CREATE TABLE %s (id NUMBER PRIMARY KEY, pid NUMBER, tag VARCHAR2(64), CONSTRAINT %s FOREIGN KEY (pid) REFERENCES %s(id) ON DELETE CASCADE)", child, fk, parent),
					fmt.Sprintf("CREATE UNIQUE INDEX %s ON %s (tag)", ux, child),
					fmt.Sprintf("CREATE INDEX %s ON %s (pid, tag)", ix, child),
				}
			},
			view: func(view, table string) string {
				return fmt.Sprintf("CREATE VIEW %s AS SELECT id, name FROM %s", view, table)
			},
			checkTable: func(table, constraint string) []string {
				return []string{fmt.Sprintf("CREATE TABLE %s (id NUMBER, qty NUMBER, CONSTRAINT %s CHECK (qty > 0))", table, constraint)}
			},
			sequence: func(name string) []string {
				// Oracle keeps no START WITH in the data dictionary, so the
				// fixture pins MINVALUE to the start it asserts on (that is the
				// column ALL_SEQUENCES can report).
				return []string{fmt.Sprintf("CREATE SEQUENCE %s START WITH 5 MINVALUE 5 INCREMENT BY 3", name)}
			},
			routine: func(name string) []string {
				return []string{fmt.Sprintf("CREATE OR REPLACE PROCEDURE %s AS BEGIN NULL; END;", name)}
			},
			routineKind: dbcore.RoutineProcedure,
			partitioned: func(table string) ([]string, []string, []string) {
				return []string{fmt.Sprintf("CREATE TABLE %s (id NUMBER, qty NUMBER) PARTITION BY RANGE (qty) (PARTITION P0 VALUES LESS THAN (10), PARTITION PMAX VALUES LESS THAN (MAXVALUE))", table)},
					[]string{"P0", "PMAX"},
					[]string{oracleDrop("DROP TABLE " + table)}
			},
			dropView: func(v string) []string {
				return []string{oracleDrop("DROP VIEW " + v)}
			},
			dropSeq: func(n string) []string {
				return []string{oracleDrop("DROP SEQUENCE " + n)}
			},
			dropRoutine: func(n string) []string {
				return []string{oracleDrop("DROP PROCEDURE " + n)}
			},
		}
	default: // SQLite (and the portable baseline)
		return fixtures{
			childTable: func(child, parent, fk, ux, ix string) []string {
				return []string{
					fmt.Sprintf("CREATE TABLE %s (id INTEGER PRIMARY KEY, pid INTEGER, tag TEXT, CONSTRAINT %s FOREIGN KEY (pid) REFERENCES %s(id) ON DELETE CASCADE)", child, fk, parent),
					fmt.Sprintf("CREATE UNIQUE INDEX %s ON %s (tag)", ux, child),
					fmt.Sprintf("CREATE INDEX %s ON %s (pid, tag)", ix, child),
				}
			},
			view: func(view, table string) string {
				return fmt.Sprintf("CREATE VIEW %s AS SELECT id, name FROM %s", view, table)
			},
			dropView: func(v string) []string { return []string{"DROP VIEW IF EXISTS " + v} },
		}
	}
}

// oracleDrop wraps a DDL statement so a missing object is not an error (Oracle
// has no DROP ... IF EXISTS before 23c).
func oracleDrop(stmt string) string {
	return "BEGIN EXECUTE IMMEDIATE '" + strings.ReplaceAll(stmt, "'", "''") +
		"'; EXCEPTION WHEN OTHERS THEN NULL; END;"
}

// fkParentDDL is the FK target the child-table fixture references: one integer
// primary-key column, so the engine accepts the foreign key on every dialect
// (the shared contract table itself has no unique column).
func fkParentDDL(kind dbcore.DatabaseKind, name string) string {
	switch kind {
	case dbcore.KindMySQL, dbcore.KindMSSQL:
		return fmt.Sprintf("CREATE TABLE %s (id INT PRIMARY KEY)", name)
	case dbcore.KindOracle:
		return fmt.Sprintf("CREATE TABLE %s (id NUMBER PRIMARY KEY)", name)
	}
	return fmt.Sprintf("CREATE TABLE %s (id INTEGER PRIMARY KEY)", name)
}

// uniqueTableDDL is the unique-violation fixture: a primary key plus a UNIQUE
// NOT NULL text column. It cannot reuse Harness.CreateTable (that override has
// no unique column for SQL Server/Oracle), and TEXT is not indexable on MySQL,
// so the string type is per engine.
func uniqueTableDDL(kind dbcore.DatabaseKind, name string) string {
	switch kind {
	case dbcore.KindMSSQL:
		return fmt.Sprintf("CREATE TABLE [%s] (id INT PRIMARY KEY, tag NVARCHAR(255) NOT NULL UNIQUE)", name)
	case dbcore.KindOracle:
		return fmt.Sprintf(`CREATE TABLE "%s" (id NUMBER PRIMARY KEY, tag VARCHAR2(255) NOT NULL UNIQUE)`, name)
	}
	return fmt.Sprintf("CREATE TABLE %s (id INTEGER PRIMARY KEY, tag VARCHAR(255) NOT NULL UNIQUE)", quoteBare(kind, name))
}
