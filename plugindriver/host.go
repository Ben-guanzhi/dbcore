package plugindriver

// Host-side additions for clients whose session surface is wider than the
// kernel's SQLDriver: raw Call, streaming CallStream, and spawn options
// (stderr capture, hidden window, banner timeout).
//
// fynequery's host rides these so the process plumbing lives in the kernel
// once: its UI consumes the connection-oriented shape (StartConn + Open +
// Bind), and the methods the kernel has no view of — transactions, DML
// batches, metrics, streaming queries — go through Call/CallStream with the
// connection id inside the params.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// StartOpts tunes how a plugin process is launched. The zero value matches
// Start: stderr discarded, window behaviour left to the OS, a 10s banner
// timeout.
type StartOpts struct {
	// Stderr receives the plugin's diagnostic output. exec copies it through a
	// pipe, so it may be any writer; nil discards.
	Stderr interface{ Write(p []byte) (int, error) }
	// HideWindow stops the plugin from flashing a console window on Windows.
	HideWindow bool
	// BannerTimeout bounds the wait for the startup banner. Zero means 10s: a
	// plugin that never banners must fail the launch, not hang it.
	BannerTimeout time.Duration
}

func (o StartOpts) bannerTimeout() time.Duration {
	if o.BannerTimeout > 0 {
		return o.BannerTimeout
	}
	return 10 * time.Second
}

// Call performs one raw request/response round trip in the plugin's own
// vocabulary — the connection-oriented names, or anything else the kernel has
// no method for. params may be nil; a non-nil result receives the reply's
// result field (a *json.RawMessage target captures the raw bytes).
func (d *Driver) Call(ctx context.Context, method string, params, result interface{}) error {
	raw, err := d.call(ctx, method, params)
	if err != nil {
		return err
	}
	if result == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, result)
}

// CallStream performs a streaming request: the plugin answers with several
// frames under one id, the last marked Done. Every non-terminal frame is
// decoded and handed to onFrame; a false return stops early (the stream is
// retired either way). The terminal frame's result, when the call ends
// normally, is decoded into final (which may be nil).
func (d *Driver) CallStream(
	ctx context.Context,
	method string,
	params interface{},
	onFrame func(StreamFrame) (bool, error),
	final interface{},
) error {
	if d.closed.Load() {
		return fmt.Errorf("plugindriver: driver is closed")
	}
	id := d.nextID.Add(1)
	var raw json.RawMessage
	if params != nil {
		encoded, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("plugindriver: encode %s: %w", method, err)
		}
		raw = encoded
	}
	frame, err := json.Marshal(Request{ID: id, Method: method, Params: raw})
	if err != nil {
		return fmt.Errorf("plugindriver: encode request: %w", err)
	}
	ch := make(chan Response, 8)
	d.mu.Lock()
	if d.closed.Load() {
		d.mu.Unlock()
		return fmt.Errorf("plugindriver: driver is closed")
	}
	d.streams[id] = ch
	d.mu.Unlock()

	stop := func() {
		d.mu.Lock()
		delete(d.streams, id)
		d.mu.Unlock()
	}
	defer stop()

	d.writeMu.Lock()
	_, writeErr := d.stdin.Write(append(frame, '\n'))
	d.writeMu.Unlock()
	if writeErr != nil {
		return fmt.Errorf("plugindriver: write %s: %w", method, writeErr)
	}

	for {
		select {
		case resp := <-ch:
			if resp.Error != "" {
				return pluginError(resp)
			}
			var f StreamFrame
			if len(resp.Result) > 0 {
				if jerr := json.Unmarshal(resp.Result, &f); jerr != nil {
					return jerr
				}
			}
			keep := true
			if !resp.Done && onFrame != nil {
				var e error
				keep, e = onFrame(f)
				if e != nil {
					return e
				}
			}
			if resp.Done || !keep {
				if resp.Done && final != nil && len(resp.Result) > 0 {
					return json.Unmarshal(resp.Result, final)
				}
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
