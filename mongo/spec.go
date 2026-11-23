package mongo

// spec.go — the factory, the DSN, and the command language the SQL console
// speaks to a MongoDB server.

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/Ben-guanzhi/dbcore"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func init() { dbcore.RegisterFactory(dbcore.KindMongo, OpenSpec) }

// DriverName is the database/sql-style name the driver registers under.
const DriverName = "mongo"

// dsnScheme is the URL scheme the mongo driver parses ("mongodb://...").
const dsnScheme = "mongodb"

// DSN renders spec as the mongodb:// URL the driver parses. The scheme is
// the driver name, the userinfo carries the credentials, and the DATABASE
// is the URL's PATH ("mongodb://host:27017/novelgen") — that is where the
// mongo driver reads it from; a "database" query parameter is not a thing
// it knows and it rejects the whole URL.
func DSN(spec dbcore.ConnSpec) string {
	u := url.URL{
		Scheme: dsnScheme,
		Host:   spec.Addr(),
	}
	if spec.User != "" {
		if spec.Password != "" {
			u.User = url.UserPassword(spec.User, spec.Password)
		} else {
			u.User = url.User(spec.User)
		}
	}
	if spec.Database != "" {
		u.Path = "/" + spec.Database
	}
	q := url.Values{}
	if spec.Schema != "" {
		q.Set("authSource", spec.Schema)
	}
	if spec.SSLMode != "" && spec.SSLMode != "disable" {
		q.Set("tls", "true")
	}
	for k, v := range spec.Params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// OpenSpec opens a connection for a spec.
func OpenSpec(ctx context.Context, spec dbcore.ConnSpec, pool dbcore.PoolConfig) (dbcore.Driver, error) {
	return Open(ctx, DSN(spec))
}

// Open connects with a DSN. The database name is read from the URL's
// "database" parameter; without one the driver answers against "admin".
//
// The v2 driver connects lazily: the first operation establishes the
// connection, so a Ping is what actually proves the server is reachable.
func Open(ctx context.Context, dsn string) (*MongoDriver, error) {
	opts := options.Client().ApplyURI(dsn)
	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, err
	}
	dbName := ""
	if u, err := url.Parse(dsn); err == nil {
		// The database is the URL's path ("mongodb://host:27017/novelgen").
		dbName = strings.Trim(u.Path, "/")
	}
	return &MongoDriver{client: client, dbName: dbName}, nil
}

// mongoCommand is one parsed command of the small command language.
type mongoCommand struct {
	verb  string // list | count | find | insert
	db    string // the database the command names ("" = the connection's)
	arg   string // the collection (for count/find/insert) or database (list)
	limit int    // find's limit
	json  string // insert's document
}

// parseMongoCommand parses the command language:
//
//	list                              — the databases
//	list <database>                   — the collections of a database
//	count <collection>                — the document count
//	find <collection> [limit N]       — the documents, as rows
//	insert <collection> <json>        — one document
//
// The verbs are case-insensitive. A database may be named as "db.collection"
// so a command can address a collection in another database.
func parseMongoCommand(text string) (mongoCommand, error) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return mongoCommand{}, fmt.Errorf("命令为空")
	}
	cmd := mongoCommand{verb: strings.ToLower(fields[0])}
	rest := fields[1:]
	switch cmd.verb {
	case "list":
		if len(rest) > 0 {
			cmd.arg = rest[0]
		}
		return cmd, nil
	case "count", "find", "insert":
		if len(rest) == 0 {
			return mongoCommand{}, fmt.Errorf("%s 需要集合名", cmd.verb)
		}
		name := rest[0]
		if i := strings.Index(name, "."); i > 0 {
			cmd.db, name = name[:i], name[i+1:]
		}
		cmd.arg = name
		rest = rest[1:]
		switch cmd.verb {
		case "find":
			if len(rest) >= 2 && strings.EqualFold(rest[0], "limit") {
				n, err := strconv.Atoi(rest[1])
				if err != nil {
					return mongoCommand{}, fmt.Errorf("limit 需要数字：%w", err)
				}
				cmd.limit = n
			}
		case "insert":
			if len(rest) == 0 {
				return mongoCommand{}, fmt.Errorf("insert 需要 JSON 文档")
			}
			cmd.json = strings.Join(rest, " ")
		}
		return cmd, nil
	default:
		return mongoCommand{}, fmt.Errorf("未知命令 %q；可用：list / count / find / insert", cmd.verb)
	}
}
