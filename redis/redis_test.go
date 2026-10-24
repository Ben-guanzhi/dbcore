package redis

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/dbcore/dbcore"
)

func newDriver(t *testing.T) *RedisDriver {
	t.Helper()
	mr := miniredis.RunT(t)
	d, err := OpenOptions(&goredis.Options{Addr: mr.Addr(), DisableIdentity: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestStringRoundTrip(t *testing.T) {
	ctx := context.Background()
	d := newDriver(t)

	if err := d.SetValue(ctx, "greeting", dbcore.RedisValue{Type: dbcore.RedisStr, Str: "hello"}); err != nil {
		t.Fatalf("SetValue: %v", err)
	}
	if kt, err := d.KeyType(ctx, "greeting"); err != nil || kt != dbcore.RedisStr {
		t.Fatalf("KeyType = %q, %v", kt, err)
	}
	v, err := d.GetValue(ctx, "greeting")
	if err != nil {
		t.Fatalf("GetValue: %v", err)
	}
	if v.Type != dbcore.RedisStr || v.Str != "hello" {
		t.Fatalf("GetValue = %+v, want string hello", v)
	}

	// Int-only values (Str empty) are written as decimal text.
	if err := d.SetValue(ctx, "answer", dbcore.RedisValue{Type: dbcore.RedisStr, Int: 42}); err != nil {
		t.Fatalf("SetValue int: %v", err)
	}
	v, err = d.GetValue(ctx, "answer")
	if err != nil || v.Str != "42" || v.Int != 42 {
		t.Fatalf("GetValue int = %+v, %v", v, err)
	}
}

func TestCollectionsRoundTrip(t *testing.T) {
	ctx := context.Background()
	d := newDriver(t)

	cases := []dbcore.RedisValue{
		{Type: dbcore.RedisList, Items: []string{"a", "b", "c"}},
		{Type: dbcore.RedisSet, Items: []string{"y", "x"}},
		{Type: dbcore.RedisHash, Hash: map[string]string{"f1": "v1", "f2": "v2"}},
		{Type: dbcore.RedisZSet, Items: []string{"low", "high"}, Scores: []float64{1, 9}},
	}
	for i, want := range cases {
		key := "k" + string(rune('a'+i))
		if err := d.SetValue(ctx, key, want); err != nil {
			t.Fatalf("%s SetValue: %v", want.Type, err)
		}
		got, err := d.GetValue(ctx, key)
		if err != nil {
			t.Fatalf("%s GetValue: %v", want.Type, err)
		}
		if got.Type != want.Type {
			t.Fatalf("%s type = %q", want.Type, got.Type)
		}
		switch want.Type {
		case dbcore.RedisList:
			if !reflect.DeepEqual(got.Items, want.Items) {
				t.Errorf("list items = %v", got.Items)
			}
		case dbcore.RedisSet:
			// SMEMBERS is unordered; the driver sorts for stable output.
			if !reflect.DeepEqual(got.Items, []string{"x", "y"}) {
				t.Errorf("set items = %v", got.Items)
			}
		case dbcore.RedisHash:
			if !reflect.DeepEqual(got.Hash, want.Hash) {
				t.Errorf("hash = %v", got.Hash)
			}
		case dbcore.RedisZSet:
			if !reflect.DeepEqual(got.Items, want.Items) || !reflect.DeepEqual(got.Scores, want.Scores) {
				t.Errorf("zset = %v / %v", got.Items, got.Scores)
			}
		}
	}
}

func TestScanKeysAndSelectDB(t *testing.T) {
	ctx := context.Background()
	d := newDriver(t)

	for _, k := range []string{"user:1", "user:2", "other"} {
		if err := d.SetValue(ctx, k, dbcore.RedisValue{Type: dbcore.RedisStr, Str: "x"}); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}
	page, err := d.ScanKeys(ctx, 0, "user:*", 10)
	if err != nil {
		t.Fatalf("ScanKeys: %v", err)
	}
	if len(page.Keys) != 2 {
		t.Fatalf("ScanKeys keys = %v", page.Keys)
	}

	if err := d.SelectDB(ctx, 1); err != nil {
		t.Fatalf("SelectDB(1): %v", err)
	}
	if _, err := d.GetValue(ctx, "user:1"); err == nil {
		t.Fatalf("db 1 should not see db 0 keys")
	}
	if err := d.SetValue(ctx, "user:1", dbcore.RedisValue{Type: dbcore.RedisStr, Str: "db1"}); err != nil {
		t.Fatalf("SetValue db1: %v", err)
	}
	if err := d.SelectDB(ctx, 0); err != nil {
		t.Fatalf("SelectDB(0): %v", err)
	}
	v, err := d.GetValue(ctx, "user:1")
	if err != nil || v.Str != "x" {
		t.Fatalf("db 0 value = %+v, %v", v, err)
	}
}

func TestExecCommand(t *testing.T) {
	ctx := context.Background()
	d := newDriver(t)

	if _, err := d.ExecCommand(ctx, []string{"SET", "raw", "value"}); err != nil {
		t.Fatalf("SET: %v", err)
	}
	reply, err := d.ExecCommand(ctx, []string{"GET", "raw"})
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if reply.Raw != "value" {
		t.Fatalf("GET reply = %q", reply.Raw)
	}
	reply, err = d.ExecCommand(ctx, []string{"LRANGE", "missing", "0", "-1"})
	if err != nil || reply.Raw != "" {
		t.Fatalf("LRANGE missing = %q, %v", reply.Raw, err)
	}
	// A nil reply is a valid reply, not an error.
	reply, err = d.ExecCommand(ctx, []string{"GET", "missing"})
	if err != nil || reply.Raw != "(nil)" {
		t.Fatalf("GET missing = %q, %v, want (nil)", reply.Raw, err)
	}
	if _, err := d.ExecCommand(ctx, nil); err == nil || !strings.Contains(err.Error(), "empty command") {
		t.Fatalf("empty command err = %v", err)
	}
}

func TestScanKeysEmptyPatternMeansAll(t *testing.T) {
	ctx := context.Background()
	d := newDriver(t)
	for _, k := range []string{"a", "b"} {
		if err := d.SetValue(ctx, k, dbcore.RedisValue{Type: dbcore.RedisStr, Str: "x"}); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}
	page, err := d.ScanKeys(ctx, 0, "", 0)
	if err != nil {
		t.Fatalf("ScanKeys: %v", err)
	}
	if len(page.Keys) != 2 {
		t.Fatalf("ScanKeys keys = %v, want both keys", page.Keys)
	}
}

func TestSetValueReplacesCollection(t *testing.T) {
	ctx := context.Background()
	d := newDriver(t)
	if err := d.SetValue(ctx, "list", dbcore.RedisValue{Type: dbcore.RedisList, Items: []string{"a", "b", "c"}}); err != nil {
		t.Fatalf("first SetValue: %v", err)
	}
	if err := d.SetValue(ctx, "list", dbcore.RedisValue{Type: dbcore.RedisList, Items: []string{"z"}}); err != nil {
		t.Fatalf("second SetValue: %v", err)
	}
	v, err := d.GetValue(ctx, "list")
	if err != nil {
		t.Fatalf("GetValue: %v", err)
	}
	if !reflect.DeepEqual(v.Items, []string{"z"}) {
		t.Fatalf("after replace items = %v, want [z]", v.Items)
	}
}

func TestSelectDBConcurrentReads(t *testing.T) {
	ctx := context.Background()
	d := newDriver(t)

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if err := d.Ping(ctx); err != nil {
					errs <- err
				}
			}
		}()
	}
	for j := 0; j < 20; j++ {
		if err := d.SelectDB(ctx, j%2); err != nil {
			t.Fatalf("SelectDB(%d): %v", j%2, err)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent ping: %v", err)
	}
}

func TestMissingKeyAndUnsupportedSet(t *testing.T) {
	ctx := context.Background()
	d := newDriver(t)

	if _, err := d.GetValue(ctx, "nope"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing key err = %v", err)
	}
	if err := d.SetValue(ctx, "s", dbcore.RedisValue{Type: dbcore.RedisStream}); !errors.Is(err, dbcore.ErrNotSupported) {
		t.Fatalf("stream SetValue err = %v, want ErrNotSupported", err)
	}
}

func TestSetElement(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	d, err := OpenOptions(&goredis.Options{Addr: mr.Addr(), DisableIdentity: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	for _, seed := range []struct {
		key string
		v   dbcore.RedisValue
	}{
		{"s", dbcore.RedisValue{Type: dbcore.RedisStr, Str: "old"}},
		{"h", dbcore.RedisValue{Type: dbcore.RedisHash, Hash: map[string]string{"a": "1", "b": "2"}}},
		{"l", dbcore.RedisValue{Type: dbcore.RedisList, Items: []string{"x", "y"}}},
		{"z", dbcore.RedisValue{Type: dbcore.RedisZSet, Items: []string{"m"}, Scores: []float64{1}}},
	} {
		if err := d.SetValue(ctx, seed.key, seed.v); err != nil {
			t.Fatalf("seed %s: %v", seed.key, err)
		}
	}
	mr.SetTTL("h", time.Hour)
	mr.SetTTL("s", time.Hour)

	edits := []struct {
		name string
		e    dbcore.RedisElement
	}{
		{"string", dbcore.RedisElement{Key: "s", Identity: "key", ID: "s", Column: "value", Value: "new"}},
		{"hash", dbcore.RedisElement{Key: "h", Identity: "field", ID: "a", Column: "value", Value: "9"}},
		{"list", dbcore.RedisElement{Key: "l", Identity: "index", ID: "1", Column: "value", Value: "Y"}},
		{"zset", dbcore.RedisElement{Key: "z", Identity: "member", ID: "m", Column: "score", Value: "2.5"}},
	}
	for _, tc := range edits {
		if err := d.SetElement(ctx, tc.e); err != nil {
			t.Fatalf("%s SetElement: %v", tc.name, err)
		}
	}

	if v, err := d.GetValue(ctx, "s"); err != nil || v.Str != "new" {
		t.Errorf("string = %+v, %v", v, err)
	}
	if v, err := d.GetValue(ctx, "h"); err != nil || v.Hash["a"] != "9" || v.Hash["b"] != "2" {
		t.Errorf("hash = %+v, %v", v, err)
	}
	if v, err := d.GetValue(ctx, "l"); err != nil || !reflect.DeepEqual(v.Items, []string{"x", "Y"}) {
		t.Errorf("list = %+v, %v", v, err)
	}
	if v, err := d.GetValue(ctx, "z"); err != nil || len(v.Scores) != 1 || v.Scores[0] != 2.5 {
		t.Errorf("zset = %+v, %v", v, err)
	}
	// The targeted write must not touch the key's TTL (SetValue would).
	if ttl := mr.TTL("h"); ttl <= 0 {
		t.Errorf("hash TTL was dropped by SetElement: %v", ttl)
	}
	if ttl := mr.TTL("s"); ttl <= 0 {
		t.Errorf("string TTL was dropped by SetElement: %v", ttl)
	}

	bad := []dbcore.RedisElement{
		{Key: "l", Identity: "index", ID: "abc", Column: "value", Value: "x"},
		{Key: "l", Identity: "index", ID: "-1", Column: "value", Value: "x"},
		{Key: "z", Identity: "member", ID: "m", Column: "score", Value: "not-a-number"},
		{Key: "h", Identity: "field", ID: "a", Column: "score", Value: "1"},
		{Key: "s", Identity: "key", ID: "s", Column: "value2", Value: "x"},
		{Key: "x", Identity: "entry", ID: "", Column: "entry", Value: "x"},
	}
	for _, e := range bad {
		if err := d.SetElement(ctx, e); err == nil {
			t.Errorf("SetElement(%+v) should fail", e)
		}
	}
}

func TestAppendRow(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	d, err := OpenOptions(&goredis.Options{Addr: mr.Addr(), DisableIdentity: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if _, err := mr.SAdd("tags", "a"); err != nil {
		t.Fatal(err)
	}
	mr.SetTTL("tags", time.Hour)
	if _, err := mr.XAdd("events", "1-1", []string{"kind", "seed"}); err != nil {
		t.Fatal(err)
	}
	mr.SetTTL("events", time.Hour)

	member, err := d.AppendRow(ctx, dbcore.RedisRow{Key: "tags", Kind: dbcore.RedisSet, Member: "b"})
	if err != nil || member != "b" {
		t.Fatalf("AppendRow set = %q, %v", member, err)
	}
	v, err := d.GetValue(ctx, "tags")
	if err != nil || !reflect.DeepEqual(v.Items, []string{"a", "b"}) {
		t.Errorf("set = %+v, %v", v, err)
	}
	// A duplicate member is a no-op, not an error.
	if member, err := d.AppendRow(ctx, dbcore.RedisRow{Key: "tags", Kind: dbcore.RedisSet, Member: "b"}); err != nil || member != "b" {
		t.Errorf("duplicate member = %q, %v", member, err)
	}
	if ttl := mr.TTL("tags"); ttl <= 0 {
		t.Errorf("set TTL was dropped by AppendRow: %v", ttl)
	}

	// An empty ID asks the server to assign one; the returned id names it.
	assigned, err := d.AppendRow(ctx, dbcore.RedisRow{
		Key: "events", Kind: dbcore.RedisStream,
		Fields: []dbcore.RedisField{{Name: "kind", Value: "new"}, {Name: "n", Value: "1"}},
	})
	if err != nil || assigned == "" || assigned == "1-1" {
		t.Fatalf("AppendRow stream = %q, %v", assigned, err)
	}
	v, err = d.GetValue(ctx, "events")
	if err != nil || len(v.Items) != 2 {
		t.Fatalf("stream = %+v, %v", v, err)
	}
	if got := v.Items[1]; !strings.HasPrefix(got, assigned+" ") || !strings.Contains(got, "kind=new") || !strings.Contains(got, "n=1") {
		t.Errorf("stream entry = %q, want id %q with both fields", got, assigned)
	}
	if id, err := d.AppendRow(ctx, dbcore.RedisRow{
		Key: "events", Kind: dbcore.RedisStream, ID: "9999999999999-0",
		Fields: []dbcore.RedisField{{Name: "kind", Value: "explicit"}},
	}); err != nil || id != "9999999999999-0" {
		t.Errorf("explicit id = %q, %v", id, err)
	}
	if ttl := mr.TTL("events"); ttl <= 0 {
		t.Errorf("stream TTL was dropped by AppendRow: %v", ttl)
	}

	bad := []dbcore.RedisRow{
		{Kind: dbcore.RedisSet, Member: "x"},
		{Key: "tags", Kind: dbcore.RedisSet},
		{Key: "events", Kind: dbcore.RedisStream},
		{Key: "events", Kind: dbcore.RedisStream, Fields: []dbcore.RedisField{{Value: "x"}}},
		{Key: "events", Kind: dbcore.RedisStream, Fields: []dbcore.RedisField{{Name: "k", Value: "1"}, {Name: "k", Value: "2"}}},
		{Key: "h", Kind: dbcore.RedisHash, Member: "x"},
	}
	for _, row := range bad {
		if _, err := d.AppendRow(ctx, row); err == nil {
			t.Errorf("AppendRow(%+v) should fail", row)
		}
	}
}

func TestDeleteElement(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	d, err := OpenOptions(&goredis.Options{Addr: mr.Addr(), DisableIdentity: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	for _, seed := range []struct {
		key string
		v   dbcore.RedisValue
	}{
		{"h", dbcore.RedisValue{Type: dbcore.RedisHash, Hash: map[string]string{"a": "1", "b": "2"}}},
		{"s", dbcore.RedisValue{Type: dbcore.RedisSet, Items: []string{"x", "y"}}},
		{"z", dbcore.RedisValue{Type: dbcore.RedisZSet, Items: []string{"low", "high"}, Scores: []float64{1, 9}}},
		{"l", dbcore.RedisValue{Type: dbcore.RedisList, Items: []string{"x", "y"}}},
		{"plain", dbcore.RedisValue{Type: dbcore.RedisStr, Str: "v"}},
	} {
		if err := d.SetValue(ctx, seed.key, seed.v); err != nil {
			t.Fatalf("seed %s: %v", seed.key, err)
		}
	}
	reply, err := d.ExecCommand(ctx, []string{"XADD", "st", "*", "f", "v"})
	if err != nil {
		t.Fatalf("XADD: %v", err)
	}
	entryID := reply.Raw
	mr.SetTTL("h", time.Hour)

	deletes := []dbcore.RedisElement{
		{Key: "h", Identity: "field", ID: "a"},
		{Key: "s", Identity: "member", ID: "x"},
		{Key: "z", Identity: "member", ID: "low"},
		// The grid hands back GetValue's rendered entry, not the bare id.
		{Key: "st", Identity: "entry", ID: entryID + " f=v"},
	}
	for _, e := range deletes {
		if err := d.DeleteElement(ctx, e); err != nil {
			t.Fatalf("DeleteElement(%+v): %v", e, err)
		}
	}

	if v, err := d.GetValue(ctx, "h"); err != nil || !reflect.DeepEqual(v.Hash, map[string]string{"b": "2"}) {
		t.Errorf("hash after delete = %+v, %v", v, err)
	}
	if v, err := d.GetValue(ctx, "s"); err != nil || !reflect.DeepEqual(v.Items, []string{"y"}) {
		t.Errorf("set after delete = %+v, %v", v, err)
	}
	if v, err := d.GetValue(ctx, "z"); err != nil || !reflect.DeepEqual(v.Items, []string{"high"}) {
		t.Errorf("zset after delete = %+v, %v", v, err)
	}
	if v, err := d.GetValue(ctx, "st"); err != nil || len(v.Items) != 0 {
		t.Errorf("stream after delete = %+v, %v", v, err)
	}
	// The targeted delete must leave the key's TTL alone.
	if ttl := mr.TTL("h"); ttl <= 0 {
		t.Errorf("hash TTL was dropped by DeleteElement: %v", ttl)
	}
	// Deleting an element that is already gone is a no-op.
	if err := d.DeleteElement(ctx, dbcore.RedisElement{Key: "h", Identity: "field", ID: "a"}); err != nil {
		t.Errorf("repeat delete = %v, want nil", err)
	}

	bad := []dbcore.RedisElement{
		{Key: "l", Identity: "index", ID: "1"},
		{Key: "plain", Identity: "key", ID: "plain"},
		{Key: "plain", Identity: "member", ID: "m"}, // not a set/zset
		{Key: "h", Identity: "bogus", ID: "a"},
	}
	for _, e := range bad {
		if err := d.DeleteElement(ctx, e); err == nil {
			t.Errorf("DeleteElement(%+v) should fail", e)
		}
	}
}

func TestKeyAdmin(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	d, err := OpenOptions(&goredis.Options{Addr: mr.Addr(), DisableIdentity: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if err := d.SetValue(ctx, "plain", dbcore.RedisValue{Type: dbcore.RedisStr, Str: "x"}); err != nil {
		t.Fatalf("seed plain: %v", err)
	}
	if err := d.SetValue(ctx, "h", dbcore.RedisValue{Type: dbcore.RedisHash, Hash: map[string]string{"a": "1"}}); err != nil {
		t.Fatalf("seed h: %v", err)
	}
	mr.SetTTL("h", time.Hour)

	meta, err := d.KeyMeta(ctx, "plain")
	if err != nil {
		t.Fatalf("KeyMeta plain: %v", err)
	}
	if meta.Kind != dbcore.RedisStr || meta.HasTTL {
		t.Errorf("plain meta = %+v, want string without TTL", meta)
	}

	meta, err = d.KeyMeta(ctx, "h")
	if err != nil {
		t.Fatalf("KeyMeta h: %v", err)
	}
	if meta.Kind != dbcore.RedisHash || !meta.HasTTL || meta.TTL <= 0 || meta.TTL > time.Hour {
		t.Errorf("hash meta = %+v, want hash with TTL <= 1h", meta)
	}

	if _, err := d.GetValue(ctx, "missing"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("KeyMeta missing err = %v", err)
	}

	// TTL can be set and cleared; a missing key refuses a new TTL.
	if err := d.SetTTL(ctx, "plain", time.Minute); err != nil {
		t.Fatalf("SetTTL: %v", err)
	}
	if meta, err := d.KeyMeta(ctx, "plain"); err != nil || !meta.HasTTL || meta.TTL <= 0 || meta.TTL > time.Minute {
		t.Errorf("after SetTTL meta = %+v, %v", meta, err)
	}
	if err := d.SetTTL(ctx, "plain", 0); err != nil {
		t.Fatalf("SetTTL persist: %v", err)
	}
	if meta, err := d.KeyMeta(ctx, "plain"); err != nil || meta.HasTTL {
		t.Errorf("after persist meta = %+v, %v", meta, err)
	}
	if err := d.SetTTL(ctx, "missing", time.Second); err == nil {
		t.Error("SetTTL on a missing key should fail")
	}

	// Rename keeps the TTL; an existing target is refused.
	if err := d.RenameKey(ctx, "h", "h2"); err != nil {
		t.Fatalf("RenameKey: %v", err)
	}
	if _, err := d.GetValue(ctx, "h"); err == nil {
		t.Error("old key still readable after rename")
	}
	if meta, err := d.KeyMeta(ctx, "h2"); err != nil || meta.Kind != dbcore.RedisHash || !meta.HasTTL {
		t.Errorf("renamed meta = %+v, %v", meta, err)
	}
	if err := d.RenameKey(ctx, "plain", "h2"); err == nil {
		t.Error("RenameKey onto existing key should fail")
	}
	if v, err := d.GetValue(ctx, "plain"); err != nil || v.Str != "x" {
		t.Errorf("plain changed by failed rename = %+v, %v", v, err)
	}
	if err := d.RenameKey(ctx, "nope", "other"); err == nil {
		t.Error("RenameKey of missing key should fail")
	}

	if err := d.DeleteKeys(ctx, "plain", "h2", "already-gone"); err != nil {
		t.Fatalf("DeleteKeys: %v", err)
	}
	for _, k := range []string{"plain", "h2"} {
		if _, err := d.GetValue(ctx, k); err == nil {
			t.Errorf("%s still readable after delete", k)
		}
	}
	if err := d.DeleteKeys(ctx); err != nil {
		t.Errorf("DeleteKeys() = %v, want nil", err)
	}
}

func TestCapabilities(t *testing.T) {
	d := newDriver(t)
	if d.Kind() != dbcore.KindRedis {
		t.Errorf("Kind = %q", d.Kind())
	}
	if _, ok := d.AsSQL(); ok {
		t.Error("AsSQL should be false")
	}
	if kv, ok := d.AsKV(); !ok || kv != d {
		t.Error("AsKV should return the driver")
	}
	if err := d.Ping(context.Background()); err != nil {
		t.Errorf("Ping: %v", err)
	}
}
