package redis

import (
	"context"
	"strconv"
	"testing"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

func itoa(n int) string { return strconv.Itoa(n) }

// Large keys must be readable in windows: list/zset by rank with their length,
// hash/set by SCAN cursor, stream by ID continuation — never forced whole.
func TestKeyPageReader(t *testing.T) {
	mr := miniredis.RunT(t)
	t.Cleanup(mr.Close)
	d, err := OpenOptions(&goredis.Options{Addr: mr.Addr(), DisableIdentity: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()

	for i := 0; i < 250; i++ {
		mr.Lpush("biglist", itoa(i))
	}
	items, length, err := d.ListRange(ctx, "biglist", 100, 149)
	if err != nil || length != 250 || len(items) != 50 {
		t.Fatalf("list page: %v len=%d n=%d", err, length, len(items))
	}
	if items[0] != "249" && items[0] != "149" {
		t.Fatalf("list window starts at %q", items[0])
	}

	for i := 0; i < 300; i++ {
		mr.HSet("bighash", itoa(i), "v")
	}
	seen := map[string]bool{}
	var cursor uint64
	steps := 0
	for {
		fields, next, err := d.HashPage(ctx, "bighash", cursor, 40)
		if err != nil {
			t.Fatalf("hash page: %v", err)
		}
		for _, f := range fields {
			if seen[f.Name] {
				t.Fatalf("hash field %q seen twice", f.Name)
			}
			seen[f.Name] = true
		}
		cursor = next
		steps++
		if cursor == 0 || steps > 100 {
			break
		}
	}
	if len(seen) != 300 {
		t.Fatalf("hash walk got %d fields, want 300", len(seen))
	}

	for i := 0; i < 120; i++ {
		mr.SAdd("bigset", itoa(i))
	}
	gotMembers := 0
	cursor = 0
	for {
		members, next, err := d.SetPage(ctx, "bigset", cursor, 30)
		if err != nil {
			t.Fatalf("set page: %v", err)
		}
		gotMembers += len(members)
		cursor = next
		if cursor == 0 {
			break
		}
	}
	if gotMembers != 120 {
		t.Fatalf("set walk got %d, want 120", gotMembers)
	}

	for i := 0; i < 80; i++ {
		mr.ZAdd("bigz", float64(i), "m"+itoa(i))
	}
	members, scores, length, err := d.ZSetRange(ctx, "bigz", 10, 19)
	if err != nil || length != 80 || len(members) != 10 || len(scores) != 10 {
		t.Fatalf("zset page: %v len=%d n=%d", err, length, len(members))
	}

	for i := 0; i < 150; i++ {
		mr.XAdd("bigstream", itoa(1000+i), []string{"seq", itoa(i)})
	}
	page1, length, err := d.StreamRange(ctx, "bigstream", "", 100)
	if err != nil || length != 150 || len(page1) != 100 {
		t.Fatalf("stream first page: %v len=%d n=%d", err, length, len(page1))
	}
	page2, _, err := d.StreamRange(ctx, "bigstream", page1[len(page1)-1].ID, 100)
	if err != nil || len(page2) != 50 {
		t.Fatalf("stream second page: %v n=%d", err, len(page2))
	}
	if page1[len(page1)-1].ID == page2[0].ID {
		t.Fatal("continuation overlaps the previous page")
	}
}
