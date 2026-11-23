// Package mongo is the MongoDB driver (W16).
//
// MongoDB speaks its own wire protocol and has no SQL, so it does not fit
// dbcore's SQLDriver the way a relational engine does. It is exposed as one
// anyway, the way DBeaver exposes a document database: a COLLECTION is a
// table and a document's fields are columns. The mapping is a view over the
// documents, and the driver says so where it matters (a collection has no
// schema, so the columns are sampled and may differ between rows).
//
// The command layer exists because a SQL console has to say something to a
// MongoDB server. It accepts a small explicit command language and refuses
// everything else, rather than pretending that SQL runs.
package mongo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Ben-guanzhi/dbcore"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// MongoDriver is the MongoDB driver.
type MongoDriver struct {
	client *mongo.Client
	dbName string
}

func (d *MongoDriver) Kind() dbcore.DatabaseKind { return dbcore.KindMongo }

func (d *MongoDriver) AsSQL() (dbcore.SQLDriver, bool) { return d, true }

func (d *MongoDriver) AsKV() (dbcore.KVDriver, bool) { return nil, false }

func (d *MongoDriver) Ping(ctx context.Context) error {
	return d.client.Ping(ctx, nil)
}

func (d *MongoDriver) Close() error {
	if d.client == nil {
		return nil
	}
	return d.client.Disconnect(context.Background())
}

// db returns the database the connection points at. A MongoDB connection
// selects a database the way a relational one selects a schema.
func (d *MongoDriver) db() *mongo.Database {
	if d.dbName != "" {
		return d.client.Database(d.dbName)
	}
	return d.client.Database("admin")
}

// dbFor returns the database a command names, or the connection's own when
// the command did not name one. An empty name is not a database.
func (d *MongoDriver) dbFor(name string) *mongo.Database {
	if name != "" {
		return d.client.Database(name)
	}
	return d.db()
}

// ListSchemas lists the server's databases: MongoDB's database is the
// closest thing it has to a schema.
func (d *MongoDriver) ListSchemas(ctx context.Context) ([]dbcore.SchemaInfo, error) {
	names, err := d.client.ListDatabaseNames(ctx, map[string]any{})
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.SchemaInfo, 0, len(names))
	for _, n := range names {
		out = append(out, dbcore.SchemaInfo{Name: n})
	}
	return out, nil
}

// ListTables lists a database's collections.
func (d *MongoDriver) ListTables(ctx context.Context, schema string) ([]dbcore.TableInfo, error) {
	db := d.dbFor(schema)
	names, err := db.ListCollectionNames(ctx, map[string]any{})
	if err != nil {
		return nil, err
	}
	out := make([]dbcore.TableInfo, 0, len(names))
	for _, n := range names {
		info := dbcore.TableInfo{Schema: schema, Name: n, Type: "table", Rows: -1}
		// The estimate is the collection's own count, which is a scan for
		// large collections; a grid would rather have -1 than a slow count.
		if c, err := db.Collection(n).EstimatedDocumentCount(ctx); err == nil {
			info.Rows = c
			info.ApproxRows = true
		}
		out = append(out, info)
	}
	return out, nil
}

// ListColumns samples a collection's documents and reports the fields it
// finds. A collection has no schema, so the list is a sample, not a
// definition: fields that appear only in later documents are missing until
// the sample grows.
func (d *MongoDriver) ListColumns(ctx context.Context, schema, table string) ([]dbcore.ColumnInfo, error) {
	docs, err := d.sampleDocuments(ctx, d.dbFor(schema).Name(), table, 64)
	if err != nil {
		return nil, err
	}
	types := map[string]string{}
	seen := map[string]bool{}
	for _, doc := range docs {
		elems, err := doc.Elements()
		if err != nil {
			continue
		}
		for _, e := range elems {
			name := e.Key()
			if seen[name] {
				continue
			}
			seen[name] = true
			types[name] = mongoTypeOf(e.Value())
		}
	}
	out := make([]dbcore.ColumnInfo, 0, len(seen))
	for name, typ := range types {
		out = append(out, dbcore.ColumnInfo{Name: name, Type: typ, Nullable: true})
	}
	sortColumns(out)
	return out, nil
}

