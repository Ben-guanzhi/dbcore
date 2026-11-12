package main

// Milvus console: vector collections over the v2 RESTful API.
//
// A vector database is the clearest case for the console-action protocol, because its
// defining operation — a similarity search — is NOT a browse. Listing collections and
// reading entities map onto the ordinary object tree (collections → fields → rows), but
// "give me the top-K nearest to this vector" is a console ACTION: it takes a vector the
// user pastes, an anns field and a topK, and returns ranked hits with distances.
//
// The REST envelope is {code, message, data}; code 0 is success. A non-zero code is a
// real error and is surfaced as one, not as an empty grid.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/plugindriver"
)

type milvusProvider struct{}

func (milvusProvider) Kind() string { return "milvus" }
func (milvusProvider) Name() string { return "Milvus 向量数据库" }

func init() { registerProvider(milvusProvider{}) }

func (milvusProvider) Open(_ context.Context, p plugindriver.ConnectParams) (Session, error) {
	hp := hostPort{Host: p.Host, Port: p.Port, Scheme: p.Options["scheme"], Base: p.Options["baseUrl"]}
	if hp.Port == "" {
		// Milvus's RESTful gateway is conventionally 19530 alongside gRPC; defaulting
		// it means "localhost" builds a working URL.
		hp.Port = "19530"
	}
	if hp.Host == "" && hp.Base == "" {
		return nil, fmt.Errorf("需要主机地址")
	}
	c := newHTTPConsole(hp)
	c.token = p.Options["token"]
	return &milvusSession{c: c, db: orDefault(p.Database, "default")}, nil
}

type milvusSession struct {
	c  *httpConsole
	db string
}

