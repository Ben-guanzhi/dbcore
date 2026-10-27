// Package redis implements dbcore.KVDriver for Redis via go-redis/v9.
//
// DSN example: "redis://user:pass@127.0.0.1:6379/0". Unlike the SQL drivers,
// Redis has no schemas or transactions; capabilities are exposed through
// AsKV rather than AsSQL (DB-CORE-CONTRACT.md §4.6).
package redis

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	goredis "github.com/redis/go-redis/v9"

	"github.com/dbcore/dbcore"
)

const defaultScanCount = 100

var (
	_ dbcore.KVDriver           = (*RedisDriver)(nil)
	_ dbcore.ElementWriter      = (*RedisDriver)(nil)
	_ dbcore.ElementDeleter     = (*RedisDriver)(nil)
	_ dbcore.RowAppender        = (*RedisDriver)(nil)
	_ dbcore.KeyAdmin           = (*RedisDriver)(nil)
	_ dbcore.KeyScanner         = (*RedisDriver)(nil)
	_ dbcore.TypedCommander     = (*RedisDriver)(nil)
	_ dbcore.BoundedValueReader = (*RedisDriver)(nil)
)

// ScanMaxRounds caps the rounds one ScanKeysEnriched call may chain (D5,
// spec/behavior.md §1.3): fill count keys, exhaust the cursor, or stop here
// with a real continuation cursor. Exported because the cap is a cross-
// implementation contract value (the Rust driver carries the same number).
const ScanMaxRounds = 100

// RedisDriver is a dbcore.KVDriver backed by a go-redis client.
type RedisDriver struct {
	mu     sync.RWMutex
	client *goredis.Client
	opts   *goredis.Options
}

// Open connects using a redis:// URL.
func Open(dsn string) (*RedisDriver, error) {
	opts, err := goredis.ParseURL(dsn)
	if err != nil {
		return nil, err
	}
	return OpenOptions(opts)
}

// OpenOptions connects using explicit go-redis options (useful for TLS,
// sentinel/cluster-adjacent tuning and tests).
func OpenOptions(opts *goredis.Options) (*RedisDriver, error) {
	c := goredis.NewClient(opts)
	if err := c.Ping(context.Background()).Err(); err != nil {
		_ = c.Close()
		return nil, err
	}
	cp := *opts
	return &RedisDriver{client: c, opts: &cp}, nil
}

func (d *RedisDriver) Kind() dbcore.DatabaseKind       { return dbcore.KindRedis }
func (d *RedisDriver) AsSQL() (dbcore.SQLDriver, bool) { return nil, false }
func (d *RedisDriver) AsKV() (dbcore.KVDriver, bool)   { return d, true }

func (d *RedisDriver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.client.Close()
}

func (d *RedisDriver) Ping(ctx context.Context) error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.client.Ping(ctx).Err()
}

// SelectDB switches the database by dialing a fresh client: SELECT is
// connection-level state and the pool would otherwise keep mixed DBs. The
// whole swap runs under the write lock so concurrent switches cannot close a
// client that another switch already published.
func (d *RedisDriver) SelectDB(ctx context.Context, idx int) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	opts := *d.opts
	opts.DB = idx

	c := goredis.NewClient(&opts)
	if err := c.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		return err
	}

	old := d.client
	d.opts = &opts
	d.client = c
	return old.Close()
}

