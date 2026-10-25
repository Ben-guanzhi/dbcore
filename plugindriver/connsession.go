package plugindriver

// The wide session on a bound connection.
//
// Everything here used to live in one client only: fynequery drove its plugin
// sessions' transactions, row edits, metrics and streaming through the kernel's
// raw Call/CallStream escape hatch, spelling the methods in its own private
// vocabulary. A second client wanting the same abilities had to re-implement
// every call.
//
// The calls are now part of the kernel's typed surface (dbcore's SessionTx /
// RowWriter / BatchWriter / MetricsSource / StreamQuerier / DatabaseCreator),
// spelled in the kernel's own method names (connproto.go) with the deployed
// plugins' names as the fallback — the same one-plugin-either-vocabulary rule
// the rest of this package already follows.

import (
	"context"
	"encoding/json"

	"github.com/dbcore/dbcore"
)

// A bound connection is the whole wide session, so a caller that holds one can
// assert any part of the surface without another lookup.
var _ dbcore.WideSession = (*Conn)(nil)

// ---- manual transactions ---------------------------------------------------

// BeginTx starts a transaction on this connection. The plugin owns it; the
// kernel only drives it, and mirrors the state so a caller can ask without a
// round trip.
func (c *Conn) BeginTx(ctx context.Context) error {
	if _, err := c.driver.callConn(ctx, connMethodBegin, ConnRef{ConnID: c.connID}); err != nil {
		return err
	}
	c.driver.setInTx(c.connID, true)
	return nil
}

// CommitTx commits the transaction this connection holds.
func (c *Conn) CommitTx(ctx context.Context) error {
	if _, err := c.driver.callConn(ctx, connMethodCommit, ConnRef{ConnID: c.connID}); err != nil {
		return err
	}
	c.driver.setInTx(c.connID, false)
	return nil
}

// RollbackTx discards the transaction this connection holds.
func (c *Conn) RollbackTx(ctx context.Context) error {
	if _, err := c.driver.callConn(ctx, connMethodRollback, ConnRef{ConnID: c.connID}); err != nil {
		return err
	}
	c.driver.setInTx(c.connID, false)
	return nil
}

// InTransaction reports the mirrored transaction state. The plugin is the
// source of truth; this is the client's view of it.
func (c *Conn) InTransaction() bool { return c.driver.inTxOf(c.connID) }

// ---- row-level DML ---------------------------------------------------------

// UpdateRow changes one row, addressed by its primary key.
func (c *Conn) UpdateRow(ctx context.Context, table string, pk, changes map[string]dbcore.Value) error {
	_, err := c.driver.callConn(ctx, connMethodUpdate, ConnDMLParams{
		ConnID:  c.connID,
		Table:   table,
		PK:      taggedCells(pk),
		Changes: taggedCells(changes),
	})
	return err
}

// InsertRow adds one row.
func (c *Conn) InsertRow(ctx context.Context, table string, values map[string]dbcore.Value) error {
	_, err := c.driver.callConn(ctx, connMethodInsert, ConnDMLParams{
		ConnID: c.connID,
		Table:  table,
		Values: taggedCells(values),
	})
	return err
}

// DeleteRow removes one row, addressed by its primary key.
func (c *Conn) DeleteRow(ctx context.Context, table string, pk map[string]dbcore.Value) error {
	_, err := c.driver.callConn(ctx, connMethodDelete, ConnDMLParams{
		ConnID: c.connID,
		Table:  table,
		PK:     taggedCells(pk),
	})
	return err
}

// ---- batch DML -------------------------------------------------------------

// ApplyBatch applies a whole commit inside one transaction on the plugin side,
// with the same affect-exactly-one-row guarantee a row-at-a-time caller gets.
func (c *Conn) ApplyBatch(ctx context.Context, req dbcore.BatchRequest) error {
	updates := make([]ConnDMLRow, 0, len(req.Updates))
	for _, u := range req.Updates {
		updates = append(updates, ConnDMLRow{PK: taggedCells(u.PK), Changes: taggedCells(u.Changes)})
	}
	deletes := make([]map[string]CellPayload, 0, len(req.Deletes))
	for _, d := range req.Deletes {
		deletes = append(deletes, taggedCells(d))
	}
	inserts := make([]map[string]CellPayload, 0, len(req.Inserts))
	for _, i := range req.Inserts {
		inserts = append(inserts, taggedCells(i))
	}
	_, err := c.driver.callConn(ctx, connMethodApplyBatch, ConnDMLBatchParams{
		ConnID:  c.connID,
		Table:   req.Table,
		Updates: updates,
		Deletes: deletes,
		Inserts: inserts,
		DDL:     req.DDL,
	})
	return err
}

