package main

// The console plugin's tests.
//
// The REST providers (Nacos, Milvus, Pulsar) are tested against a real httptest server,
// so the provider cannot tell the difference from the live API — including the fact that
// a non-2xx must surface as an error rather than an empty grid. Kafka is tested through
// injected interfaces, because the mapping from a message to a grid row is what matters
// and no broker is needed to prove it.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/plugindriver"
)

// ---------- Nacos ----------

func TestNacosConsole(t *testing.T) {
	var published string
	var lastForm http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			q := r.URL.Query()
			if q.Get("pageNo") != "" {
				// The list call, which also sends an EMPTY dataId — so branch on
				// pageNo, not on the mere presence of the key.
				// Real Nacos v2.3.2 list shape (verified against a standalone instance):
				// the page items carry many fields, and "type" is null for most configs.
				// A decoder that assumed a non-null type used to break on this.
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"totalCount":1,"pageNumber":1,"pagesAvailable":1,"pageItems":[{"id":"1089366333","dataId":"app.json","group":"APP","content":null,"md5":null,"encryptedDataKey":"","tenant":"","appName":"","type":null}]}`)
				return
			}
			if q.Get("dataId") != "" {
				fmt.Fprint(w, "hello-content")
				return
			}
			http.Error(w, "unexpected", http.StatusBadRequest)
		case http.MethodPost:
			lastForm = r.Header
			_ = r.ParseForm()
			published = r.FormValue("content")
			fmt.Fprint(w, "true")
		}
	}))
	defer srv.Close()

	prov := nacosProvider{}
	sess, err := prov.Open(context.Background(), plugindriver.ConnectParams{
		Options: map[string]string{"baseUrl": srv.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// The object tree lists configs flat as "group/dataId".
	objs, err := sess.Objects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 || objs[0].Name != "APP/app.json" {
		t.Fatalf("objects = %+v", objs)
	}

	// Browse splits the name on the FIRST slash, so a dataId that itself contains a
	// slash survives intact.
	br, err := sess.Browse(context.Background(), "APP/app.json", 10)
	if err != nil {
		t.Fatal(err)
	}
	if asCell(br.Rows[0][0]).Text != "APP" || asCell(br.Rows[0][2]).Text != "hello-content" {
		t.Fatalf("browse row = %+v", br.Rows[0])
	}

	// query-config action.
	qr, err := sess.RunAction(context.Background(), "query-config", map[string]any{"dataId": "app.json", "group": "APP"})
	if err != nil {
		t.Fatal(err)
	}
	if asCell(qr.Rows[0][2]).Text != "hello-content" {
		t.Fatalf("query-config = %+v", qr.Rows[0])
	}
	if len(qr.Rows[0]) != 3 {
		t.Fatalf("query-config must have 3 columns (group/dataId/content), got %d", len(qr.Rows[0]))
	}

	// publish-config action writes remote state; the fake proves the form arrives.
	pr, err := sess.RunAction(context.Background(), "publish-config", map[string]any{
		"dataId": "app.json", "group": "APP", "content": "new", "type": "json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if asCell(pr.Rows[0][0]).Text != "发布成功" {
		t.Fatalf("publish result = %+v", pr.Rows[0])
	}
	if published != "new" {
		t.Fatalf("fake server got content %q", published)
	}
	if lastForm == nil || lastForm.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Fatalf("publish must send a form, got %v", lastForm)
	}

	// An unknown action is refused with a real code.
	if _, err := sess.RunAction(context.Background(), "nope", nil); err == nil {
		t.Fatal("unknown action must be refused")
	} else if e, ok := err.(*dbcore.Error); !ok || e.Code != dbcore.ErrCodeNotSupported {
		t.Fatalf("unknown action code = %v", err)
	}
}

func TestNacosSplitConfigNameKeepsInnerSlash(t *testing.T) {
	g, id := splitConfigName("APP/com/app/config.json")
	if g != "APP" || id != "com/app/config.json" {
		t.Fatalf("split = %q %q", g, id)
	}
}

// ---------- Milvus ----------

func TestMilvusConsole(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/vectordb/collections/list":
			fmt.Fprint(w, `{"code":0,"data":["books","movies"]}`)
		case "/v2/vectordb/collections/describe":
			// REAL Milvus v2.5.4 shape (captured from a live standalone): fields at
			// data's top level, `primaryKey`/`nullable`, and params as [{key,value}]
			// with dim as a STRING. The old fake used data.schema.fields +
			// is_primary_key + params:{dim:4}, which no real server returns.
			fmt.Fprint(w, `{"code":0,"data":{"collectionName":"books","fields":[
				{"id":100,"name":"id","type":"Int64","primaryKey":true,"nullable":false},
				{"id":101,"name":"vec","type":"FloatVector","nullable":false,"params":[{"key":"dim","value":"4"}]}],
				"indexes":[],"load":"LoadStateLoaded"}}`)
		case "/v2/vectordb/entities/query":
			fmt.Fprint(w, `{"code":0,"data":[{"id":1,"name":"a"}]}`)
		case "/v2/vectordb/entities/search":
			// Real v2.5.4: data is a FLAT array of hit objects, not [[{...}]].
			fmt.Fprint(w, `{"code":0,"data":[{"distance":0.2,"id":1,"name":"a"},{"distance":0.5,"id":2,"name":"b"}]}`)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	sess, err := milvusProvider{}.Open(context.Background(), plugindriver.ConnectParams{
		Options: map[string]string{"baseUrl": srv.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	objs, err := sess.Objects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 || objs[0].Name != "default/books" {
		t.Fatalf("objects = %+v", objs)
	}

	// Columns show the vector dimension — the one thing a SQL header cannot express.
	cols, err := sess.Columns(context.Background(), "books")
	if err != nil {
		t.Fatal(err)
	}
	if cols[0].Key != "PK" || !strings.Contains(cols[1].Type, "FloatVector(4)") {
		t.Fatalf("columns = %+v", cols)
	}

	// Browse reads entities in field order.
	br, err := sess.Browse(context.Background(), "books", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(br.Rows) != 1 || asCell(br.Rows[0][0]).Int != 1 {
		t.Fatalf("browse = %+v", br.Rows)
	}

	// The vector search action is the console's defining op: rank + distance first.
	sr, err := sess.RunAction(context.Background(), "vector-search", map[string]any{
		"collection": "books",
		"vector":     "0.1, 0.2, 0.3, 0.4",
		"annsField":  "vec",
		"topK":       2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if asCell(sr.Rows[0][0]).Int != 1 || asCell(sr.Rows[0][1]).Flt != 0.2 {
		t.Fatalf("search first row = %+v", sr.Rows[0])
	}
	if asCell(sr.Rows[1][0]).Int != 2 {
		t.Fatalf("search second row = %+v", sr.Rows[1])
	}

	// A malformed vector is refused as an argument error, not silently.
	if _, err := sess.RunAction(context.Background(), "vector-search", map[string]any{"collection": "books", "vector": "x,y"}); err == nil {
		t.Fatal("bad vector must be refused")
	} else if e, ok := err.(*dbcore.Error); !ok || e.Code != "ERR_INVALID_ARG" {
		t.Fatalf("bad vector code = %v", err)
	}
}

// ---------- Pulsar ----------

func TestPulsarConsole(t *testing.T) {
	var bogusBacklogCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A request to the invented /subscription/{sub}/backlog endpoint must NEVER
		// happen: Pulsar 3.0 has no such admin path (verified in PersistentTopics.java).
		if strings.Contains(r.URL.Path, "/backlog") {
			bogusBacklogCalls++
			http.Error(w, "no such endpoint", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/admin/v2/tenants":
			fmt.Fprint(w, `["t1"]`)
		case "/admin/v2/namespaces/t1":
			fmt.Fprint(w, `["t1/ns1"]`)
		case "/admin/v2/persistent/t1/ns1":
			fmt.Fprint(w, `["persistent://t1/ns1/orders"]`)
		case "/admin/v2/persistent/t1/ns1/orders/subscriptions":
			fmt.Fprint(w, `["sub-a","sub-b"]`)
		case "/admin/v2/persistent/t1/ns1/orders/stats":
			// REAL Pulsar 3.0 contract (PersistentTopics.getStats): /stats carries a
			// `subscriptions` map with each subscription's msgBacklog. There is NO
			// /subscription/{sub}/backlog endpoint — the old fake invented one, which
			// is exactly why the plugin's wrong path went unnoticed.
			fmt.Fprint(w, `{"msgRateIn":1.5,"publishers":1,"subscriptions":{"sub-a":{"msgBacklog":7},"sub-b":{"msgBacklog":0}}}`)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	sess, err := pulsarProvider{}.Open(context.Background(), plugindriver.ConnectParams{
		Options: map[string]string{"baseUrl": srv.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	objs, err := sess.Objects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 || objs[0].Name != "persistent://t1/ns1/orders" {
		t.Fatalf("objects = %+v", objs)
	}

	// Browse lists subscriptions with their backlog — the operator's daily view.
	br, err := sess.Browse(context.Background(), "persistent://t1/ns1/orders", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(br.Rows) != 2 {
		t.Fatalf("browse rows = %+v", br.Rows)
	}
	if asCell(br.Rows[0][0]).Text != "sub-a" || asCell(br.Rows[0][1]).Int != 7 {
		t.Fatalf("sub-a row = %+v", br.Rows[0])
	}
	if asCell(br.Rows[1][0]).Text != "sub-b" || asCell(br.Rows[1][1]).Int != 0 {
		t.Fatalf("sub-b row = %+v", br.Rows[1])
	}

	// topic-stats renders the nested document as readable JSON in one cell.
	sr, err := sess.RunAction(context.Background(), "topic-stats", map[string]any{"topic": "persistent://t1/ns1/orders"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(asCell(sr.Rows[0][0]).Text, "msgRateIn") {
		t.Fatalf("stats = %+v", sr.Rows[0])
	}

	// list-namespaces action.
	nr, err := sess.RunAction(context.Background(), "list-namespaces", map[string]any{"tenant": "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if asCell(nr.Rows[0][0]).Text != "t1/ns1" {
		t.Fatalf("namespaces = %+v", nr.Rows)
	}
	if bogusBacklogCalls != 0 {
		t.Fatalf("called the nonexistent /backlog endpoint %d times", bogusBacklogCalls)
	}
}

// ---------- Kafka ----------

// fakeTopicLister etc. stand in for the broker-touching interfaces, so the mapping from
// a Kafka message to a grid row is tested without a running broker.
type fakeTopicLister struct {
	topics []string
	parts  map[string][]int
}

func (f fakeTopicLister) ListTopics(context.Context) ([]string, error) { return f.topics, nil }
func (f fakeTopicLister) Partitions(_ context.Context, topic string) ([]int, error) {
	if p, ok := f.parts[topic]; ok {
		return p, nil
	}
	return nil, nil
}

type fakeConsumer struct{ msgs []consoleMessage }

func (f fakeConsumer) Consume(context.Context, string, consumeOpts) ([]consoleMessage, error) {
	return f.msgs, nil
}

type fakeProducer struct{ got string }

func (f *fakeProducer) Produce(context.Context, string, string, string) error {
	f.got = "sent"
	return nil
}

func TestKafkaConsoleMapping(t *testing.T) {
	sess := &kafkaSession{
		brokers: []string{"localhost:9092"},
		list:    fakeTopicLister{topics: []string{"__consumer_offsets", "orders"}, parts: map[string][]int{"orders": {0, 1, 2}}},
		consume: fakeConsumer{msgs: []consoleMessage{
			{Partition: 1, Offset: 42, Key: "k", Value: "v", Time: time.Unix(1700000000, 0)},
		}},
	}

	// Internal topics are marked as views rather than hidden, so an operator can still
	// find one — but the tree can render it differently.
	objs, err := sess.Objects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 || objs[0].Kind != "view" || objs[1].Kind != "topic" {
		t.Fatalf("objects = %+v", objs)
	}

	// Columns carry the partition count the grid needs.
	cols, err := sess.Columns(context.Background(), "orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 4 { // partition + 3 partition columns
		t.Fatalf("columns = %+v", cols)
	}

	// Browse reads a bounded batch from earliest.
	br, err := sess.Browse(context.Background(), "orders", 10)
	if err != nil {
		t.Fatal(err)
	}
	if asCell(br.Rows[0][0]).Int != 1 || asCell(br.Rows[0][1]).Int != 42 || asCell(br.Rows[0][2]).Text != "k" {
		t.Fatalf("browse row = %+v", br.Rows[0])
	}

	// produce-message writes and reports success.
	prod := &fakeProducer{}
	sess.produce = prod
	pr, err := sess.RunAction(context.Background(), "produce-message", map[string]any{"topic": "orders", "value": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if asCell(pr.Rows[0][0]).Text != "已发送" {
		t.Fatalf("produce result = %+v", pr.Rows[0])
	}
}

// ---------- protocol dispatch ----------

// fakeSession answers with deterministic data so the dispatch's framing can be checked.
type fakeSession struct {
	actions []plugindriver.ConnAction
}

func (fakeSession) Objects(context.Context) ([]plugindriver.ConnObject, error) {
	return []plugindriver.ConnObject{{Name: "x", Kind: "collection"}}, nil
}
func (fakeSession) Columns(context.Context, string) ([]plugindriver.ConnColumn, error) {
	return []plugindriver.ConnColumn{{Name: "a", Type: "TEXT"}}, nil
}
func (fakeSession) Browse(context.Context, string, int) (*plugindriver.ConnQueryResult, error) {
	return result([]plugindriver.ConnColumn{{Name: "a", Type: "TEXT"}},
		[][]plugindriver.CellPayload{{textCell("hi")}}), nil
}
func (f fakeSession) Actions() []plugindriver.ConnAction { return f.actions }
func (fakeSession) RunAction(context.Context, string, map[string]any) (*plugindriver.ConnQueryResult, error) {
	return result([]plugindriver.ConnColumn{{Name: "r", Type: "TEXT"}},
		[][]plugindriver.CellPayload{{textCell("done")}}), nil
}
func (fakeSession) Close() error { return nil }

type fakeProvider struct{ sess Session }

func (fakeProvider) Kind() string { return "test" }
func (fakeProvider) Name() string { return "Test Console" }
func (p fakeProvider) Open(context.Context, plugindriver.ConnectParams) (Session, error) {
	return p.sess, nil
}

func TestServerDispatchFrames(t *testing.T) {
	srv := &server{prov: fakeProvider{sess: fakeSession{actions: []plugindriver.ConnAction{
		{ID: "vector-search", Title: "向量检索", Danger: false},
	}}}, sessions: map[string]Session{}}

	var out bytes.Buffer
	w := bufio.NewWriter(&out)
	var wmu sync.Mutex

	call := func(method string, params any) plugindriver.Response {
		req := plugindriver.Request{ID: 1, Method: method}
		if params != nil {
			req.Params, _ = json.Marshal(params)
		}
		out.Reset()
		w.Reset(&out)
		srv.handle(w, &wmu, req)
		w.Flush()
		var resp plugindriver.Response
		if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &resp); err != nil {
			t.Fatalf("response not a frame: %q (%v)", out.String(), err)
		}
		return resp
	}

	// open → the plugin is told the banner Kind, so open must succeed and give a conn id.
	open := call(plugindriver.MethodOpen, plugindriver.ConnectParams{Host: "h"})
	if open.Error != "" || open.Result == nil {
		t.Fatalf("open = %+v", open)
	}
	var o plugindriver.OpenResult
	if err := json.Unmarshal(open.Result, &o); err != nil || o.ConnID == "" {
		t.Fatalf("open result = %v (%v)", o, err)
	}

	// tables / columns / browse use the object tree.
	for _, method := range []string{"tables", "columns", "browse"} {
		r := call(method, plugindriver.ConnTableRef{ConnID: o.ConnID, Table: "x"})
		if r.Error != "" {
			t.Fatalf("%s = %s", method, r.Error)
		}
	}

	// connListActions declares the console's actions.
	la := call(plugindriver.MethodConnListActions, plugindriver.ConnRef{ConnID: o.ConnID})
	if la.Error != "" {
		t.Fatalf("list actions = %s", la.Error)
	}
	var actions []plugindriver.ConnAction
	if err := json.Unmarshal(la.Result, &actions); err != nil || len(actions) != 1 {
		t.Fatalf("actions = %v (%v)", actions, err)
	}

	// connRunAction runs one and returns a query-shaped result.
	ra := call(plugindriver.MethodConnRunAction, plugindriver.ConnRunActionParams{
		ConnID: o.ConnID, Action: "vector-search", Args: map[string]any{"topK": 5},
	})
	if ra.Error != "" {
		t.Fatalf("run action = %s", ra.Error)
	}
	var qr dbcore.QueryResult
	if err := json.Unmarshal(ra.Result, &qr); err != nil || len(qr.Rows) != 1 {
		t.Fatalf("run action result = %v (%v)", qr, err)
	}

	// An unknown connection is refused rather than silently answered.
	bad := call(plugindriver.MethodConnBrowse, plugindriver.ConnTableRef{ConnID: "nope", Table: "x"})
	if bad.Error == "" {
		t.Fatal("an unknown connection must be refused")
	}

	// An unknown method is a real protocol error, so a newer host talking to an older
	// plugin (or vice versa) is visible rather than silent.
	unknown := call("no-such-method", nil)
	if unknown.Code != dbcore.ErrCodeNotSupported {
		t.Fatalf("unknown method = %+v", unknown)
	}
}

// TestServerDispatchWithRealProvider closes the one seam the other tests leave open:
// the providers are tested against httptest, and the dispatch against a fake session,
// but nothing exercised a REAL provider THROUGH the dispatch. This drives a Nacos
// session through open → tables → actions → run over the framed protocol.
func TestServerDispatchWithRealProvider(t *testing.T) {
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("pageNo") != "" {
			// Real Nacos v2.3.2 shape: extra fields, type: null.
			fmt.Fprint(w, `{"totalCount":1,"pageNumber":1,"pagesAvailable":1,"pageItems":[{"id":"1","dataId":"app.json","group":"APP","content":null,"md5":null,"encryptedDataKey":"","tenant":"","appName":"","type":null}]}`)
			return
		}
		fmt.Fprint(w, "cfg-content")
	}))
	defer svc.Close()

	sc := &server{prov: nacosProvider{}, sessions: map[string]Session{}}
	var out bytes.Buffer
	w := bufio.NewWriter(&out)
	var wmu sync.Mutex
	call := func(req plugindriver.Request) plugindriver.Response {
		out.Reset()
		w.Reset(&out)
		sc.handle(w, &wmu, req)
		w.Flush()
		var resp plugindriver.Response
		if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &resp); err != nil {
			t.Fatalf("frame %q: %v", out.String(), err)
		}
		return resp
	}

	open := call(plugindriver.Request{ID: 1, Method: plugindriver.MethodOpen,
		Params: mustJSON(t, plugindriver.ConnectParams{Options: map[string]string{"baseUrl": svc.URL}})})
	if open.Error != "" {
		t.Fatalf("open: %s", open.Error)
	}
	var o plugindriver.OpenResult
	if err := json.Unmarshal(open.Result, &o); err != nil || o.ConnID == "" {
		t.Fatalf("open result = %v (%v)", o, err)
	}

	tables := call(plugindriver.Request{ID: 2, Method: "tables",
		Params: mustJSON(t, plugindriver.ConnRef{ConnID: o.ConnID})})
	if tables.Error != "" {
		t.Fatalf("tables: %s", tables.Error)
	}
	var objs []plugindriver.ConnObject
	if err := json.Unmarshal(tables.Result, &objs); err != nil || len(objs) != 1 || objs[0].Name != "APP/app.json" {
		t.Fatalf("objects = %v (%v)", objs, err)
	}

	list := call(plugindriver.Request{ID: 3, Method: plugindriver.MethodConnListActions,
		Params: mustJSON(t, plugindriver.ConnRef{ConnID: o.ConnID})})
	var actions []plugindriver.ConnAction
	if err := json.Unmarshal(list.Result, &actions); err != nil || len(actions) != 2 {
		t.Fatalf("actions = %v (%v)", actions, err)
	}

	run := call(plugindriver.Request{ID: 4, Method: plugindriver.MethodConnRunAction,
		Params: mustJSON(t, plugindriver.ConnRunActionParams{
			ConnID: o.ConnID, Action: "query-config",
			Args: map[string]any{"dataId": "app.json", "group": "APP"},
		})})
	if run.Error != "" {
		t.Fatalf("run: %s", run.Error)
	}
	var qr dbcore.QueryResult
	if err := json.Unmarshal(run.Result, &qr); err != nil || len(qr.Rows) != 1 {
		t.Fatalf("run result = %v (%v)", qr, err)
	}
	if qr.Rows[0][2].S != "cfg-content" {
		t.Fatalf("run cell = %+v", qr.Rows[0][2])
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// ---------- helpers used across the above ----------

// asCell unwraps a cell from a result row. Rows are [][]any because the protocol accepts
// both tagged cells and plain values, so a test must unwrap before it can read a field.
func asCell(v any) plugindriver.CellPayload {
	if c, ok := v.(plugindriver.CellPayload); ok {
		return c
	}
	return plugindriver.CellPayload{Kind: "text", Text: fmt.Sprint(v)}
}

// The provider files each call registerProvider in their own init; nothing to add.