// sortColumns puts the columns in a stable order: the identifier first (it
// is the document's identity), then the rest by name.
func sortColumns(cols []dbcore.ColumnInfo) {
	for i := range cols {
		if cols[i].Name == "_id" {
			cols[0], cols[i] = cols[i], cols[0]
			break
		}
	}
	for i := 1; i < len(cols); i++ {
		for j := i; j > 0 && cols[j].Name < cols[j-1].Name; j-- {
			cols[j], cols[j-1] = cols[j-1], cols[j]
		}
	}
}

// mongoTypeOf maps a BSON value onto the kernel's column-type vocabulary.
func mongoTypeOf(val bson.RawValue) string {
	switch val.Type {
	case bson.TypeInt32, bson.TypeInt64:
		return "INTEGER"
	case bson.TypeDouble, bson.TypeDecimal128:
		return "REAL"
	case bson.TypeBoolean:
		return "BOOLEAN"
	case bson.TypeDateTime:
		return "TIMESTAMP"
	case bson.TypeBinary:
		return "BINARY"
	default:
		// Strings, objects, arrays and null all read as text in a grid;
		// objects and arrays are JSON-rendered when a row is built.
		return "TEXT"
	}
}

// ListIndexes maps a collection's MongoDB indexes onto the kernel's index
// vocabulary. A MongoDB index has no "unique" concept separate from its
// options, so the unique flag is read from them.
func (d *MongoDriver) ListIndexes(ctx context.Context, schema, table string) ([]dbcore.IndexInfo, error) {
	cur, err := d.dbFor(schema).Collection(table).Indexes().List(ctx)
	if err != nil {
		return nil, err
	}
	var results []bson.Raw
	if err := cur.All(ctx, &results); err != nil {
		return nil, err
	}
	out := make([]dbcore.IndexInfo, 0, len(results))
	for _, raw := range results {
		var idx struct {
			Name   string `bson:"name"`
			Key    bson.D `bson:"key"`
			Unique bool   `bson:"unique"`
		}
		if err := bson.Unmarshal(raw, &idx); err != nil {
			continue
		}
		info := dbcore.IndexInfo{Name: idx.Name, Unique: idx.Unique}
		for _, k := range idx.Key {
			info.Columns = append(info.Columns, k.Key)
		}
		if idx.Name == "_id_" {
			info.Primary = true
		}
		out = append(out, info)
	}
	return out, nil
}

// ListForeignKeys answers that MongoDB has no foreign keys: a reference is a
// field holding an identifier, and the server does not know about it.
func (d *MongoDriver) ListForeignKeys(ctx context.Context, schema, table string) ([]dbcore.ForeignKeyInfo, error) {
	return nil, dbcore.ErrNotSupported
}

// CreateTableSQL explains that a collection needs no creation statement: it
// comes into being with its first document.
func (d *MongoDriver) CreateTableSQL(ctx context.Context, schema, table string) (string, error) {
	return fmt.Sprintf("-- MongoDB 的集合不需要创建语句：写入第一条文档即自动创建集合 %q。", table), nil
}