// ScanKeys pages through keys with SCAN. An empty pattern means "all keys"
// (Redis itself would treat "" as a literal key pattern and match nothing).
func (d *RedisDriver) ScanKeys(ctx context.Context, cursor uint64, pattern string, count int) (*dbcore.RedisScanPage, error) {
	if count <= 0 {
		count = defaultScanCount
	}
	if pattern == "" {
		pattern = "*"
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	keys, next, err := d.client.Scan(ctx, cursor, pattern, int64(count)).Result()
	if err != nil {
		return nil, err
	}
	return &dbcore.RedisScanPage{Cursor: next, Keys: keys}, nil
}

func (d *RedisDriver) KeyType(ctx context.Context, key string) (dbcore.RedisKeyType, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	t, err := d.client.Type(ctx, key).Result()
	if err != nil {
		return dbcore.RedisNone, err
	}
	return redisKeyType(t), nil
}

// ScanKeysEnriched is the D5 "fill the page" scan: rounds continue until count
// keys are collected, the cursor returns to 0, or the round cap is reached,
// and every key carries its TYPE and remaining TTL so a page renders in one
// call. TTL collapses to seconds when positive; the raw sentinels (-1 no
// expiry, -2 missing) pass through like the bare TTL/PTTL reply would.
func (d *RedisDriver) ScanKeysEnriched(ctx context.Context, cursor uint64, pattern string, count int) (*dbcore.EnrichedScanPage, error) {
	if pattern == "" {
		pattern = "*"
	}
	if count <= 0 {
		count = defaultScanCount
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	page := &dbcore.EnrichedScanPage{Keys: []dbcore.RedisKeyEntry{}}
	for round := 0; round < ScanMaxRounds; round++ {
		keys, next, err := d.client.Scan(ctx, cursor, pattern, int64(count)).Result()
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			typ, err := d.client.Type(ctx, k).Result()
			if err != nil {
				return nil, err
			}
			ttl := d.client.TTL(ctx, k).Val()
			// go-redis returns -1ns / -2ns for the no-expiry and missing
			// sentinels; positive values are whole seconds. Dividing a negative
			// would truncate -1ns to 0, so negatives keep their raw value.
			ttlSec := int64(ttl / time.Second)
			if ttl < 0 {
				ttlSec = int64(ttl)
			}
			page.Keys = append(page.Keys, dbcore.RedisKeyEntry{
				Key:  k,
				Kind: redisKeyType(typ),
				TTL:  &ttlSec,
			})
		}
		cursor = next
		if cursor == 0 || len(page.Keys) >= count {
			break
		}
	}
	page.Cursor = cursor
	return page, nil
}

func (d *RedisDriver) GetValue(ctx context.Context, key string) (dbcore.RedisValue, error) {
	// Hold the read lock across TYPE + read so a concurrent SelectDB cannot
	// close the client between the two commands.
	d.mu.RLock()
	defer d.mu.RUnlock()
	c := d.client
	t, err := c.Type(ctx, key).Result()
	if err != nil {
		return dbcore.RedisValue{}, err
	}
	switch redisKeyType(t) {
	case dbcore.RedisNone:
		return dbcore.RedisValue{}, fmt.Errorf("redis: key %q not found", key)
	case dbcore.RedisStr:
		s, err := c.Get(ctx, key).Result()
		if err != nil {
			return dbcore.RedisValue{}, err
		}
		v := dbcore.RedisValue{Type: dbcore.RedisStr, Str: s}
		if n, perr := strconv.ParseInt(s, 10, 64); perr == nil {
			v.Int = n
		}
		return v, nil
	case dbcore.RedisList:
		items, err := c.LRange(ctx, key, 0, -1).Result()
		if err != nil {
			return dbcore.RedisValue{}, err
		}
		return dbcore.RedisValue{Type: dbcore.RedisList, Items: items}, nil
	case dbcore.RedisSet:
		items, err := c.SMembers(ctx, key).Result()
		if err != nil {
			return dbcore.RedisValue{}, err
		}
		sort.Strings(items)
		return dbcore.RedisValue{Type: dbcore.RedisSet, Items: items}, nil
	case dbcore.RedisZSet:
		zs, err := c.ZRangeWithScores(ctx, key, 0, -1).Result()
		if err != nil {
			return dbcore.RedisValue{}, err
		}
		v := dbcore.RedisValue{Type: dbcore.RedisZSet}
		for _, z := range zs {
			v.Items = append(v.Items, fmt.Sprint(z.Member))
			v.Scores = append(v.Scores, z.Score)
		}
		return v, nil
	case dbcore.RedisHash:
		h, err := c.HGetAll(ctx, key).Result()
		if err != nil {
			return dbcore.RedisValue{}, err
		}
		return dbcore.RedisValue{Type: dbcore.RedisHash, Hash: h}, nil
	case dbcore.RedisStream:
		msgs, err := c.XRangeN(ctx, key, "-", "+", 100).Result()
		if err != nil {
			return dbcore.RedisValue{}, err
		}
		v := dbcore.RedisValue{Type: dbcore.RedisStream}
		for _, m := range msgs {
			fields := make([]string, 0, len(m.Values))
			entry := dbcore.RedisStreamEntry{ID: m.ID}
			for k, val := range m.Values {
				fields = append(fields, fmt.Sprintf("%s=%v", k, val))
				entry.Fields = append(entry.Fields, dbcore.RedisField{Name: k, Value: fmt.Sprint(val)})
			}
			sort.Strings(fields)
			v.Items = append(v.Items, m.ID+" "+strings.Join(fields, " "))
			v.Stream = append(v.Stream, entry)
		}
		return v, nil
	default:
		return dbcore.RedisValue{Type: redisKeyType(t)}, nil
	}
}

// GetValueBounded reads a key's value under a limit (BoundedValueReader): the
// first window of any key in one call, with the full size and a truncated
// flag, so a value grid never pays for a million-member collection or a
// half-gigabyte string just to paint its first rows. Strings are cut at
// MaxStringBytes on a rune boundary; collections return the first MaxEntries
// entries — lists and zsets by rank, hash and set by SCAN (SSCAN can repeat a
// member mid-rehash; the page is deduplicated), streams by ID order. A limit
// of zero resolves to the package defaults.
func (d *RedisDriver) GetValueBounded(ctx context.Context, key string, limit dbcore.RedisValueLimit) (dbcore.BoundedRedisValue, error) {
	if limit.MaxStringBytes <= 0 {
		limit.MaxStringBytes = dbcore.DefaultMaxStringBytes
	}
	if limit.MaxEntries <= 0 {
		limit.MaxEntries = dbcore.DefaultMaxEntries
	}
	// Hold the read lock across every command, exactly like GetValue: a
	// concurrent SelectDB must not swap the client mid-read.
	d.mu.RLock()
	defer d.mu.RUnlock()
	c := d.client
	t, err := c.Type(ctx, key).Result()
	if err != nil {
		return dbcore.BoundedRedisValue{}, err
	}
	switch redisKeyType(t) {
	case dbcore.RedisNone:
		return dbcore.BoundedRedisValue{}, fmt.Errorf("redis: key %q not found", key)
	case dbcore.RedisStr:
		n, err := c.StrLen(ctx, key).Result()
		if err != nil {
			return dbcore.BoundedRedisValue{}, err
		}
		out := dbcore.BoundedRedisValue{Value: dbcore.RedisValue{Type: dbcore.RedisStr}, Total: n}
		if n <= limit.MaxStringBytes {
			out.Value.Str, err = c.Get(ctx, key).Result()
			if err != nil {
				return dbcore.BoundedRedisValue{}, err
			}
		} else {
			out.Truncated = true
			s, err := c.GetRange(ctx, key, 0, limit.MaxStringBytes-1).Result()
			if err != nil {
				return dbcore.BoundedRedisValue{}, err
			}
			out.Value.Str = trimPartialRune(s)
		}
		// The Int convenience only means something on the whole value: a
		// truncated decimal is a different number, not a smaller one.
		if !out.Truncated {
			if n, perr := strconv.ParseInt(out.Value.Str, 10, 64); perr == nil {
				out.Value.Int = n
			}
		}
		return out, nil
	case dbcore.RedisList:
		n, err := c.LLen(ctx, key).Result()
		if err != nil {
			return dbcore.BoundedRedisValue{}, err
		}
		items, err := c.LRange(ctx, key, 0, limit.MaxEntries-1).Result()
		if err != nil {
			return dbcore.BoundedRedisValue{}, err
		}
		return dbcore.BoundedRedisValue{
			Value:     dbcore.RedisValue{Type: dbcore.RedisList, Items: items},
			Total:     n,
			Truncated: n > limit.MaxEntries,
		}, nil
	case dbcore.RedisSet:
		card, err := c.SCard(ctx, key).Result()
		if err != nil {
			return dbcore.BoundedRedisValue{}, err
		}
		members, err := scanSetWindow(ctx, c, key, limit.MaxEntries)
		if err != nil {
			return dbcore.BoundedRedisValue{}, err
		}
		sort.Strings(members)
		return dbcore.BoundedRedisValue{
			Value:     dbcore.RedisValue{Type: dbcore.RedisSet, Items: members},
			Total:     card,
			Truncated: card > int64(len(members)),
		}, nil
	case dbcore.RedisZSet:
		card, err := c.ZCard(ctx, key).Result()
		if err != nil {
			return dbcore.BoundedRedisValue{}, err
		}
		zs, err := c.ZRangeWithScores(ctx, key, 0, limit.MaxEntries-1).Result()
		if err != nil {
			return dbcore.BoundedRedisValue{}, err
		}
		out := dbcore.BoundedRedisValue{Value: dbcore.RedisValue{Type: dbcore.RedisZSet}, Total: card, Truncated: card > limit.MaxEntries}
		for _, z := range zs {
			out.Value.Items = append(out.Value.Items, fmt.Sprint(z.Member))
			out.Value.Scores = append(out.Value.Scores, z.Score)
		}
		return out, nil
	case dbcore.RedisHash:
		n, err := c.HLen(ctx, key).Result()
		if err != nil {
			return dbcore.BoundedRedisValue{}, err
		}
		hash, err := scanHashWindow(ctx, c, key, limit.MaxEntries)
		if err != nil {
			return dbcore.BoundedRedisValue{}, err
		}
		return dbcore.BoundedRedisValue{
			Value:     dbcore.RedisValue{Type: dbcore.RedisHash, Hash: hash},
			Total:     n,
			Truncated: n > int64(len(hash)),
		}, nil
	case dbcore.RedisStream:
		n, err := c.XLen(ctx, key).Result()
		if err != nil {
			return dbcore.BoundedRedisValue{}, err
		}
		msgs, err := c.XRangeN(ctx, key, "-", "+", limit.MaxEntries).Result()
		if err != nil {
			return dbcore.BoundedRedisValue{}, err
		}
		v := dbcore.RedisValue{Type: dbcore.RedisStream}
		for _, m := range msgs {
			fields := make([]string, 0, len(m.Values))
			entry := dbcore.RedisStreamEntry{ID: m.ID}
			for k, val := range m.Values {
				fields = append(fields, fmt.Sprintf("%s=%v", k, val))
				entry.Fields = append(entry.Fields, dbcore.RedisField{Name: k, Value: fmt.Sprint(val)})
			}
			sort.Strings(fields)
			v.Items = append(v.Items, m.ID+" "+strings.Join(fields, " "))
			v.Stream = append(v.Stream, entry)
		}
		return dbcore.BoundedRedisValue{Value: v, Total: n, Truncated: n > limit.MaxEntries}, nil
	default:
		return dbcore.BoundedRedisValue{Value: dbcore.RedisValue{Type: redisKeyType(t)}}, nil
	}
}

// scanSetWindow collects up to max distinct members with SSCAN. Whether
// members remain is the caller's call (SCARD against what came back): SCAN's
// cursor only says the walk ended, not that the window was the whole set, and
// a server may return everything in one round regardless of the count hint.
func scanSetWindow(ctx context.Context, c *goredis.Client, key string, max int64) ([]string, error) {
	seen := map[string]struct{}{}
	var members []string
	var cursor uint64
	for {
		batch, next, err := c.SScan(ctx, key, cursor, "", max-int64(len(members))).Result()
		if err != nil {
			return nil, err
		}
		for _, m := range batch {
			if _, dup := seen[m]; dup {
				continue
			}
			seen[m] = struct{}{}
			members = append(members, m)
		}
		if next == 0 || int64(len(members)) >= max {
			return members, nil
		}
		cursor = next
	}
}

// scanHashWindow is scanSetWindow for hashes: HSCAN pairs flattened as
// field/value, deduplicated by field, up to max fields.
func scanHashWindow(ctx context.Context, c *goredis.Client, key string, max int64) (map[string]string, error) {
	hash := map[string]string{}
	var cursor uint64
	for {
		raw, next, err := c.HScan(ctx, key, cursor, "", max-int64(len(hash))).Result()
		if err != nil {
			return nil, err
		}
		for i := 0; i+1 < len(raw) && int64(len(hash)) < max; i += 2 {
			hash[raw[i]] = raw[i+1]
		}
		if next == 0 || int64(len(hash)) >= max {
			return hash, nil
		}
		cursor = next
	}
}

// trimPartialRune backs a byte cut up to the last complete UTF-8 rune, so a
// truncated string value stays valid text for the grid.
func trimPartialRune(s string) string {
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// SetValue replaces the key with v. Sorted sets use Scores parallel to Items
// (missing scores default to 0). Streams are read-only here; use ExecCommand.
func (d *RedisDriver) SetValue(ctx context.Context, key string, v dbcore.RedisValue) error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	c := d.client
	switch v.Type {
	case dbcore.RedisStr:
		s := v.Str
		if s == "" && v.Int != 0 {
			s = strconv.FormatInt(v.Int, 10)
		}
		return c.Set(ctx, key, s, 0).Err()
	case dbcore.RedisList:
		_, err := c.TxPipelined(ctx, func(p goredis.Pipeliner) error {
			p.Del(ctx, key)
			if len(v.Items) > 0 {
				p.RPush(ctx, key, stringArgs(v.Items)...)
			}
			return nil
		})
		return err
	case dbcore.RedisSet:
		_, err := c.TxPipelined(ctx, func(p goredis.Pipeliner) error {
			p.Del(ctx, key)
			if len(v.Items) > 0 {
				p.SAdd(ctx, key, stringArgs(v.Items)...)
			}
			return nil
		})
		return err
	case dbcore.RedisZSet:
		zs := make([]goredis.Z, len(v.Items))
		for i, m := range v.Items {
			var score float64
			if i < len(v.Scores) {
				score = v.Scores[i]
			}
			zs[i] = goredis.Z{Member: m, Score: score}
		}
		_, err := c.TxPipelined(ctx, func(p goredis.Pipeliner) error {
			p.Del(ctx, key)
			if len(zs) > 0 {
				p.ZAdd(ctx, key, zs...)
			}
			return nil
		})
		return err
	case dbcore.RedisHash:
		_, err := c.TxPipelined(ctx, func(p goredis.Pipeliner) error {
			p.Del(ctx, key)
			if len(v.Hash) > 0 {
				p.HSet(ctx, key, v.Hash)
			}
			return nil
		})
		return err
	default:
		return dbcore.ErrNotSupported
	}
}

// SetElement overwrites one element of a key in place. Identity follows the
// grid layout exposed by GetValue: "key" (a string's single element), "field",
// "index" or "member" (sorted sets). Sets and streams have no writable value
// column. The TTL of the key is left untouched, unlike SetValue.
func (d *RedisDriver) SetElement(ctx context.Context, e dbcore.RedisElement) error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	c := d.client
	switch e.Identity {
	case "key":
		if e.Column != "value" {
			return fmt.Errorf("redis: string key has no column %q", e.Column)
		}
		// A plain SET would drop the key's expiry; KEEPTTL is what makes the
		// string case match the collection cases below.
		return c.Set(ctx, e.Key, e.Value, goredis.KeepTTL).Err()
	case "field":
		if e.Column != "value" {
			return fmt.Errorf("redis: hash field has no column %q", e.Column)
		}
		return c.HSet(ctx, e.Key, e.ID, e.Value).Err()
	case "index":
		if e.Column != "value" {
			return fmt.Errorf("redis: list item has no column %q", e.Column)
		}
		idx, err := strconv.Atoi(e.ID)
		if err != nil || idx < 0 {
			return fmt.Errorf("redis: invalid list index %q", e.ID)
		}
		return c.LSet(ctx, e.Key, int64(idx), e.Value).Err()
	case "member":
		if e.Column != "score" {
			return fmt.Errorf("redis: sorted-set member has no column %q", e.Column)
		}
		score, err := strconv.ParseFloat(e.Value, 64)
		if err != nil {
			return fmt.Errorf("redis: score %q is not a number", e.Value)
		}
		return c.ZAdd(ctx, e.Key, goredis.Z{Member: e.ID, Score: score}).Err()
	default:
		return fmt.Errorf("redis: element %q is not writable", e.Identity)
	}
}