func (s *milvusSession) call(ctx context.Context, path string, body any, out any) error {
	raw, status, err := s.c.postJSON(ctx, path, body)
	if err != nil {
		return err
	}
	var env struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := decodeJSON(raw, status, &env); err != nil {
		return err
	}
	if env.Code != 0 {
		return fmt.Errorf("Milvus 错误 %d: %s", env.Code, env.Message)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}

func (s *milvusSession) Objects(ctx context.Context) ([]plugindriver.ConnObject, error) {
	var collections []string
	if err := s.call(ctx, "/v2/vectordb/collections/list",
		map[string]any{"dbName": s.db}, &collections); err != nil {
		return nil, fmt.Errorf("列出集合: %w", err)
	}
	out := make([]plugindriver.ConnObject, 0, len(collections))
	for _, name := range collections {
		out = append(out, plugindriver.ConnObject{Name: s.db + "/" + name, Kind: "collection"})
	}
	return out, nil
}

// milvusField is one field of a collection's schema.
//
// Real-service integration finding (Milvus v2.5.4): describe returns fields at the
// TOP level of data as `fields`, each with `name`/`type`/`primaryKey`/`nullable`, and
// `params` as an ARRAY of {key,value} pairs whose "dim" value is a STRING. The shape
// assumed from the docs (`data.schema.fields`, `is_primary_key`, `params:{dim:4}`) never
// matches a real server — an httptest fake built from the docs hid that completely.
// UnmarshalJSON accepts both spellings so one plugin works across versions.
type milvusField struct {
	Name         string
	Type         string
	IsPrimaryKey bool
	Nullable     bool
	Dim          int
}

func (f *milvusField) UnmarshalJSON(raw []byte) error {
	var base struct {
		Name         string `json:"name"`
		Type         string `json:"type"`
		IsPrimaryKey bool   `json:"is_primary_key"`
		PrimaryKey   bool   `json:"primaryKey"`
		Nullable     bool   `json:"nullable"`
		IsNullable   bool   `json:"is_nullable"`
	}
	if err := json.Unmarshal(raw, &base); err != nil {
		return err
	}
	f.Name, f.Type = base.Name, base.Type
	f.IsPrimaryKey = base.IsPrimaryKey || base.PrimaryKey
	f.Nullable = base.Nullable || base.IsNullable
	// params is either {"dim":4} or [{"key":"dim","value":"4"}].
	var obj struct {
		Dim json.RawMessage `json:"dim"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && len(obj.Dim) > 0 {
		f.Dim = rawInt(obj.Dim)
	}
	if f.Dim == 0 {
		var pairs []struct {
			Key   string          `json:"key"`
			Value json.RawMessage `json:"value"`
		}
		var holder struct {
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(raw, &holder); err == nil && len(holder.Params) > 0 {
			_ = json.Unmarshal(holder.Params, &pairs)
			for _, p := range pairs {
				if p.Key == "dim" {
					f.Dim = rawInt(p.Value)
				}
			}
			// The object form may also sit directly under params.
			var pobj struct {
				Dim json.RawMessage `json:"dim"`
			}
			if f.Dim == 0 {
				_ = json.Unmarshal(holder.Params, &pobj)
				f.Dim = rawInt(pobj.Dim)
			}
		}
	}
	return nil
}

// rawInt reads a JSON number or a numeric string ("4") into an int, since Milvus v2
// returns element-type params as strings.
func rawInt(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		_, _ = fmt.Sscanf(s, "%d", &n)
	}
	return n
}

func (s *milvusSession) describe(ctx context.Context, collection string) ([]milvusField, error) {
	// Two spellings: real v2.5 has `fields` at data's top level; older docs put them
	// under `schema`. Decode both so one binary survives a version change.
	var data struct {
		Fields []milvusField `json:"fields"`
		Schema struct {
			Fields []milvusField `json:"fields"`
		} `json:"schema"`
	}
	if err := s.call(ctx, "/v2/vectordb/collections/describe",
		map[string]any{"dbName": s.db, "collectionName": collection}, &data); err != nil {
		return nil, err
	}
	if len(data.Fields) > 0 {
		return data.Fields, nil
	}
	return data.Schema.Fields, nil
}

func (s *milvusSession) Columns(ctx context.Context, object string) ([]plugindriver.ConnColumn, error) {
	fields, err := s.describe(ctx, objectName(s.db, object))
	if err != nil {
		return nil, err
	}
	cols := make([]plugindriver.ConnColumn, 0, len(fields))
	for _, f := range fields {
		c := plugindriver.ConnColumn{Name: f.Name, Type: f.Type, Nullable: f.Nullable}
		// The vector dimension is the one thing a SQL column header cannot show, and
		// it is the thing a user needs to know before they search.
		if f.Dim > 0 {
			c.Type = fmt.Sprintf("%s(%d)", f.Type, f.Dim)
		}
		if f.IsPrimaryKey {
			c.Key = "PK"
		}
		cols = append(cols, c)
	}
	return cols, nil
}

// Browse reads entities with a match-all filter, in the collection's field order so
// the grid's columns are stable rather than map-iteration order.
func (s *milvusSession) Browse(ctx context.Context, object string, limit int) (*plugindriver.ConnQueryResult, error) {
	collection := objectName(s.db, object)
	fields, err := s.describe(ctx, collection)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(fields))
	cols := make([]plugindriver.ConnColumn, 0, len(fields))
	for _, f := range fields {
		names = append(names, f.Name)
		col := plugindriver.ConnColumn{Name: f.Name, Type: f.Type, Nullable: f.Nullable}
		if f.Dim > 0 {
			col.Type = fmt.Sprintf("%s(%d)", f.Type, f.Dim)
		}
		if f.IsPrimaryKey {
			col.Key = "PK"
		}
		cols = append(cols, col)
	}
	rows, err := s.query(ctx, collection, "", names, limit)
	if err != nil {
		return nil, err
	}
	return result(cols, rows), nil
}

func (s *milvusSession) Actions() []plugindriver.ConnAction {
	return []plugindriver.ConnAction{
		{
			ID:    "vector-search",
			Title: "向量检索",
			Group: "检索",
			Params: []plugindriver.ConnActionParam{
				{Name: "collection", Label: "集合", Required: true},
				{Name: "vector", Label: "查询向量（逗号分隔）", Kind: "textarea", Required: true},
				{Name: "annsField", Label: "向量字段"},
				{Name: "topK", Label: "Top-K", Kind: "number", Default: "10"},
				{Name: "filter", Label: "过滤表达式"},
				{Name: "outputFields", Label: "输出字段（逗号分隔，留空取 *）"},
			},
		},
	}
}

func (s *milvusSession) RunAction(ctx context.Context, action string, args map[string]any) (*plugindriver.ConnQueryResult, error) {
	if action != "vector-search" {
		return nil, &dbcore.Error{Code: dbcore.ErrCodeNotSupported, Message: "未知动作 " + action}
	}
	collection := argString(args, "collection")
	vecText := argString(args, "vector")
	if collection == "" || vecText == "" {
		return nil, &dbcore.Error{Code: "ERR_INVALID_ARG", Message: "collection 与 vector 不能为空"}
	}
	vector, err := parseVector(vecText)
	if err != nil {
		return nil, &dbcore.Error{Code: "ERR_INVALID_ARG", Message: err.Error()}
	}
	topK := argInt(args, "topK", 10)
	if topK <= 0 {
		topK = 10
	}
	body := map[string]any{
		"dbName":         s.db,
		"collectionName": collection,
		"data":           [][]float64{vector},
		"limit":          topK,
	}
	if f := argString(args, "annsField"); f != "" {
		body["annsField"] = f
	}
	output := parseList(argString(args, "outputFields"))
	filter := argString(args, "filter")
	if filter != "" {
		body["filter"] = filter
	}
	if len(output) > 0 {
		body["outputFields"] = output
	} else {
		body["outputFields"] = []string{"*"}
	}
	// Real-service integration finding (Milvus v2.5.4): search's `data` is a FLAT
	// array of hit objects — [{distance,id,title,vec}, ...] — not the nested
	// [[{...}]] shape the docs (and our first fake service) implied. Accept both so a
	// plugin built against one version keeps working with the other.
	var rawHits []json.RawMessage
	if err := s.call(ctx, "/v2/vectordb/entities/search", body, &rawHits); err != nil {
		return nil, fmt.Errorf("向量检索: %w", err)
	}
	hits := make([]map[string]any, 0, len(rawHits))
	for _, raw := range rawHits {
		var hit map[string]any
		if err := json.Unmarshal(raw, &hit); err == nil {
			hits = append(hits, hit)
			continue
		}
		// Nested form: one query vector's hit list per element; we send one vector,
		// so take the first list.
		var group []map[string]any
		if err := json.Unmarshal(raw, &group); err == nil {
			hits = append(hits, group...)
		}
	}
	var rows [][]plugindriver.CellPayload
	cols := []plugindriver.ConnColumn{{Name: "rank", Type: "INT"}, {Name: "distance", Type: "FLOAT"}}
	for i, hit := range hits {
		if i == 0 {
			// Discover the hit's fields from the first row so the columns match whatever
			// the server returned, then prepend rank/distance once.
			for _, name := range orderedKeys(hit, "distance") {
				cols = append(cols, plugindriver.ConnColumn{Name: name, Type: "TEXT"})
			}
		}
		row := []plugindriver.CellPayload{intCell(int64(i + 1)), floatCell(toFloat(hit["distance"]))}
		for _, name := range orderedKeys(hit, "distance") {
			row = append(row, anyCell(hit[name]))
		}
		rows = append(rows, row)
	}
	return result(cols, rows), nil
}

// query runs a match-all (or filtered) entity read and builds rows in field order.
func (s *milvusSession) query(
	ctx context.Context,
	collection, filter string,
	outputFields []string,
	limit int,
) ([][]plugindriver.CellPayload, error) {
	body := map[string]any{
		"dbName":         s.db,
		"collectionName": collection,
		"outputFields":   outputFields,
		"limit":          limit,
	}
	if filter != "" {
		body["filter"] = filter
	}
	var data []map[string]any
	if err := s.call(ctx, "/v2/vectordb/entities/query", body, &data); err != nil {
		return nil, fmt.Errorf("读取实体: %w", err)
	}
	rows := make([][]plugindriver.CellPayload, 0, len(data))
	for _, entity := range data {
		row := make([]plugindriver.CellPayload, 0, len(outputFields))
		for _, name := range outputFields {
			row = append(row, anyCell(entity[name]))
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (s *milvusSession) Close() error { return nil }

// ---------- small helpers ----------

// parseVector turns "0.1, 0.2, 0.3" into a float slice. A vector pasted from anywhere
// (a log, another tool) uses commas; tolerating spaces and brackets keeps that easy.
func parseVector(s string) ([]float64, error) {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "[]")
	parts := strings.Split(s, ",")
	out := make([]float64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		f, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return nil, fmt.Errorf("向量分量 %q 不是数字", p)
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("向量为空")
	}
	return out, nil
}

func parseList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// anyCell maps an arbitrary JSON value to a cell. Vectors come back as arrays, so a
// non-scalar is rendered as its JSON — compact, and honest about what it is.
func anyCell(v any) plugindriver.CellPayload {
	switch t := v.(type) {
	case nil:
		return plugindriver.CellPayload{Kind: "null"}
	case string:
		return textCell(t)
	case bool:
		return boolCell(t)
	case float64:
		if t == float64(int64(t)) {
			return intCell(int64(t))
		}
		return floatCell(t)
	case []any:
		b, _ := json.Marshal(t)
		return textCell(string(b))
	default:
		b, _ := json.Marshal(t)
		return textCell(string(b))
	}
}

func toFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int64:
		return float64(t)
	case int:
		return float64(t)
	}
	return 0
}

// orderedKeys returns a map's keys in a stable order: the "skip" field (distance) is
// dropped because the caller renders it explicitly, and the rest are sorted so the grid
// does not shuffle between rows.
func orderedKeys(m map[string]any, skip string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		if k == skip {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func objectName(db, object string) string {
	// The object tree names a collection "db/collection"; strip the db prefix when it
	// is present so the API always gets the bare collection name.
	if i := strings.Index(object, "/"); i >= 0 {
		return object[i+1:]
	}
	return object
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
