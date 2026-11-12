// consoleplugin is a reference plugin that exposes NON-SQL consoles — vector
// databases and message queues — through the kernel's generic console-action
// protocol (plugindriver.MethodConnListActions / MethodConnRunAction).
//
// The point is the split of knowledge:
//
//	the HOST (TableProGo) is a generic console runner. It lists the actions a
//	  plugin declares and runs them by ID, rendering whatever comes back in the
//	  ordinary grid. It knows nothing about vectors, topics, subjects or configs.
//	the PLUGIN owns every piece of domain knowledge: how to list collections, what
//	  a similarity search is, how to consume a topic, how to publish a config.
//
// One binary serves several engines. The engine is chosen by the first argv, so the
// user registers the same executable under several types:
//
//	TABLEPRO_PLUGIN_MILVUS=/path/to/consoleplugin;milvus
//	TABLEPRO_PLUGIN_KAFKA=/path/to/consoleplugin;kafka
//	TABLEPRO_PLUGIN_PULSAR=/path/to/consoleplugin;pulsar
//	TABLEPRO_PLUGIN_NACOS=/path/to/consoleplugin;nacos
//
// The banner's Kind must agree with the registered type, which is why the provider is
// resolved before the banner is printed.
//
// A console is not a SQL engine, so the SQL surface is answered honestly rather than
// faked: Execute reports that the engine has no SQL, and the real surface is the object
// tree (databases → objects → columns → browse) plus the console actions. Browsing a
// collection or a topic works through the normal grid; a similarity search or a consume
// runs as a console action.
//
// The object tree is FLAT on purpose. A console has no schema/table two-level nesting
// the way a SQL catalog does, so Objects() returns one list and an object's name encodes
// its namespace ("tenant/namespace/topic", "database/collection"). The host's flat tree
// renders that unchanged.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/plugindriver"
)

func main() {
	name := "nacos"
	if len(os.Args) > 1 {
		name = os.Args[1]
	}
	prov, ok := LookupProvider(name)
	if !ok {
		fmt.Fprintf(os.Stderr, "consoleplugin: unknown provider %q (want one of %v)\n", name, ProviderNames())
		os.Exit(2)
	}

	out := bufio.NewWriter(os.Stdout)
	// The banner is the protocol's contract: the host verifies Proto/Version/Kind
	// before it sends anything, so a stale binary fails at startup with a clear
	// message rather than at the first query.
	banner, _ := json.Marshal(plugindriver.Banner{
		Proto:   plugindriver.ProtocolName,
		Version: plugindriver.ProtocolVersion,
		Kind:    prov.Kind(),
		Name:    prov.Name(),
	})
	fmt.Fprintf(out, "%s\n", banner)
	out.Flush()

	srv := &server{prov: prov, sessions: map[string]Session{}}
	in := bufio.NewReader(os.Stdin)
	var wmu sync.Mutex
	for {
		line, err := in.ReadBytes('\n')
		if len(line) > 0 {
			var req plugindriver.Request
			if json.Unmarshal(line, &req) == nil {
				// Each frame is served in its own goroutine so a long-running
				// consume or search cannot block a later cancel.
				go func(req plugindriver.Request) { srv.handle(out, &wmu, req) }(req)
			}
		}
		if err != nil {
			return
		}
	}
}

// server holds the open console sessions, addressed by conn id — the same shape the
// kernel's ConnDriver assumes.
type server struct {
	prov     Provider
	sessions map[string]Session
}

