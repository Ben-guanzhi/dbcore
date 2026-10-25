package redis

// The shared KV contract against the in-kernel driver, backed by miniredis.
// Before this suite existed the Redis driver was only covered by its own
// package tests; a client wrapping dbcore.KVDriver had no contract to run.

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/dbcore/dbcore"
	"github.com/dbcore/dbcore/drivertest"
)

func TestRedisKVContract(t *testing.T) {
	mr := miniredis.RunT(t)
	drivertest.RunKV(t, drivertest.KVSpec{
		Open: func(ctx context.Context) (dbcore.KVDriver, error) {
			return OpenOptions(&goredis.Options{Addr: mr.Addr(), DisableIdentity: true})
		},
		Prefix: "kvct:",
	})
}
