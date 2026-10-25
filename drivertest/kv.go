package drivertest

// The shared KV contract (P3-8): the Redis-style surface every KV driver
// implements, plus the optional capabilities (ElementWriter, ElementDeleter,
// RowAppender, KeyAdmin, KeyPageReader, KeyScanner, TypedCommander) promoted
// one subtest at a time. The in-kernel driver is covered by miniredis; a
// client's own KV glue can run the identical suite with KVSpec.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dbcore/dbcore"
)

// KVSpec describes how to open the KV driver for the contract run.
type KVSpec struct {
	// Open must return a driver whose AsKV() succeeds. The suite closes it.
	Open func(ctx context.Context) (dbcore.KVDriver, error)
	// Prefix isolates every key the suite writes (default "dbcore:kvct:").
	Prefix string
	// TTL is the lifetime used by the TTL subtest (default 1 hour).
	TTL time.Duration
}

// RunKV executes the KV contract assertions against spec.
func RunKV(t *testing.T, spec KVSpec) {
	t.Helper()
	if spec.Open == nil {
		t.Fatalf("drivertest.KVSpec.Open must be set")
	}
	prefix := spec.Prefix
	if prefix == "" {
		prefix = "dbcore:kvct:"
	}
	ttl := spec.TTL
	if ttl <= 0 {
		ttl = time.Hour
	}
	ctx := context.Background()

	drv, err := spec.Open(ctx)
	if err != nil {
		t.Fatalf("open KV driver: %v", err)
	}
	defer drv.Close()

	kv, ok := drv.AsKV()
	if !ok {
		t.Fatalf("driver %s does not expose KVDriver", drv.Kind())
	}

	var written []string
	key := func(suffix string) string {
		k := prefix + suffix
		written = append(written, k)
		return k
	}
	cleanup := func() {
		if len(written) == 0 {
			return
		}
		if admin, ok := dbcore.AsKeyAdmin(kv); ok {
			_ = admin.DeleteKeys(ctx, written...)
			return
		}
		_, _ = kv.ExecCommand(ctx, append([]string{"DEL"}, written...))
	}
	defer cleanup()

	t.Run("kv_ping", func(t *testing.T) {
		if err := kv.Ping(ctx); err != nil {
			t.Fatalf("Ping: %v", err)
		}
	})

	t.Run("kv_string_round_trip", func(t *testing.T) {
		k := key("str")
		if err := kv.SetValue(ctx, k, dbcore.RedisValue{Type: dbcore.RedisStr, Str: "hello"}); err != nil {
			t.Fatalf("SetValue: %v", err)
		}
		if kind, err := kv.KeyType(ctx, k); err != nil || kind != dbcore.RedisStr {
			t.Fatalf("KeyType = %q, %v, want string", kind, err)
		}
		v, err := kv.GetValue(ctx, k)
		if err != nil {
			t.Fatalf("GetValue: %v", err)
		}
		if v.Type != dbcore.RedisStr || v.Str != "hello" {
			t.Fatalf("GetValue = %+v, want string hello", v)
		}
		// An Int-only value is written as its decimal text.
		if err := kv.SetValue(ctx, k, dbcore.RedisValue{Type: dbcore.RedisStr, Int: 42}); err != nil {
			t.Fatalf("SetValue(int): %v", err)
		}
		if v, err = kv.GetValue(ctx, k); err != nil || v.Str != "42" || v.Int != 42 {
			t.Fatalf("GetValue(int) = %+v, %v, want 42", v, err)
		}
	})

	t.Run("kv_collections_round_trip", func(t *testing.T) {
		listKey := key("list")
		if err := kv.SetValue(ctx, listKey, dbcore.RedisValue{Type: dbcore.RedisList, Items: []string{"a", "b", "c"}}); err != nil {
			t.Fatalf("list SetValue: %v", err)
		}
		if v, err := kv.GetValue(ctx, listKey); err != nil || strings.Join(v.Items, ",") != "a,b,c" {
			t.Fatalf("list GetValue = %+v, %v, want a,b,c", v, err)
		}

		setKey := key("set")
		if err := kv.SetValue(ctx, setKey, dbcore.RedisValue{Type: dbcore.RedisSet, Items: []string{"y", "x"}}); err != nil {
			t.Fatalf("set SetValue: %v", err)
		}
		if v, err := kv.GetValue(ctx, setKey); err != nil || strings.Join(v.Items, ",") != "x,y" {
			t.Fatalf("set GetValue = %+v, %v, want sorted x,y", v, err)
		}

		zKey := key("zset")
		if err := kv.SetValue(ctx, zKey, dbcore.RedisValue{
			Type: dbcore.RedisZSet, Items: []string{"m1", "m2"}, Scores: []float64{2, 1},
		}); err != nil {
			t.Fatalf("zset SetValue: %v", err)
		}
		if v, err := kv.GetValue(ctx, zKey); err != nil || strings.Join(v.Items, ",") != "m2,m1" ||
			len(v.Scores) != 2 || v.Scores[0] != 1 || v.Scores[1] != 2 {
			t.Fatalf("zset GetValue = %+v, %v, want m2(1),m1(2)", v, err)
		}

		hKey := key("hash")
		if err := kv.SetValue(ctx, hKey, dbcore.RedisValue{
			Type: dbcore.RedisHash, Hash: map[string]string{"f1": "v1", "f2": "v2"},
		}); err != nil {
			t.Fatalf("hash SetValue: %v", err)
		}
		if v, err := kv.GetValue(ctx, hKey); err != nil || v.Hash["f1"] != "v1" || v.Hash["f2"] != "v2" {
			t.Fatalf("hash GetValue = %+v, %v", v, err)
		}
	})

	t.Run("kv_scan_keys", func(t *testing.T) {
		want := map[string]bool{}
		for i := 0; i < 3; i++ {
			k := key(fmt.Sprintf("scan:%d", i))
			want[k] = true
			if err := kv.SetValue(ctx, k, dbcore.RedisValue{Type: dbcore.RedisStr, Str: "v"}); err != nil {
				t.Fatalf("seed scan key: %v", err)
			}
		}
		found := map[string]bool{}
		cursor := uint64(0)
		for page := 0; page < 10; page++ {
			res, err := kv.ScanKeys(ctx, cursor, prefix+"scan:*", 100)
			if err != nil {
				t.Fatalf("ScanKeys: %v", err)
			}
			for _, k := range res.Keys {
				found[k] = true
			}
			cursor = res.Cursor
			if cursor == 0 {
				break
			}
		}
		if cursor != 0 {
			t.Fatalf("scan cursor did not come back to 0 after 10 pages")
		}
		for k := range want {
			if !found[k] {
				t.Errorf("scan missed %q (found %v)", k, found)
			}
		}
	})

	t.Run("kv_missing_key", func(t *testing.T) {
		k := prefix + "does-not-exist"
		kind, err := kv.KeyType(ctx, k)
		if err != nil {
			t.Fatalf("KeyType(missing): %v", err)
		}
		if kind != dbcore.RedisNone {
			t.Fatalf("KeyType(missing) = %q, want none", kind)
		}
		if _, err := kv.GetValue(ctx, k); err == nil {
			t.Fatalf("GetValue(missing) should fail")
		}
	})

	t.Run("kv_command", func(t *testing.T) {
		if reply, err := kv.ExecCommand(ctx, []string{"PING"}); err != nil || strings.TrimSpace(reply.Raw) != "PONG" {
			t.Fatalf("PING = %q, %v", reply.Raw, err)
		}
		k := key("cmd")
		if _, err := kv.ExecCommand(ctx, []string{"SET", k, "v"}); err != nil {
			t.Fatalf("SET: %v", err)
		}
		if v, err := kv.GetValue(ctx, k); err != nil || v.Str != "v" {
			t.Fatalf("value written by command = %+v, %v", v, err)
		}
		if _, err := kv.ExecCommand(ctx, nil); err == nil {
			t.Fatalf("empty command should be an error")
		}
	})

	t.Run("kv_typed_command", func(t *testing.T) {
		tc, ok := dbcore.AsTypedCommander(kv)
		if !ok {
			t.Skip("driver does not implement TypedCommander")
		}
		k := key("typed")
		if _, err := tc.ExecCommandTyped(ctx, []string{"SET", k, "v"}); err != nil {
			t.Fatalf("SET: %v", err)
		}
		got, err := tc.ExecCommandTyped(ctx, []string{"GET", k})
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		if got.Kind != dbcore.RedisReplyKindBulkString || got.Value != "v" {
			t.Fatalf("GET reply = %+v, want bulk_string v", got)
		}
		if got, err = tc.ExecCommandTyped(ctx, []string{"INCR", key("typed:counter")}); err != nil ||
			got.Kind != dbcore.RedisReplyKindInteger || got.Value != int64(1) {
			t.Fatalf("INCR reply = %+v, %v, want integer 1", got, err)
		}
		missing := prefix + "typed:missing"
		if got, err = tc.ExecCommandTyped(ctx, []string{"GET", missing}); err != nil || got.Kind != dbcore.RedisReplyKindNull {
			t.Fatalf("GET missing = %+v, %v, want null", got, err)
		}
	})

	t.Run("kv_element_write", func(t *testing.T) {
		ew, ok := dbcore.AsElementWriter(kv)
		if !ok {
			t.Skip("driver does not implement ElementWriter")
		}
		hKey := key("elem:hash")
		if err := kv.SetValue(ctx, hKey, dbcore.RedisValue{Type: dbcore.RedisHash, Hash: map[string]string{"f": "old"}}); err != nil {
			t.Fatalf("seed hash: %v", err)
		}
		if err := ew.SetElement(ctx, dbcore.RedisElement{Key: hKey, Identity: "field", ID: "f", Column: "value", Value: "new"}); err != nil {
			t.Fatalf("SetElement field: %v", err)
		}
		if v, err := kv.GetValue(ctx, hKey); err != nil || v.Hash["f"] != "new" {
			t.Fatalf("hash after element write = %+v, %v", v, err)
		}
		listKey := key("elem:list")
		if err := kv.SetValue(ctx, listKey, dbcore.RedisValue{Type: dbcore.RedisList, Items: []string{"a", "b"}}); err != nil {
			t.Fatalf("seed list: %v", err)
		}
		if err := ew.SetElement(ctx, dbcore.RedisElement{Key: listKey, Identity: "index", ID: "1", Column: "value", Value: "B"}); err != nil {
			t.Fatalf("SetElement index: %v", err)
		}
		if v, err := kv.GetValue(ctx, listKey); err != nil || strings.Join(v.Items, ",") != "a,B" {
			t.Fatalf("list after element write = %+v, %v", v, err)
		}
	})

	t.Run("kv_element_delete", func(t *testing.T) {
		ed, ok := dbcore.AsElementDeleter(kv)
		if !ok {
			t.Skip("driver does not implement ElementDeleter")
		}
		setKey := key("elem:set")
		if err := kv.SetValue(ctx, setKey, dbcore.RedisValue{Type: dbcore.RedisSet, Items: []string{"a", "b"}}); err != nil {
			t.Fatalf("seed set: %v", err)
		}
		if err := ed.DeleteElement(ctx, dbcore.RedisElement{Key: setKey, Identity: "member", ID: "a"}); err != nil {
			t.Fatalf("DeleteElement: %v", err)
		}
		if v, err := kv.GetValue(ctx, setKey); err != nil || strings.Join(v.Items, ",") != "b" {
			t.Fatalf("set after element delete = %+v, %v, want b", v, err)
		}
	})

	t.Run("kv_row_append", func(t *testing.T) {
		ra, ok := dbcore.AsRowAppender(kv)
		if !ok {
			t.Skip("driver does not implement RowAppender")
		}
		setKey := key("append:set")
		if err := kv.SetValue(ctx, setKey, dbcore.RedisValue{Type: dbcore.RedisSet, Items: []string{"a"}}); err != nil {
			t.Fatalf("seed set: %v", err)
		}
		member, err := ra.AppendRow(ctx, dbcore.RedisRow{Key: setKey, Kind: dbcore.RedisSet, Member: "b"})
		if err != nil || member != "b" {
			t.Fatalf("append set member = %q, %v", member, err)
		}
		streamKey := key("append:stream")
		// A stream key does not exist until its first entry, so it needs no seed.
		id, err := ra.AppendRow(ctx, dbcore.RedisRow{
			Key: streamKey, Kind: dbcore.RedisStream,
			Fields: []dbcore.RedisField{{Name: "f", Value: "v"}},
		})
		if err != nil || id == "" {
			t.Fatalf("append stream entry = %q, %v", id, err)
		}
		v, err := kv.GetValue(ctx, streamKey)
		if err != nil || len(v.Stream) != 1 || v.Stream[0].ID != id || v.Stream[0].Fields[0].Value != "v" {
			t.Fatalf("stream after append = %+v, %v", v, err)
		}
	})

	t.Run("kv_key_admin", func(t *testing.T) {
		ka, ok := dbcore.AsKeyAdmin(kv)
		if !ok {
			t.Skip("driver does not implement KeyAdmin")
		}
		k := key("admin")
		if err := kv.SetValue(ctx, k, dbcore.RedisValue{Type: dbcore.RedisStr, Str: "v"}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := ka.SetTTL(ctx, k, ttl); err != nil {
			t.Fatalf("SetTTL: %v", err)
		}
		meta, err := ka.KeyMeta(ctx, k)
		if err != nil {
			t.Fatalf("KeyMeta: %v", err)
		}
		if meta.Kind != dbcore.RedisStr || !meta.HasTTL || meta.TTL <= 0 || meta.TTL > ttl {
			t.Fatalf("KeyMeta after SetTTL = %+v, want string with a %v TTL", meta, ttl)
		}
		// A zero TTL clears the expiry (PERSIST).
		if err := ka.SetTTL(ctx, k, 0); err != nil {
			t.Fatalf("SetTTL(0): %v", err)
		}
		if meta, err = ka.KeyMeta(ctx, k); err != nil || meta.HasTTL {
			t.Fatalf("KeyMeta after persist = %+v, %v, want no TTL", meta, err)
		}
		// Rename must not overwrite an existing key.
		target := key("admin:target")
		if err := kv.SetValue(ctx, target, dbcore.RedisValue{Type: dbcore.RedisStr, Str: "keep"}); err != nil {
			t.Fatalf("seed target: %v", err)
		}
		if err := ka.RenameKey(ctx, k, target); err == nil {
			t.Fatalf("RenameKey onto an existing key should fail")
		}
		renamed := key("admin:renamed")
		if err := ka.RenameKey(ctx, k, renamed); err != nil {
			t.Fatalf("RenameKey: %v", err)
		}
		if kind, err := kv.KeyType(ctx, k); err != nil || kind != dbcore.RedisNone {
			t.Fatalf("old key kind = %q, %v, want none", kind, err)
		}
		if err := ka.DeleteKeys(ctx, renamed, target); err != nil {
			t.Fatalf("DeleteKeys: %v", err)
		}
		if kind, _ := kv.KeyType(ctx, renamed); kind != dbcore.RedisNone {
			t.Fatalf("deleted key still present")
		}
	})

	t.Run("kv_key_paging", func(t *testing.T) {
		kp, ok := dbcore.AsKeyPageReader(kv)
		if !ok {
			t.Skip("driver does not implement KeyPageReader")
		}
		listKey := key("page:list")
		if err := kv.SetValue(ctx, listKey, dbcore.RedisValue{Type: dbcore.RedisList, Items: []string{"a", "b", "c"}}); err != nil {
			t.Fatalf("seed list: %v", err)
		}
		items, length, err := kp.ListRange(ctx, listKey, 1, 2)
		if err != nil || length != 3 || strings.Join(items, ",") != "b,c" {
			t.Fatalf("ListRange = %v len %d, %v, want b,c / 3", items, length, err)
		}

		hKey := key("page:hash")
		if err := kv.SetValue(ctx, hKey, dbcore.RedisValue{Type: dbcore.RedisHash, Hash: map[string]string{"f1": "v1", "f2": "v2"}}); err != nil {
			t.Fatalf("seed hash: %v", err)
		}
		seen := map[string]string{}
		cursor := uint64(0)
		for i := 0; i < 10; i++ {
			fields, next, err := kp.HashPage(ctx, hKey, cursor, 1)
			if err != nil {
				t.Fatalf("HashPage: %v", err)
			}
			for _, f := range fields {
				seen[f.Name] = f.Value
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
		if len(seen) != 2 || seen["f1"] != "v1" || seen["f2"] != "v2" {
			t.Fatalf("HashPage collected %v", seen)
		}

		streamKey := key("page:stream")
		ra, ok := dbcore.AsRowAppender(kv)
		if ok {
			ids := make([]string, 0, 3)
			for i := 0; i < 3; i++ {
				id, err := ra.AppendRow(ctx, dbcore.RedisRow{
					Key: streamKey, Kind: dbcore.RedisStream,
					Fields: []dbcore.RedisField{{Name: "n", Value: fmt.Sprint(i)}},
				})
				if err != nil {
					t.Fatalf("append stream entry: %v", err)
				}
				ids = append(ids, id)
			}
			entries, length, err := kp.StreamRange(ctx, streamKey, ids[0], 10)
			if err != nil || length != 3 || len(entries) != 2 || entries[0].ID != ids[1] {
				t.Fatalf("StreamRange after %q = %+v len %d, %v", ids[0], entries, length, err)
			}
		}
	})

	t.Run("kv_enriched_scan", func(t *testing.T) {
		ks, ok := dbcore.AsKeyScanner(kv)
		if !ok {
			t.Skip("driver does not implement KeyScanner")
		}
		ka, _ := dbcore.AsKeyAdmin(kv)
		k := key("enriched")
		if err := kv.SetValue(ctx, k, dbcore.RedisValue{Type: dbcore.RedisStr, Str: "v"}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if ka != nil {
			if err := ka.SetTTL(ctx, k, ttl); err != nil {
				t.Fatalf("SetTTL: %v", err)
			}
		}
		found := false
		cursor := uint64(0)
		for page := 0; page < 10; page++ {
			res, err := ks.ScanKeysEnriched(ctx, cursor, prefix+"enriched", 100)
			if err != nil {
				t.Fatalf("ScanKeysEnriched: %v", err)
			}
			for _, entry := range res.Keys {
				if entry.Key != k {
					continue
				}
				found = true
				if entry.Kind != dbcore.RedisStr {
					t.Errorf("enriched kind = %q, want string", entry.Kind)
				}
				if ka != nil && (entry.TTL == nil || *entry.TTL <= 0) {
					t.Errorf("enriched TTL = %v, want a positive lifetime", entry.TTL)
				}
			}
			cursor = res.Cursor
			if cursor == 0 {
				break
			}
		}
		if !found {
			t.Fatalf("enriched scan missed %q", k)
		}
	})
}