// DeleteElement removes one element of a hash, set, sorted set or stream
// (HDEL/SREM/ZREM/XDEL). A missing element is a no-op, like the commands
// themselves. List items are index-addressed and Redis only deletes them by
// value (LREM), while a string's single element IS the key — both are rejected.
func (d *RedisDriver) DeleteElement(ctx context.Context, e dbcore.RedisElement) error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	c := d.client
	switch e.Identity {
	case "field":
		return c.HDel(ctx, e.Key, e.ID).Err()
	case "member":
		t, err := c.Type(ctx, e.Key).Result()
		if err != nil {
			return err
		}
		switch redisKeyType(t) {
		case dbcore.RedisSet:
			return c.SRem(ctx, e.Key, e.ID).Err()
		case dbcore.RedisZSet:
			return c.ZRem(ctx, e.Key, e.ID).Err()
		default:
			return fmt.Errorf("redis: key %q is not a set or sorted set", e.Key)
		}
	case "entry":
		// GetValue renders each entry as "id field=value ..." in its single
		// entry column, and that rendered string is what callers hand back as
		// the identity. XDEL addresses entries by id alone.
		id, _, _ := strings.Cut(e.ID, " ")
		return c.XDel(ctx, e.Key, id).Err()
	case "index":
		return fmt.Errorf("redis: list items are index-addressed; delete by value with LREM in the command editor")
	case "key":
		return fmt.Errorf("redis: a string key's element is the key itself; delete the key instead")
	default:
		return fmt.Errorf("redis: element %q cannot be deleted", e.Identity)
	}
}

