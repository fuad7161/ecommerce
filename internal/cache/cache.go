// Package cache is a small Redis-backed JSON cache for catalog data.
//
// Keys are namespaced by a version counter. Bump() increments it after any admin write,
// which invalidates every cached entry at once without scanning keys. Entries also
// expire after a TTL so stock badges never lag for long. All errors degrade to a cache miss.
package cache

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const versionKey = "cache:catalog:version"

type Cache struct {
	rdb *redis.Client
	ttl time.Duration
}

func New(rdb *redis.Client, ttl time.Duration) *Cache { return &Cache{rdb: rdb, ttl: ttl} }

func (c *Cache) key(ctx context.Context, name string) (string, bool) {
	v, err := c.rdb.Get(ctx, versionKey).Int64()
	if err != nil && err != redis.Nil {
		return "", false
	}
	return "cache:catalog:" + strconv.FormatInt(v, 10) + ":" + name, true
}

// Fetch returns the cached value for name, or calls load, caches and returns its result.
func Fetch[T any](ctx context.Context, c *Cache, name string, load func() (T, error)) (T, error) {
	if c == nil || c.ttl <= 0 {
		return load()
	}
	key, ok := c.key(ctx, name)
	if ok {
		if b, err := c.rdb.Get(ctx, key).Bytes(); err == nil {
			var v T
			if json.Unmarshal(b, &v) == nil {
				return v, nil
			}
		}
	}
	v, err := load()
	if err != nil || !ok {
		return v, err
	}
	if b, err := json.Marshal(v); err == nil {
		if err := c.rdb.Set(ctx, key, b, c.ttl).Err(); err != nil {
			slog.WarnContext(ctx, "cache set failed", "key", key, "err", err)
		}
	}
	return v, nil
}

// Bump invalidates all cached catalog entries.
func (c *Cache) Bump(ctx context.Context) {
	if c == nil {
		return
	}
	if err := c.rdb.Incr(ctx, versionKey).Err(); err != nil {
		slog.WarnContext(ctx, "cache bump failed", "err", err)
	}
}
