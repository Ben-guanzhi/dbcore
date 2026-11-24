package sqltext

// programobjects.go — the catalog recipes for the object families only the
// Oracle-family engines have: PL/SQL packages, scheduled jobs and synonyms.
//
// Oracle and DM8 share the ALL_* data dictionary in shape but not always in
// content: DM8 keeps the classic DBMS_JOB catalog (DBA_JOBS) rather than
// Oracle's scheduler views, so the job recipe differs even though the packages
// and synonyms ones do not.

import "fmt"

// PackagesQuery lists a schema's packages with which halves exist and whether
// anything is INVALID.
//
// A package is two catalog objects (the specification and the body), and the
// grouping is what turns them into one row. The status must be INVALID when
// EITHER half is, which is why it is an explicit CASE over a MAX of the
// invalid flag rather than a MAX over the status text: "INVALID" < "VALID"
// lexically today, but a query whose correctness depends on that would break
// the day the engine renames a status.
func PackagesQuery(dbType, schema string) (string, []interface{}, error) {
	switch Normalize(dbType) {
	case Oracle, DM:
		return fmt.Sprintf(`SELECT OBJECT_NAME,
				CASE WHEN MAX(CASE WHEN OBJECT_TYPE = 'PACKAGE' THEN 1 ELSE 0 END) = 1 THEN 1 ELSE 0 END,
				CASE WHEN MAX(CASE WHEN OBJECT_TYPE = 'PACKAGE BODY' THEN 1 ELSE 0 END) = 1 THEN 1 ELSE 0 END,
				CASE WHEN MAX(CASE WHEN STATUS = 'INVALID' THEN 1 ELSE 0 END) = 1 THEN 'INVALID' ELSE 'VALID' END
			FROM ALL_OBJECTS
			WHERE OWNER = %s AND OBJECT_TYPE IN ('PACKAGE', 'PACKAGE BODY')
			GROUP BY OBJECT_NAME
			ORDER BY OBJECT_NAME`,
			QuotedLiteral(Oracle, upperASCII(schema))), nil, nil
	}
	return "", nil, fmt.Errorf("sqltext: engine %q has no packages", dbType)
}

// PackageDefinitionQuery reads one half of a package's source, line by line.
//
// part is the ALL_OBJECTS type — "PACKAGE" for the specification and
// "PACKAGE BODY" for the body. The caller asks for each half separately
// because a package may legitimately have only one of them.
func PackageDefinitionQuery(dbType, schema, name, part string) (string, []interface{}, error) {
	switch Normalize(dbType) {
	case Oracle, DM:
		return fmt.Sprintf(`SELECT TEXT FROM ALL_SOURCE
			WHERE OWNER = %s AND NAME = %s AND TYPE = %s
			ORDER BY LINE`,
			QuotedLiteral(Oracle, upperASCII(schema)),
			QuotedLiteral(Oracle, upperASCII(name)),
			QuotedLiteral(Oracle, part)), nil, nil
	}
	return "", nil, fmt.Errorf("sqltext: engine %q has no package source", dbType)
}

// JobsQuery lists a schema's scheduled jobs.
//
// Oracle's scheduler views are the superset: since 10g the classic DBMS_JOB
// entries appear there too, so one recipe covers both generations.
//
// DM8 has no recipe on purpose. A live 20241022 instance answered "invalid
// table or view" for every candidate — DBA_JOBS, ALL_JOBS, USER_JOBS and all
// three scheduler views — and its ALL_OBJECTS carries no JOB type at all. A
// recipe that cannot run is worse than no recipe, so the DM driver does not
// claim JobReader and the client says the concept is unavailable there.
func JobsQuery(dbType, schema string) (string, []interface{}, error) {
	switch Normalize(dbType) {
	case Oracle:
		return fmt.Sprintf(`SELECT JOB_NAME, JOB_TYPE, ENABLED,
				COALESCE(TO_CHAR(NEXT_RUN_DATE, 'YYYY-MM-DD HH24:MI:SS'), '')
			FROM ALL_SCHEDULER_JOBS
			WHERE OWNER = %s
			ORDER BY JOB_NAME`,
			QuotedLiteral(Oracle, upperASCII(schema))), nil, nil
	}
	return "", nil, fmt.Errorf("sqltext: engine %q has no job list", dbType)
}

// SynonymsQuery lists the synonyms visible from one schema: its own plus the
// public ones, which is what name resolution would see.
//
// The target's object type is not in ALL_SYNONYMS, so it comes from a LEFT
// JOIN against ALL_OBJECTS: a synonym pointing at a dropped object (or at a
// database link) reports an empty type instead of hiding the row.
func SynonymsQuery(dbType, schema string) (string, []interface{}, error) {
	switch Normalize(dbType) {
	case Oracle, DM:
		return fmt.Sprintf(`SELECT s.SYNONYM_NAME, s.OWNER, s.TABLE_OWNER, s.TABLE_NAME,
				COALESCE(o.OBJECT_TYPE, '')
			FROM ALL_SYNONYMS s
			LEFT JOIN ALL_OBJECTS o
				ON o.OWNER = s.TABLE_OWNER AND o.OBJECT_NAME = s.TABLE_NAME
			WHERE s.OWNER = %s OR s.OWNER = 'PUBLIC'
			ORDER BY s.OWNER, s.SYNONYM_NAME`,
			QuotedLiteral(Oracle, upperASCII(schema))), nil, nil
	}
	return "", nil, fmt.Errorf("sqltext: engine %q has no synonyms", dbType)
}
