package dberror

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"testing"
)

// A pgx-style error carries SQLSTATE; classification must prefer it over any
// wording and hit every constraint class.
type fakePgError struct{ state string }

func (e fakePgError) Error() string { return "ERROR: " + e.state }
func (e fakePgError) SQLState() string { return e.state }

func TestClassifySQLState(t *testing.T) {
	cases := map[string]Code{
		"23505": UniqueViolation,
		"23503": ForeignKeyViolation,
		"23502": NotNullViolation,
		"23514": CheckViolation,
		"40P01": Deadlock,
		"40001": SerializationFailure,
		"08006": ConnectionBroken,
		"57014": Canceled,
		"HYT00": Timeout,
	}
	for state, want := range cases {
		if got := Classify(fakePgError{state: state}); got != want {
			t.Fatalf("SQLState %s = %s, want %s", state, got, want)
		}
	}
}

// Wrapped errors keep their class: the driver wording sits underneath layers
// the consumers add ("import: insert rows 3-4: ...").
func TestClassifyWrappedText(t *testing.T) {
	cases := []struct {
		msg  string
		want Code
	}{
		{"Error 1062: Duplicate entry 'a' for key 't.tag'", UniqueViolation},
		{"import: insert rows 1-2: Error 1062: Duplicate entry 'a' for key 'uk'", UniqueViolation},
		{"UNIQUE constraint failed: parts.name", UniqueViolation},
		{"mssql: Violation of UNIQUE KEY constraint 'uk_tag'. Cannot insert duplicate key", UniqueViolation},
		{"ORA-00001: unique constraint (APP.PK) violated", UniqueViolation},
		{"PRIMARY KEY constraint failed: parts.id", UniqueViolation},
		{"insert statement conflicted with the FOREIGN KEY constraint", ForeignKeyViolation},
		{"Error 1452: Cannot add or update a child row: a foreign key constraint fails", ForeignKeyViolation},
		{"ORA-02292: integrity constraint violated - child record found", ForeignKeyViolation},
		{"NOT NULL constraint failed: parts.name", NotNullViolation},
		{"Column 'name' cannot be null", NotNullViolation},
		{"CHECK constraint failed: parts", CheckViolation},
		{"Transaction (Process ID 51) was deadlocked on lock resources", Deadlock},
		{"could not serialize access due to concurrent update", SerializationFailure},
		{"driver: bad connection", ConnectionBroken},
		{"dial tcp 127.0.0.1:5432: connect: connection refused", ConnectionBroken},
		{"ORA-03113: end-of-file on communication channel", ConnectionBroken},
	}
	for _, c := range cases {
		wrapped := fmt.Errorf("run: %w", errors.New(c.msg))
		if got := Classify(wrapped); got != c.want {
			t.Fatalf("Classify(%q) = %s, want %s", c.msg, got, c.want)
		}
	}
}

func TestClassifyContextAndBadConn(t *testing.T) {
	if got := Classify(context.DeadlineExceeded); got != Timeout {
		t.Fatalf("deadline = %s", got)
	}
	if got := Classify(fmt.Errorf("query: %w", context.Canceled)); got != Canceled {
		t.Fatalf("canceled = %s", got)
	}
	if got := Classify(fmt.Errorf("scan: %w", driver.ErrBadConn)); got != ConnectionBroken {
		t.Fatalf("bad conn = %s", got)
	}
	if got := Classify(errors.New("totally alien wording")); got != Unknown {
		t.Fatalf("alien = %s", got)
	}
	if got := Classify(nil); got != Unknown {
		t.Fatalf("nil = %s", got)
	}
}

// Specificity: a deadlock that also mentions a constraint classifies as a
// deadlock, not as a constraint violation.
func TestDeadlockBeatsConstraintWording(t *testing.T) {
	err := errors.New("ORA-00060: deadlock detected while waiting for ORA-00001")
	if got := Classify(err); got != Deadlock {
		t.Fatalf("got %s, want deadlock", got)
	}
}