// BrowseRows reads one page of a collection. The documents are flattened to
// rows: the columns are the sampled fields, and a field the row does not
// have reads as NULL.
func (d *MongoDriver) BrowseRows(ctx context.Context, schema, table string, req *dbcore.TableRowsRequest) (*dbcore.TableRowsResult, error) {
	if req == nil {
		req = &dbcore.TableRowsRequest{Page: 1, PageSize: 200}
	}
	cols, err := d.ListColumns(ctx, schema, table)
	if err != nil {
		return nil, err
	}
	filter, err := mongoFilter(req.Filter)
	if err != nil {
		return nil, err
	}
	opts := options.Find()
	if req.PageSize > 0 {
		opts.SetLimit(int64(req.PageSize))
	}
	if req.Page > 1 {
		opts.SetSkip(int64((req.Page - 1) * req.PageSize))
	}
	sortKeys, err := mongoSort(req.Sort)
	if err != nil {
		return nil, err
	}
	if len(sortKeys) > 0 {
		opts.SetSort(sortKeys)
	}
	// The keyset cursor: the last row's sort values continue the scan.
	if len(req.After) > 0 && len(sortKeys) == 1 {
		after, err := mongoAfterFilter(req.After[0])
		if err != nil {
			return nil, err
		}
		existing, _ := filter["$and"].([]any)
		filter["$and"] = append(existing, after)
	}
	cur, err := d.dbFor(schema).Collection(table).Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	var docs []bson.Raw
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := &dbcore.TableRowsResult{Columns: cols}
	for _, doc := range docs {
		out.Rows = append(out.Rows, mongoRow(doc, cols))
	}
	if !req.NoCount {
		total, err := d.dbFor(schema).Collection(table).CountDocuments(ctx, filter)
		if err == nil {
			out.Total = uint64(total)
			out.TotalFound = true
		}
	}
	return out, nil
}

// BrowseRowsCount counts the documents a request would return.
func (d *MongoDriver) BrowseRowsCount(ctx context.Context, schema, table string, req *dbcore.TableRowsRequest) (uint64, error) {
	filter, err := mongoFilter(req.Filter)
	if err != nil {
		return 0, err
	}
	n, err := d.dbFor(schema).Collection(table).CountDocuments(ctx, filter)
	if err != nil {
		return 0, err
	}
	return uint64(n), nil
}

// Execute runs one command from the small command language. The commands
// are the ones a data browser actually needs; anything else is refused with
// a message that says what the language accepts.
func (d *MongoDriver) Execute(ctx context.Context, query string, args ...dbcore.Value) (*dbcore.QueryResult, error) {
	cmd, err := parseMongoCommand(query)
	if err != nil {
		return nil, err
	}
	switch cmd.verb {
	case "list":
		if cmd.arg == "" {
			return d.listDatabases(ctx)
		}
		return d.listCollections(ctx, cmd.arg)
	case "count":
		if cmd.arg == "" {
			return nil, fmt.Errorf("count 需要集合名")
		}
		n, err := d.dbFor(cmd.db).Collection(cmd.arg).CountDocuments(ctx, map[string]any{})
		if err != nil {
			return nil, err
		}
		return &dbcore.QueryResult{
			Columns: []dbcore.ColumnInfo{{Name: "count", Type: "INTEGER"}},
			Rows:    [][]dbcore.Value{{{Kind: dbcore.ValInt, I: n}}},
		}, nil
	case "find":
		if cmd.arg == "" {
			return nil, fmt.Errorf("find 需要集合名")
		}
		limit := int64(50)
		if cmd.limit > 0 {
			limit = int64(cmd.limit)
		}
		cur, err := d.dbFor(cmd.db).Collection(cmd.arg).Find(ctx, map[string]any{},
			options.Find().SetLimit(limit))
		if err != nil {
			return nil, err
		}
		var docs []bson.Raw
		if err := cur.All(ctx, &docs); err != nil {
			return nil, err
		}
		dbName := cmd.db
		if dbName == "" {
			dbName = d.dbName
		}
		cols, err := d.ListColumns(ctx, dbName, cmd.arg)
		if err != nil {
			cols = nil
		}
		out := &dbcore.QueryResult{Columns: cols}
		for _, doc := range docs {
			out.Rows = append(out.Rows, mongoRow(doc, cols))
		}
		return out, nil
	case "insert":
		if cmd.arg == "" || cmd.json == "" {
			return nil, fmt.Errorf("insert 需要集合名和 JSON 文档")
		}
		var doc bson.M
		if err := json.Unmarshal([]byte(cmd.json), &doc); err != nil {
			return nil, fmt.Errorf("文档不是合法 JSON：%w", err)
		}
		res, err := d.dbFor(cmd.db).Collection(cmd.arg).InsertOne(ctx, doc)
		if err != nil {
			return nil, err
		}
		id := fmt.Sprintf("%v", res.InsertedID)
		return &dbcore.QueryResult{
			Columns: []dbcore.ColumnInfo{{Name: "insertedId", Type: "TEXT"}},
			Rows:    [][]dbcore.Value{{{Kind: dbcore.ValText, S: id}}},
		}, nil
	default:
		return nil, fmt.Errorf("%w", dbcore.ErrNotSupported)
	}
}

