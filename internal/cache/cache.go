package cache

import (
	"sync"
	"time"
)

type entry[V any] struct {
	v   V
	exp time.Time
}

type Cache[V any] struct {
	mu   sync.Mutex
	ttl  time.Duration
	now  func() time.Time
	data map[string]entry[V]
}

func New[V any](ttl time.Duration, now func() time.Time) *Cache[V] {
	if now == nil {
		now = time.Now
	}
	return &Cache[V]{ttl: ttl, now: now, data: map[string]entry[V]{}}
}

func (c *Cache[V]) Get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.data[key]
	if !ok || c.now().After(e.exp) {
		var zero V
		return zero, false
	}
	return e.v, true
}

func (c *Cache[V]) Set(key string, v V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key] = entry[V]{v: v, exp: c.now().Add(c.ttl)}
}