func (s *server) handle(out *bufio.Writer, wmu *sync.Mutex, req plugindriver.Request) {
	send := func(result any, errText, code string) {
		payload, _ := json.Marshal(result)
		resp := plugindriver.Response{ID: req.ID, Result: payload, Error: errText, Code: code}
		frame, _ := json.Marshal(resp)
		wmu.Lock()
		fmt.Fprintf(out, "%s\n", frame)
		out.Flush()
		wmu.Unlock()
	}
	fail := func(err error) {
		// A provider error carries its own code when it is a dbcore.Error; otherwise
		// a generic plugin code. Either way the host surfaces the message.
		code := "ERR_PLUGIN"
		var de *dbcore.Error
		if errors.As(err, &de) {
			code = de.Code
		}
		send(nil, err.Error(), code)
	}

	ctx := context.Background()
	switch req.Method {
	case plugindriver.MethodPing:
		send("pong", "", "")
	case plugindriver.MethodOpen:
		var p plugindriver.ConnectParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			send(nil, "bad open params: "+err.Error(), "ERR_INVALID_ARG")
			return
		}
		sess, err := s.prov.Open(ctx, p)
		if err != nil {
			fail(err)
			return
		}
		id := fmt.Sprintf("c%d", len(s.sessions)+1)
		s.sessions[id] = sess
		send(plugindriver.OpenResult{ConnID: id, Server: s.prov.Name()}, "", "")
	case plugindriver.MethodClose:
		for _, sess := range s.sessions {
			_ = sess.Close()
		}
		s.sessions = map[string]Session{}
		send(nil, "", "")
		os.Exit(0)
	case plugindriver.MethodCloseConn:
		var p plugindriver.ConnRef
		_ = json.Unmarshal(req.Params, &p)
		if sess, ok := s.sessions[p.ConnID]; ok {
			_ = sess.Close()
			delete(s.sessions, p.ConnID)
		}
		send(nil, "", "")
	case plugindriver.MethodConnCancel:
		// Consoles have no long statement to reach from here: browse and actions are
		// short and bounded. Acknowledge so the host does not wait.
		send(nil, "", "")
	case "tables", plugindriver.MethodListConnTables:
		sess, _, ok := s.sessionOf(req, send)
		if !ok {
			return
		}
		objs, err := sess.Objects(ctx)
		if err != nil {
			fail(err)
			return
		}
		send(objs, "", "")
	case "columns", plugindriver.MethodListConnColumns:
		sess, p, ok := s.sessionOf(req, send)
		if !ok {
			return
		}
		var t plugindriver.ConnTableRef
		_ = json.Unmarshal(req.Params, &t)
		name := t.Table
		if name == "" {
			name = p.ConnID
		}
		cols, err := sess.Columns(ctx, name)
		if err != nil {
			fail(err)
			return
		}
		send(cols, "", "")
	case "browse", plugindriver.MethodConnBrowse:
		sess, _, ok := s.sessionOf(req, send)
		if !ok {
			return
		}
		var bp plugindriver.ConnBrowseParams
		_ = json.Unmarshal(req.Params, &bp)
		limit := bp.Limit
		if limit <= 0 {
			limit = 100
		}
		res, err := sess.Browse(ctx, bp.Table, limit)
		if err != nil {
			fail(err)
			return
		}
		send(res, "", "")
	case "query", plugindriver.MethodConnQuery:
		// A console has no SQL. Saying so beats a plugin that pretends and returns an
		// empty grid the user cannot tell from "no rows".
		send(nil, "此引擎不支持 SQL；请使用对象树浏览或控制台动作", dbcore.ErrCodeNotSupported)
	case plugindriver.MethodConnListActions:
		sess, _, ok := s.sessionOf(req, send)
		if !ok {
			return
		}
		send(sess.Actions(), "", "")
	case plugindriver.MethodConnRunAction:
		sess, _, ok := s.sessionOf(req, send)
		if !ok {
			return
		}
		var rp plugindriver.ConnRunActionParams
		if err := json.Unmarshal(req.Params, &rp); err != nil {
			send(nil, "bad action params: "+err.Error(), "ERR_INVALID_ARG")
			return
		}
		res, err := sess.RunAction(ctx, rp.Action, rp.Args)
		if err != nil {
			fail(err)
			return
		}
		send(res, "", "")
	default:
		send(nil, "unknown method "+req.Method, dbcore.ErrCodeNotSupported)
	}
}

// sessionOf decodes the conn-id-bearing params and looks up the session, replying with
// an error and reporting false when it is missing.
func (s *server) sessionOf(
	req plugindriver.Request,
	send func(result any, errText, code string),
) (Session, plugindriver.ConnRef, bool) {
	var p plugindriver.ConnRef
	_ = json.Unmarshal(req.Params, &p)
	sess, ok := s.sessions[p.ConnID]
	if !ok {
		send(nil, "未知连接 "+p.ConnID, "ERR_INVALID_ARG")
		return nil, p, false
	}
	return sess, p, true
}