// listDatabases answers "list" with no argument.
func (d *MongoDriver) listDatabases(ctx context.Context) (*dbcore.QueryResult, error) {
	names, err := d.client.ListDatabaseNames(ctx, map[string]any{})
	if err != nil {
		return nil, err
	}
	out := &dbcore.QueryResult{Columns: []dbcore.ColumnInfo{{Name: "database", Type: "TEXT"}}}
	for _, n := range names {
		out.Rows = append(out.Rows, []dbcore.Value{{Kind: dbcore.ValText, S: n}})
	}
	return out, nil
}

// listCollections answers "list <database>".
func (d *MongoDriver) listCollections(ctx context.Context, database string) (*dbcore.QueryResult, error) {
	names, err := d.client.Database(database).ListCollectionNames(ctx, map[string]any{})
	if err != nil {
		return nil, err
	}
	out := &dbcore.QueryResult{Columns: []dbcore.ColumnInfo{{Name: "collection", Type: "TEXT"}}}
	for _, n := range names {
		out.Rows = append(out.Rows, []dbcore.Value{{Kind: dbcore.ValText, S: n}})
	}
	return out, nil
}

// sampleDocuments reads a few documents for the column sampling.
func (d *MongoDriver) sampleDocuments(ctx context.Context, schema, table string, limit int64) ([]bson.Raw, error) {
	cur, err := d.dbFor(schema).Collection(table).Find(ctx, map[string]any{},
		options.Find().SetLimit(limit))
	if err != nil {
		return nil, err
	}
	var docs []bson.Raw
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	return docs, nil
}

// mongoRow flattens one document into a row of the sampled columns. A field
// the document does not have reads as NULL, and an object or an array is
// rendered as JSON so the cell keeps its shape.
func mongoRow(doc bson.Raw, cols []dbcore.ColumnInfo) []dbcore.Value {
	row := make([]dbcore.Value, len(cols))
	for i, col := range cols {
		val, err := doc.LookupErr(col.Name)
		if err != nil {
			row[i] = dbcore.Value{Kind: dbcore.ValNull}
			continue
		}
		row[i] = mongoValue(val)
	}
	return row
}

// mongoValue maps one BSON value onto the kernel's Value.
func mongoValue(val bson.RawValue) dbcore.Value {
	switch val.Type {
	case bson.TypeNull:
		return dbcore.Value{Kind: dbcore.ValNull}
	case bson.TypeBoolean:
		return dbcore.Value{Kind: dbcore.ValBool, Bool: val.Boolean()}
	case bson.TypeInt32:
		return dbcore.Value{Kind: dbcore.ValInt, I: int64(val.Int32())}
	case bson.TypeInt64:
		return dbcore.Value{Kind: dbcore.ValInt, I: val.Int64()}
	case bson.TypeDouble:
		return dbcore.Value{Kind: dbcore.ValFloat, F: val.Double()}
	case bson.TypeDateTime:
		// A BSON datetime is milliseconds since the epoch.
		return dbcore.Value{Kind: dbcore.ValTime, T: time.UnixMilli(val.DateTime()).UTC()}
	case bson.TypeBinary:
		_, data := val.Binary()
		return dbcore.Value{Kind: dbcore.ValBlob, B: data}
	case bson.TypeString:
		return dbcore.Value{Kind: dbcore.ValText, S: val.StringValue()}
	default:
		// Objects and arrays: their JSON text is the only lossless-enough
		// rendering for a grid cell.
		return dbcore.Value{Kind: dbcore.ValText, S: rawToJSON(val)}
	}
}

// rawToJSON renders a BSON value as JSON text.
func rawToJSON(val bson.RawValue) string {
	extended, err := bson.MarshalExtJSON(val, true, false)
	if err != nil {
		return val.String()
	}
	return string(extended)
}

