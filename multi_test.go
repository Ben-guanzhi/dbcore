package dbcore

// Tests for reading several result sets from one call, with a fake
// database/sql driver that answers like SQL Server's batch or MySQL's stored
// procedure: two sets with columns, then an empty one for the DML in between.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"testing"
)

func init() {
	sql.Register("dbcore_multi_test", multiTestDriver{})
}

type multiTestDriver struct{}

func (multiTestDriver) Open(string) (driver.Conn, error) { return multiTestConn{}, nil }

type multiTestConn struct{}

func (multiTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("multiTestConn: prepared statements are not supported")
}
func (multiTestConn) Close() error              { return nil }
func (multiTestConn) Begin() (driver.Tx, error) { return nil, errors.New("multiTestConn: no tx") }

func (multiTestConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &multiTestRows{sets: []multiTestSet{
		{cols: []string{"id"}, rows: [][]driver.Value{{int64(1)}, {int64(2)}}},
		{cols: []string{"name"}, rows: [][]driver.Value{{"ada"}}},
		{cols: nil, rows: nil}, // the DML set: no columns, not a result
	}}, nil
}

type multiTestSet struct {
	cols []string
	rows [][]driver.Value
}

type multiTestRows struct {
	sets []multiTestSet
	at   int
	row  int
}

func (r *multiTestRows) Columns() []string { return r.sets[r.at].cols }
func (r *multiTestRows) Close() error      { return nil }

func (r *multiTestRows) Next(dest []driver.Value) error {
	set := r.sets[r.at]
	if r.row >= len(set.rows) {
		return io.EOF
	}
	copy(dest, set.rows[r.row])
	r.row++
	return nil
}

// HasNextResultSet and NextResultSet are what database/sql walks for the
// second and later sets.
func (r *multiTestRows) HasNextResultSet() bool { return r.at+1 < len(r.sets) }

func (r *multiTestRows) NextResultSet() error {
	if r.at+1 >= len(r.sets) {
		return io.EOF
	}
	r.at++
	r.row = 0
	return nil
}

// RunMulti must return the sets that have columns, in order, and skip the one
// that does not: a DML batch answers with an empty set that is not a result.
func TestRunMultiReadsEverySet(t *testing.T) {
	db, err := sql.Open("dbcore_multi_test", "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	sets, err := RunMulti(context.Background(), db, "SELECT 1; UPDATE t SET x=1; SELECT 2", nil, 0)
	if err != nil {
		t.Fatalf("RunMulti: %v", err)
	}
	if len(sets) != 2 {
		t.Fatalf("sets = %d, want 2 (the columnless set is not a result)", len(sets))
	}
	if sets[0].Columns[0].Name != "id" || len(sets[0].Rows) != 2 {
		t.Fatalf("first set = %+v, want id with two rows", sets[0].Columns)
	}
	if sets[0].Rows[1][0].I != 2 {
		t.Fatalf("first set's second row = %+v", sets[0].Rows[1][0])
	}
	if sets[1].Columns[0].Name != "name" || len(sets[1].Rows) != 1 {
		t.Fatalf("second set = %+v", sets[1].Columns)
	}
	if sets[1].Rows[0][0].S != "ada" {
		t.Fatalf("second set's row = %+v", sets[1].Rows[0][0])
	}
}

// Each set is bounded like a single result: the cap applies per set, not to the
// sum, or a many-set batch would silently truncate the later ones.
func TestRunMultiBoundsEachSet(t *testing.T) {
	db, err := sql.Open("dbcore_multi_test", "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	sets, err := RunMulti(context.Background(), db, "SELECT 1", nil, 1)
	if err != nil {
		t.Fatalf("RunMulti: %v", err)
	}
	if len(sets[0].Rows) != 1 || !sets[0].Truncated {
		t.Fatalf("set = %d rows truncated=%v, want one row marked truncated",
			len(sets[0].Rows), sets[0].Truncated)
	}
	if len(sets[1].Rows) != 1 {
		t.Fatalf("the second set was not bounded on its own: %d rows", len(sets[1].Rows))
	}
}

// AsMultiResult reports the capability a driver actually has.
func TestAsMultiResult(t *testing.T) {
	if _, ok := AsMultiResult(fakeMultiDriver{}); !ok {
		t.Fatal("a driver implementing MultiResultReader is not detected")
	}
	if _, ok := AsMultiResult(fakePlainDriver{}); ok {
		t.Fatal("a driver without ExecuteMulti is reported as multi-capable")
	}
}

type fakeMultiDriver struct{ Driver }

func (fakeMultiDriver) ExecuteMulti(context.Context, string, ...Value) ([]*QueryResult, error) {
	return nil, nil
}

type fakePlainDriver struct{ Driver }
