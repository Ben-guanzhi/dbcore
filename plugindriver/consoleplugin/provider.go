package main

// The provider seam: one engine's console. The protocol dispatch (main.go) is generic;
// everything that knows about collections, topics, subjects or configs lives behind
// this interface, which is what makes adding an engine a matter of writing one file.

import (
	"context"
	"fmt"
	"sync"

	"github.com/dbcore/dbcore/plugindriver"
)

// Session is one open console connection. The methods mirror the kernel's
// connection-oriented protocol, minus the SQL-specific ones a console does not have.
type Session interface {
	// Objects lists the console's top-level objects — collections, topics, subjects,
	// config pairs — as a flat list. The name encodes any namespace ("db/collection").
	Objects(ctx context.Context) ([]plugindriver.ConnObject, error)
	// Columns describes one object's shape: a collection's fields, a topic's
	// partitions, a config's metadata.
	Columns(ctx context.Context, object string) ([]plugindriver.ConnColumn, error)
	// Browse reads up to limit records of one object into a query-shaped result.
	Browse(ctx context.Context, object string, limit int) (*plugindriver.ConnQueryResult, error)
	// Actions declares the console actions this session offers (a search, a consume,
	// a publish). A session may tailor these to what it connected to.
	Actions() []plugindriver.ConnAction
	// RunAction runs one declared action. args are keyed by the action's param Name.
	RunAction(ctx context.Context, action string, args map[string]any) (*plugindriver.ConnQueryResult, error)
	Close() error
}

// Provider opens sessions for one engine. It is the identity the banner reports.
type Provider interface {
	Kind() string
	Name() string
	Open(ctx context.Context, p plugindriver.ConnectParams) (Session, error)
}

// providers is the registry the argv selects from.
var (
	provMu       sync.RWMutex
	providerList = []Provider{}
)

func registerProvider(p Provider) {
	provMu.Lock()
	defer provMu.Unlock()
	providerList = append(providerList, p)
}

// LookupProvider finds a provider by its engine kind.
func LookupProvider(kind string) (Provider, bool) {
	provMu.RLock()
	defer provMu.RUnlock()
	for _, p := range providerList {
		if p.Kind() == kind {
			return p, true
		}
	}
	return nil, false
}

// ProviderNames lists the registered kinds, for the startup error message.
func ProviderNames() []string {
	provMu.RLock()
	defer provMu.RUnlock()
	out := make([]string, 0, len(providerList))
	for _, p := range providerList {
		out = append(out, p.Kind())
	}
	return out
}

// ---------- result helpers ----------

// text/int/bool/float/null build the tagged cells the protocol carries. A console's
// values are mostly text (JSON bodies, IDs, topic names) and ints (offsets, counts),
// with the occasional bool for a primary-key or partition flag.
func textCell(s string) plugindriver.CellPayload { return plugindriver.CellPayload{Kind: "text", Text: s} }
func intCell(i int64) plugindriver.CellPayload   { return plugindriver.CellPayload{Kind: "int", Int: i} }
func floatCell(f float64) plugindriver.CellPayload {
	return plugindriver.CellPayload{Kind: "float", Flt: f}
}
func boolCell(b bool) plugindriver.CellPayload { return plugindriver.CellPayload{Kind: "bool", Bool: b} }

// textCol is the common column builder: name + a display type.
func textCol(name string) plugindriver.ConnColumn {
	return plugindriver.ConnColumn{Name: name, Type: "TEXT"}
}

// result assembles a query-shaped result from columns and rows of cells. The host
// renders it in the ordinary grid, so a console's output needs no special UI.
func result(cols []plugindriver.ConnColumn, rows [][]plugindriver.CellPayload) *plugindriver.ConnQueryResult {
	out := &plugindriver.ConnQueryResult{Columns: cols}
	for _, r := range rows {
		cells := make([]any, len(r))
		for i, c := range r {
			cells[i] = c
		}
		out.Rows = append(out.Rows, cells)
	}
	return out
}

// argString/argInt pull typed values out of an action's args map. JSON decodes numbers
// as float64, so argInt accepts both float64 and a numeric string — a console's params
// arrive from a form, where a user may type "10" or the UI may send 10.
func argString(args map[string]any, name string) string {
	if v, ok := args[name]; ok {
		switch t := v.(type) {
		case string:
			return t
		case fmt.Stringer:
			return t.String()
		default:
			return fmt.Sprintf("%v", v)
		}
	}
	return ""
}

func argInt(args map[string]any, name string, def int64) int64 {
	v, ok := args[name]
	if !ok {
		return def
	}
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case string:
		var n int64
		if _, err := fmt.Sscanf(t, "%d", &n); err == nil {
			return n
		}
	}
	return def
}