// ---- server metrics --------------------------------------------------------

// ServerMetrics reads the connection's server metrics, reusing the open
// connection rather than opening one to ask.
func (c *Conn) ServerMetrics(ctx context.Context) ([]dbcore.Metric, error) {
	raw, err := c.driver.callConn(ctx, connMethodMetrics, ConnRef{ConnID: c.connID})
	if err != nil {
		return nil, err
	}
	var payload ConnMetricsResult
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	out := make([]dbcore.Metric, 0, len(payload.Metrics))
	for _, m := range payload.Metrics {
		out = append(out, dbcore.Metric{Label: m.Label, Value: m.Value, Group: m.Group})
	}
	return out, nil
}

// ---- streaming -------------------------------------------------------------

// QueryStream reads a statement's rows in batches, handing each to emit as it
// arrives so a large result is never held whole. The terminal frame carries the
// aggregate counters, which come back in the result.
func (c *Conn) QueryStream(
	ctx context.Context,
	query string,
	batch int,
	emit func(dbcore.StreamBatch) (bool, error),
) (*dbcore.StreamResult, error) {
	var final ConnQueryResult
	err := c.driver.callStreamConn(ctx, connMethodQueryStream,
		ConnStreamParams{ConnID: c.connID, SQL: query, Batch: batch},
		func(f StreamFrame) (bool, error) {
			batch, err := streamBatchOf(f)
			if err != nil {
				return false, err
			}
			return emit(batch)
		}, &final)
	if err != nil {
		return nil, err
	}
	out := &dbcore.StreamResult{AffectedRows: final.RowsAffected, DurationMS: final.DurationMS}
	for _, col := range final.Columns {
		// The terminal frame names a primary key the fynequery way ("PK"); the
		// streamed frames use either spelling (see isPrimaryColumn).
		out.Columns = append(out.Columns, dbcore.ColumnInfo{
			Name:         col.Name,
			Type:         col.Type,
			Nullable:     col.Nullable,
			IsPrimaryKey: col.Key == "PK",
		})
	}
	return out, nil
}

// ---- databases -------------------------------------------------------------

// CreateDatabase creates a database or schema on the server, reusing the open
// connection (postgres/mssql create databases from any open connection).
func (c *Conn) CreateDatabase(ctx context.Context, name string, opts map[string]string) error {
	_, err := c.driver.callConn(ctx, connMethodCreateDB, ConnCreateDBParams{
		ConnID:    c.connID,
		Name:      name,
		Charset:   opts["charset"],
		Collation: opts["collation"],
	})
	return err
}

// ---- wire shapes -----------------------------------------------------------

// taggedCells renders the kernel's values as the tagged cells the wire carries,
// so a big integer stays an integer and a NULL stays a NULL.
func taggedCells(values map[string]dbcore.Value) map[string]CellPayload {
	if values == nil {
		return nil
	}
	out := make(map[string]CellPayload, len(values))
	for name, v := range values {
		out[name] = EncodeValue(v)
	}
	return out
}

// streamBatchOf renders one streamed frame in the kernel's shape. The primary
// key is read in whichever spelling the plugin sent it (isPrimaryKey or key).
func streamBatchOf(f StreamFrame) (dbcore.StreamBatch, error) {
	out := dbcore.StreamBatch{}
	for _, col := range f.Columns {
		out.Columns = append(out.Columns, dbcore.ColumnInfo{
			Name:         col.Name,
			Type:         col.Type,
			Nullable:     col.Nullable,
			IsPrimaryKey: isPrimaryColumn(col),
		})
	}
	for _, row := range f.Rows {
		cells := make([]dbcore.Value, len(row))
		for i, cell := range row {
			v, err := DecodeValue(cell)
			if err != nil {
				return dbcore.StreamBatch{}, err
			}
			cells[i] = v
		}
		out.Rows = append(out.Rows, cells)
	}
	return out, nil
}