// AppendRow adds one row to a set (SADD) or a stream (XADD). Both commands add
// an identity instead of overwriting a value, which is why an existing member
// or entry is not editable through SetElement. A stream entry needs at least
// one field and unique field names: XADD stores duplicates, but reading the
// entry back into a map would keep only the last value. The returned string is
// the member, or the stream ID the server assigned (or echoed). The key's TTL
// is untouched.
func (d *RedisDriver) AppendRow(ctx context.Context, row dbcore.RedisRow) (string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	c := d.client
	if row.Key == "" {
		return "", errors.New("redis: empty key")
	}
	switch row.Kind {
	case dbcore.RedisSet:
		if row.Member == "" {
			return "", errors.New("redis: empty set member")
		}
		if err := c.SAdd(ctx, row.Key, row.Member).Err(); err != nil {
			return "", err
		}
		return row.Member, nil
	case dbcore.RedisStream:
		if len(row.Fields) == 0 {
			return "", errors.New("redis: stream entry needs at least one field")
		}
		values := make([]interface{}, 0, len(row.Fields)*2)
		seen := make(map[string]bool, len(row.Fields))
		for _, f := range row.Fields {
			if f.Name == "" {
				return "", errors.New("redis: stream field name must not be empty")
			}
			if seen[f.Name] {
				return "", fmt.Errorf("redis: duplicate stream field %q", f.Name)
			}
			seen[f.Name] = true
			values = append(values, f.Name, f.Value)
		}
		id := strings.TrimSpace(row.ID)
		if id == "" {
			id = "*"
		}
		return c.XAdd(ctx, &goredis.XAddArgs{Stream: row.Key, ID: id, Values: values}).Result()
	default:
		return "", fmt.Errorf("redis: %q rows are not appendable", row.Kind)
	}
}