// mongoFilter maps the kernel's filter expressions onto a MongoDB query. An
// expression the mapping cannot express is refused: dropping it would return
// rows the caller did not ask for.
func mongoFilter(exprs []dbcore.FilterExpr) (map[string]any, error) {
	out := map[string]any{}
	for _, e := range exprs {
		switch e.Op {
		case dbcore.FilterEq, dbcore.FilterNeq:
			key := "eq"
			if e.Op == dbcore.FilterNeq {
				key = "ne"
			}
			v, err := filterValue(e.Value)
			if err != nil {
				return nil, err
			}
			out[e.Column] = map[string]any{key: v}
		case dbcore.FilterGt, dbcore.FilterGte, dbcore.FilterLt, dbcore.FilterLte:
			key := map[dbcore.FilterOp]string{
				dbcore.FilterGt: "$gt", dbcore.FilterGte: "$gte",
				dbcore.FilterLt: "$lt", dbcore.FilterLte: "$lte",
			}[e.Op]
			v, err := filterValue(e.Value)
			if err != nil {
				return nil, err
			}
			out[e.Column] = map[string]any{key: v}
		case dbcore.FilterLike:
			// A LIKE pattern becomes a regular expression: the anchors are
			// kept, the wildcards become the expression's equivalents.
			if e.Value.Kind != dbcore.ValText {
				return nil, fmt.Errorf("MongoDB 的 like 过滤需要文本模式")
			}
			pattern, err := likeToRegex(e.Value.S)
			if err != nil {
				return nil, err
			}
			out[e.Column] = map[string]any{"$regex": pattern}
		case dbcore.FilterIsNull:
			out[e.Column] = nil
		case dbcore.FilterIsNotNull:
			out[e.Column] = map[string]any{"$ne": nil}
		default:
			return nil, fmt.Errorf("MongoDB 驱动不支持该过滤条件 %q", e.Op)
		}
	}
	return out, nil
}

// filterValue maps a filter's value onto a MongoDB query value.
func filterValue(v dbcore.Value) (any, error) {
	switch v.Kind {
	case dbcore.ValNull:
		return nil, nil
	case dbcore.ValInt:
		return v.I, nil
	case dbcore.ValFloat:
		return v.F, nil
	case dbcore.ValBool:
		return v.Bool, nil
	case dbcore.ValText:
		return v.S, nil
	default:
		return nil, fmt.Errorf("MongoDB 过滤条件不支持值类型 %v", v.Kind)
	}
}

// likeToRegex converts a SQL LIKE pattern into a regular expression.
func likeToRegex(pattern string) (string, error) {
	var b strings.Builder
	for _, r := range pattern {
		switch r {
		case '%':
			b.WriteString(".*")
		case '_':
			b.WriteString(".")
		default:
			b.WriteString(regexpQuoteMeta(string(r)))
		}
	}
	return b.String(), nil
}

// regexpQuoteMeta escapes the characters that mean something in a regular
// expression.
func regexpQuoteMeta(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`\.+*?()|[]{}^$`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// mongoSort maps the kernel's sort keys onto a MongoDB sort document.
func mongoSort(keys []dbcore.SortKey) (bson.D, error) {
	var out bson.D
	for _, k := range keys {
		direction := 1
		if k.Desc {
			direction = -1
		}
		out = append(out, bson.E{Key: k.Column, Value: direction})
	}
	return out, nil
}

// mongoAfterFilter builds the keyset cursor's condition: the sort value must
// come after the one the last row carried.
func mongoAfterFilter(after dbcore.Value) (map[string]any, error) {
	switch after.Kind {
	case dbcore.ValInt:
		return map[string]any{"$gt": after.I}, nil
	case dbcore.ValFloat:
		return map[string]any{"$gt": after.F}, nil
	case dbcore.ValText:
		return map[string]any{"$gt": after.S}, nil
	default:
		return nil, fmt.Errorf("MongoDB 的游标不支持值类型 %v", after.Kind)
	}
}

// unusedTime keeps the time import honest when the build tags change.
var _ = time.Time{}
