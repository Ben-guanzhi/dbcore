package sqltext

import (
	"fmt"
	"strings"
)

// UpsertClause returns the engine-specific conflict clause for an INSERT in
// "upsert" mode. It is the single source of that clause for every client in the
// workspace; TableProGo's import layer and any future importer call this instead
// of re-deriving per-engine syntax.
//
// cols are the columns named in the INSERT (used to build the SET assignments);
// keys are the conflict target — the table's primary key or a unique column,
// each one of cols. Empty keys or a key outside cols is rejected so the caller
// never ships a clause the engine will reject with an opaque error.
//
// SQL Server and Oracle need a full MERGE statement (source values plus matched
// and not-matched branches); fabricating one here would be a second, untested
// write path, so those two report a clear error and the caller keeps append
// mode.
func UpsertClause(dbType string, cols, keys []string) (string, error) {
	if len(keys) == 0 {
		return "", fmt.Errorf("upsert needs a conflict target (the table's primary key or a unique column)")
	}
	known := make(map[string]bool, len(cols))
	for _, c := range cols {
		known[c] = true
	}
	quotedKeys := make([]string, 0, len(keys))
	for _, k := range keys {
		if !known[k] {
			return "", fmt.Errorf("upsert key %q is not one of the imported columns", k)
		}
		q, err := QuoteIdent(dbType, k)
		if err != nil {
			return "", err
		}
		quotedKeys = append(quotedKeys, q)
	}
	assignments := make([]string, 0, len(cols))
	switch Normalize(dbType) {
	case MySQL:
		// MySQL ignores the key list: any unique index can conflict.
		for _, c := range cols {
			q, err := QuoteIdent(dbType, c)
			if err != nil {
				return "", err
			}
			assignments = append(assignments, q+"=VALUES("+q+")")
		}
		return " ON DUPLICATE KEY UPDATE " + strings.Join(assignments, ", "), nil
	case Postgres, SQLite:
		for _, c := range cols {
			q, err := QuoteIdent(dbType, c)
			if err != nil {
				return "", err
			}
			assignments = append(assignments, q+"=excluded."+q)
		}
		return " ON CONFLICT (" + strings.Join(quotedKeys, ", ") + ") DO UPDATE SET " + strings.Join(assignments, ", "), nil
	case SQLServer, Oracle:
		return "", fmt.Errorf("%s does not support upsert import (needs MERGE); use append or truncate mode", Normalize(dbType))
	}
	return "", fmt.Errorf("upsert is not supported for %s", Normalize(dbType))
}