// KeyMeta reports the key's type and remaining lifetime. A key that never
// expires reports HasTTL=false.
func (d *RedisDriver) KeyMeta(ctx context.Context, key string) (dbcore.KeyMeta, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	c := d.client
	t, err := c.Type(ctx, key).Result()
	if err != nil {
		return dbcore.KeyMeta{}, err
	}
	kind := redisKeyType(t)
	if kind == dbcore.RedisNone {
		return dbcore.KeyMeta{}, fmt.Errorf("redis: key %q not found", key)
	}
	meta := dbcore.KeyMeta{Kind: kind}
	ttl, err := c.PTTL(ctx, key).Result()
	if err != nil {
		return dbcore.KeyMeta{}, err
	}
	// PTTL returns -1 (no expiry) or -2 (missing) as bare negative durations.
	if ttl > 0 {
		meta.TTL, meta.HasTTL = ttl, true
	}
	return meta, nil
}

// DeleteKeys removes keys and ignores missing ones, like DEL.
func (d *RedisDriver) DeleteKeys(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.client.Del(ctx, keys...).Err()
}

// RenameKey renames a key without overwriting an existing target (RENAMENX),
// so a typo cannot silently destroy another key.
func (d *RedisDriver) RenameKey(ctx context.Context, oldKey, newKey string) error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	ok, err := d.client.RenameNX(ctx, oldKey, newKey).Result()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("redis: target key %q already exists (or %q is missing)", newKey, oldKey)
	}
	return nil
}

