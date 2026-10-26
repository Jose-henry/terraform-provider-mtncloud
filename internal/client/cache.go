package client

import (
	"context"
	"fmt"
	"sync"
)

// lookupCache memoises read-only lookups for the life of one provider instance (one Terraform
// command), so 50 instances that use the same image cost one API call, not 50.
//
// Concurrent callers of the same key wait for the first call and share its result, error included:
// a failing endpoint is called once, not once per waiter. A failure is then evicted, so a later
// call (a new resource, the next command) tries again. Success is kept.
type lookupCache struct {
	mu      sync.Mutex
	entries map[string]*cacheEntry
}

type cacheEntry struct {
	done chan struct{}
	val  any
	err  error
}

func cached[T any](ctx context.Context, c *lookupCache, key string, load func(context.Context) (T, error)) (val T, err error) {
	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[string]*cacheEntry{}
	}
	if e, ok := c.entries[key]; ok {
		c.mu.Unlock()
		select {
		case <-e.done:
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		}
		if e.err != nil {
			var zero T
			return zero, e.err
		}
		return e.val.(T), nil
	}
	e := &cacheEntry{done: make(chan struct{})}
	c.entries[key] = e
	c.mu.Unlock()

	defer func() {
		if r := recover(); r != nil {
			e.err = fmt.Errorf("lookup %s failed: %v", key, r)
			err = e.err
		}
		if e.err != nil {
			c.mu.Lock()
			delete(c.entries, key)
			c.mu.Unlock()
		}
		close(e.done) // always, so waiters never hang
	}()
	val, err = load(ctx)
	e.val, e.err = val, err
	return val, err
}
