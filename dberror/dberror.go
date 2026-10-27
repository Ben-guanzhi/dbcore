// Package dberror classifies driver errors into stable codes, so consumers can
// react to *what happened* (duplicate key, dead server, deadlock) instead of
// string-matching engine dialects. Classification is lazy and dependency-free:
// no driver package is imported. Evidence comes from three sources, in order —
// context errors (errors.Is), a SQLState()-carrying error (pgx/lib/pq expose
// it), and the text of the whole error chain (every engine spells its own
// violations; the patterns below are the ones real servers emit).
package dberror

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
)

// Code is a stable, engine-independent classification.
type Code string

const (
	UniqueViolation      Code = "unique_violation"
	ForeignKeyViolation  Code = "foreign_key_violation"
	NotNullViolation     Code = "not_null_violation"
	CheckViolation       Code = "check_violation"
	Deadlock             Code = "deadlock"
	SerializationFailure Code = "serialization_failure"
	ConnectionBroken     Code = "connection_broken"
	Timeout              Code = "timeout"
	Canceled             Code = "canceled"
	Unknown              Code = "unknown"
)

// sqlStater is what error types with a SQLSTATE expose (pgx, lib/pq).
type sqlStater interface {
	SQLState() string
}

// Classify maps an error onto a Code. Never nil: unknown errors come back as
// Unknown, so callers switch without nil checks.
func Classify(err error) Code {
	if err == nil {
		return Unknown
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Timeout
	}
	if errors.Is(err, context.Canceled) {
		return Canceled
	}
	if errors.Is(err, driver.ErrBadConn) {
		return ConnectionBroken
	}

	var st sqlStater
	if errors.As(err, &st) {
		if code, ok := fromSQLState(st.SQLState()); ok {
			return code
		}
	}

	// The whole chain matters: a wrapper ("import: insert rows 3-4: ...")
	// carries the driver's wording underneath.
	var texts []string
	for e := err; e != nil; e = errors.Unwrap(e) {
		texts = append(texts, e.Error())
	}
	joined := strings.ToUpper(strings.Join(texts, "\n"))
	for _, p := range patterns {
		if p.match(joined) {
			return p.code
		}
	}
	return Unknown
}

// Convenience predicates.
func IsUniqueViolation(err error) bool      { return Classify(err) == UniqueViolation }
func IsForeignKeyViolation(err error) bool  { return Classify(err) == ForeignKeyViolation }
func IsNotNullViolation(err error) bool     { return Classify(err) == NotNullViolation }
func IsCheckViolation(err error) bool       { return Classify(err) == CheckViolation }
func IsDeadlock(err error) bool             { return Classify(err) == Deadlock }
func IsSerializationFailure(err error) bool { return Classify(err) == SerializationFailure }
func IsConnectionBroken(err error) bool     { return Classify(err) == ConnectionBroken }
func IsTimeout(err error) bool              { return Classify(err) == Timeout }
func IsCanceled(err error) bool             { return Classify(err) == Canceled }

// fromSQLState maps the SQLSTATE classes every engine agrees on. PostgreSQL
// exposes them directly; MySQL/MSSQL/Oracle errors rarely carry the interface,
// so their wording is handled by the text patterns.
func fromSQLState(state string) (Code, bool) {
	switch {
	case state == "23505":
		return UniqueViolation, true
	case state == "23503":
		return ForeignKeyViolation, true
	case state == "23502":
		return NotNullViolation, true
	case state == "23514":
		return CheckViolation, true
	case state == "40P01":
		return Deadlock, true
	case state == "40001" || state == "2D000":
		return SerializationFailure, true
	case strings.HasPrefix(state, "08"):
		return ConnectionBroken, true
	case state == "57014":
		return Canceled, true
	case state == "HYT00" || state == "HYT01":
		return Timeout, true
	}
	return Unknown, false
}

type pattern struct {
	code  Code
	words []string // every word must appear (order-free) in the upper-cased text
}

