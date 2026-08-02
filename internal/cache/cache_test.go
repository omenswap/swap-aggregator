package cache

import (
	"sync"
	"testing"
	"time"
)

func TestGetSet(t *testing.T) {
	c := New[int](time.Minute, nil)
	if _, ok := c.Get("a"); ok {
		t.Error("expected miss on empty cache")
	}
	c.Set("a", 1)
	v, ok := c.Get("a")
	if !ok || v != 1 {
		t.Errorf("got %d, %v", v, ok)
	}
}

func TestExpiry(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	c := New[int](time.Minute, clock)
	c.Set("a", 1)
	if _, ok := c.Get("a"); !ok {
		t.Error("expected hit before expiry")
	}
	now = now.Add(time.Minute + time.Second)
	if _, ok := c.Get("a"); ok {
		t.Error("expected miss after expiry")
	}
}

func TestOverwriteResetsExpiry(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	c := New[int](time.Minute, clock)
	c.Set("a", 1)
	now = now.Add(45 * time.Second)
	c.Set("a", 2)
	now = now.Add(30 * time.Second)
	v, ok := c.Get("a")
	if !ok || v != 2 {
		t.Errorf("got %d, %v", v, ok)
	}
}

func TestConcurrent(t *testing.T) {
	c := New[int](time.Minute, nil)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			c.Set("k", i)
		}(i)
		go func() {
			defer wg.Done()
			c.Get("k")
		}()
	}
	wg.Wait()
}