// SetTTL sets the key's remaining lifetime, or clears the expiry when ttl is
// zero or negative (PERSIST). Setting a TTL on a missing key is an error;
// clearing one that does not exist is a no-op.
func (d *RedisDriver) SetTTL(ctx context.Context, key string, ttl time.Duration) error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	c := d.client
	if ttl <= 0 {
		return c.Persist(ctx, key).Err()
	}
	ok, err := c.PExpire(ctx, key, ttl).Result()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("redis: key %q not found", key)
	}
	return nil
}

// ExecCommand runs an arbitrary command and renders the reply as text. A nil
// reply (e.g. GET on a missing key) is a valid reply, not an error.
func (d *RedisDriver) ExecCommand(ctx context.Context, args []string) (dbcore.RedisReply, error) {
	if len(args) == 0 {
		return dbcore.RedisReply{}, fmt.Errorf("redis: empty command")
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	res, err := d.client.Do(ctx, stringArgs(args)...).Result()
	if err != nil && !errors.Is(err, goredis.Nil) {
		return dbcore.RedisReply{}, err
	}
	return dbcore.RedisReply{Raw: formatReply(res)}, nil
}

// ExecCommandTyped is ExecCommand with the reply as structure: simple/bulk
// strings collapse to bulk_string, nil is null, and errors (including ones
// nested in arrays) keep their kind. An empty command is reported as an
// error-kind reply, not a Go error, matching the RESP view a command editor
// shows.
func (d *RedisDriver) ExecCommandTyped(ctx context.Context, args []string) (dbcore.RedisTypedReply, error) {
	if len(args) == 0 {
		return dbcore.RedisTypedReply{Kind: dbcore.RedisReplyKindError, Value: "empty command"}, nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	res, err := d.client.Do(ctx, stringArgs(args)...).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			// GET on a missing key returns null, not an error.
			return dbcore.RedisTypedReply{Kind: dbcore.RedisReplyKindNull}, nil
		}
		return dbcore.RedisTypedReply{Kind: dbcore.RedisReplyKindError, Value: err.Error()}, nil
	}
	return typedReply(res), nil
}

