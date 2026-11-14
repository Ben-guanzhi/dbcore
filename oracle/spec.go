package oracle

import (
	"context"
	"database/sql"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/oracledsn"
)

func init() { dbcore.RegisterFactory(dbcore.KindOracle, OpenSpec) }

// DSN renders spec as an oracle:// URL understood by go-ora — the spelling
// lives in oracledsn so clients can render it without linking this driver.
func DSN(spec dbcore.ConnSpec) string { return oracledsn.DSN(spec) }
// OpenSpec implements dbcore.Factory. go-ora has no per-connection dialer hook;
// an SSH tunnel must be terminated by the caller and pointed at via Host/Port.
func OpenSpec(ctx context.Context, spec dbcore.ConnSpec, pool dbcore.PoolConfig) (dbcore.Driver, error) {
	if spec.Dialer != nil {
		return nil, &dbcore.Error{Code: dbcore.ErrCodeNotSupported,
			Message: "oracle: custom Dialer is not supported; terminate the SSH tunnel and set Host/Port instead"}
	}
	db, err := sql.Open(DriverName, DSN(spec))
	if err != nil {
		return nil, err
	}
	pool.Apply(db)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	d := &OracleDriver{db: db, dialect: &OracleDialect{}, owned: true}
	d.initWide()
	return d, nil
}