// patterns cover the wording each engine actually emits. Ordered most-specific
// first: a deadlock that also says "duplicate" must classify as a deadlock.
var patterns = []pattern{
	// Deadlock / serialization before generic constraint words.
	{Deadlock, []string{"DEADLOCK"}},                                           // PG 40P01, MySQL 1213, MSSQL 1205, Oracle ORA-00060
	{Deadlock, []string{"LOCK WAIT TIMEOUT"}},                                  // MySQL 1205
	{Deadlock, []string{"DATABASE IS LOCKED"}},                                 // SQLite busy
	{SerializationFailure, []string{"COULD NOT SERIALIZE"}},                    // PG
	{SerializationFailure, []string{"ORA-08177"}},                              // Oracle
	{SerializationFailure, []string{"SNAPSHOT ISOLATION TRANSACTION ABORTED"}}, // MSSQL RCSI conflict

	// Connection health.
	{ConnectionBroken, []string{"SERVER HAS GONE AWAY"}},         // MySQL 2006
	{ConnectionBroken, []string{"LOST CONNECTION"}},              // MySQL 2013
	{ConnectionBroken, []string{"CONNECTION REFUSED"}},           // dial failures, every engine
	{ConnectionBroken, []string{"SERVER CLOSED THE CONNECTION"}}, // pgx
	{ConnectionBroken, []string{"TERMINATING CONNECTION"}},       // PG
	{ConnectionBroken, []string{"ORA-03113"}},                    // Oracle end-of-file on channel
	{ConnectionBroken, []string{"ORA-03114"}},                    // Oracle not connected
	{ConnectionBroken, []string{"ORA-12545"}},                    // Oracle connect failed
	{ConnectionBroken, []string{"NOT LOGGED ON"}},                // Oracle ORA-01012
	{ConnectionBroken, []string{"BROKEN PIPE"}},                  // socket level
	{ConnectionBroken, []string{"BAD CONNECTION"}},               // driver.ErrBadConn wording

	// Constraint violations.
	{UniqueViolation, []string{"DUPLICATE ENTRY"}},                    // MySQL 1062
	{UniqueViolation, []string{"ERROR 1062"}},                         // MySQL, raw
	{UniqueViolation, []string{"UNIQUE CONSTRAINT"}},                  // PG wording, SQLite "UNIQUE constraint failed"
	{UniqueViolation, []string{"VIOLATION OF UNIQUE KEY"}},            // MSSQL 2627/2601
	{UniqueViolation, []string{"CANNOT INSERT DUPLICATE KEY"}},        // MSSQL 2601
	{UniqueViolation, []string{"ORA-00001"}},                          // Oracle unique
	{UniqueViolation, []string{"CONSTRAINT", "PRIMARY KEY"}},          // SQLite "PRIMARY KEY constraint failed", MSSQL PK
	{ForeignKeyViolation, []string{"FOREIGN KEY CONSTRAINT"}},         // PG/SQLite/MSSQL
	{ForeignKeyViolation, []string{"ERROR 1451"}},                     // MySQL delete parent
	{ForeignKeyViolation, []string{"ERROR 1452"}},                     // MySQL insert child
	{ForeignKeyViolation, []string{"A FOREIGN KEY CONSTRAINT FAILS"}}, // MySQL wording
	{ForeignKeyViolation, []string{"ORA-02292"}},                      // Oracle child record found
	{ForeignKeyViolation, []string{"ORA-02270"}},                      // Oracle no matching parent
	{NotNullViolation, []string{"NOT NULL CONSTRAINT"}},               // SQLite
	{NotNullViolation, []string{"NULL VALUE IN COLUMN"}},              // PG 23502 wording
	{NotNullViolation, []string{"CANNOT BE NULL"}},                    // MySQL 1048 / MSSQL
	{NotNullViolation, []string{"INSERT THE VALUE NULL"}},             // MSSQL
	{NotNullViolation, []string{"ORA-01400"}},                         // Oracle cannot insert NULL
	{CheckViolation, []string{"CHECK CONSTRAINT"}},                    // SQLite/PG/MSSQL
	{CheckViolation, []string{"CHECK CONSTRAINT IS VIOLATED"}},        // MySQL 3819
	{CheckViolation, []string{"ORA-02290"}},                           // Oracle
}

func (p pattern) match(text string) bool {
	for _, w := range p.words {
		if !strings.Contains(text, w) {
			return false
		}
	}
	return true
}