// typedReply maps a go-redis raw RESP value onto the structured reply kinds.
func typedReply(v any) dbcore.RedisTypedReply {
	switch r := v.(type) {
	case nil:
		return dbcore.RedisTypedReply{Kind: dbcore.RedisReplyKindNull}
	case string:
		return dbcore.RedisTypedReply{Kind: dbcore.RedisReplyKindBulkString, Value: r}
	case []byte:
		return dbcore.RedisTypedReply{Kind: dbcore.RedisReplyKindBulkString, Value: string(r)}
	case int64:
		return dbcore.RedisTypedReply{Kind: dbcore.RedisReplyKindInteger, Value: r}
	case error:
		// proto.RedisError, as found nested inside arrays.
		return dbcore.RedisTypedReply{Kind: dbcore.RedisReplyKindError, Value: r.Error()}
	case []any:
		arr := make([]dbcore.RedisTypedReply, len(r))
		for i, e := range r {
			arr[i] = typedReply(e)
		}
		return dbcore.RedisTypedReply{Kind: dbcore.RedisReplyKindArray, Value: arr}
	default:
		return dbcore.RedisTypedReply{Kind: dbcore.RedisReplyKindBulkString, Value: fmt.Sprint(r)}
	}
}

func redisKeyType(t string) dbcore.RedisKeyType {
	switch t {
	case "none":
		return dbcore.RedisNone
	case "string":
		return dbcore.RedisStr
	case "list":
		return dbcore.RedisList
	case "set":
		return dbcore.RedisSet
	case "zset":
		return dbcore.RedisZSet
	case "hash":
		return dbcore.RedisHash
	case "stream":
		return dbcore.RedisStream
	default:
		return dbcore.RedisKeyType(t)
	}
}

func stringArgs(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func formatReply(v any) string {
	switch x := v.(type) {
	case nil:
		return "(nil)"
	case string:
		return x
	case []byte:
		return string(x)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = formatReply(e)
		}
		return strings.Join(parts, " ")
	default:
		return fmt.Sprint(x)
	}
}

// KeyPageReader implementation: windowed reads for large collection keys.
// Everything below holds the same read lock as GetValue so a concurrent
// SelectDB cannot swap the client mid-read.

func (d *RedisDriver) ListRange(ctx context.Context, key string, start, stop int64) ([]string, int64, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	c := d.client
	length, err := c.LLen(ctx, key).Result()
	if err != nil {
		return nil, 0, err
	}
	items, err := c.LRange(ctx, key, start, stop).Result()
	if err != nil {
		return nil, 0, err
	}
	return items, length, nil
}

func (d *RedisDriver) HashPage(ctx context.Context, key string, cursor uint64, count int64) ([]dbcore.RedisField, uint64, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	raw, next, err := d.client.HScan(ctx, key, cursor, "", count).Result()
	if err != nil {
		return nil, 0, err
	}
	fields := make([]dbcore.RedisField, 0, len(raw)/2)
	// HSCAN returns field/value pairs flattened; a count is a hint and the
	// walk is per-bucket, so a trailing odd element means a partial write.
	for i := 0; i+1 < len(raw); i += 2 {
		fields = append(fields, dbcore.RedisField{Name: raw[i], Value: raw[i+1]})
	}
	return fields, next, nil
}

func (d *RedisDriver) SetPage(ctx context.Context, key string, cursor uint64, count int64) ([]string, uint64, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	members, next, err := d.client.SScan(ctx, key, cursor, "", count).Result()
	if err != nil {
		return nil, 0, err
	}
	sort.Strings(members)
	return members, next, nil
}

func (d *RedisDriver) ZSetRange(ctx context.Context, key string, start, stop int64) ([]string, []float64, int64, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	c := d.client
	length, err := c.ZCard(ctx, key).Result()
	if err != nil {
		return nil, nil, 0, err
	}
	zs, err := c.ZRangeWithScores(ctx, key, start, stop).Result()
	if err != nil {
		return nil, nil, 0, err
	}
	members := make([]string, 0, len(zs))
	scores := make([]float64, 0, len(zs))
	for _, z := range zs {
		members = append(members, fmt.Sprint(z.Member))
		scores = append(scores, z.Score)
	}
	return members, scores, length, nil
}

func (d *RedisDriver) StreamRange(ctx context.Context, key, afterID string, count int64) ([]dbcore.RedisStreamEntry, int64, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	c := d.client
	length, err := c.XLen(ctx, key).Result()
	if err != nil {
		return nil, 0, err
	}
	start := afterID
	if start == "" {
		start = "-"
	} else {
		// XRANGE's start is inclusive; the "(" prefix (Redis 6.2+) excludes
		// afterID itself. Servers that do not understand the prefix report a
		// syntax error, which the fallback below handles by reading one extra
		// entry and dropping it.
		msgs, perr := c.XRangeN(ctx, key, "("+start, "+", count).Result()
		if perr == nil {
			return streamEntries(msgs), length, nil
		}
	}
	// The +1 read is only for the afterID trim; from the start, count is exact.
	fetch := count + 1
	if afterID == "" {
		fetch = count
	}
	msgs, err := c.XRangeN(ctx, key, start, "+", fetch).Result()
	if err != nil {
		return nil, 0, err
	}
	if afterID != "" && len(msgs) > 0 && msgs[0].ID == afterID {
		msgs = msgs[1:]
	}
	return streamEntries(msgs), length, nil
}

func streamEntries(msgs []goredis.XMessage) []dbcore.RedisStreamEntry {
	out := make([]dbcore.RedisStreamEntry, 0, len(msgs))
	for _, m := range msgs {
		entry := dbcore.RedisStreamEntry{ID: m.ID}
		for k, val := range m.Values {
			entry.Fields = append(entry.Fields, dbcore.RedisField{Name: k, Value: fmt.Sprint(val)})
		}
		sort.Slice(entry.Fields, func(i, j int) bool { return entry.Fields[i].Name < entry.Fields[j].Name })
		out = append(out, entry)
	}
	return out
}

// Compile-time assertion: the redis driver serves large keys in windows.
var _ dbcore.KeyPageReader = (*RedisDriver)(nil)
