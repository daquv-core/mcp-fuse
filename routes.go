package main

import (
	"sync"
	"time"
)

// routeCache remembers which backend owns which tool (or prompt, or resource) for one caller, so that a call
// can be sent to its owner without listing every backend first. It is an index of names, not of results, it is
// kept per person and never shared across them, and losing it costs one extra listing.
type routeCache struct {
	mu  sync.Mutex
	m   map[routeKey]routeEntry
	ttl time.Duration
	max int
	now func() time.Time
}

type routeKey struct {
	person string
	kind   string // the list method
}

type routeEntry struct {
	owner   map[string]int
	expires time.Time
}

func newRouteCache(ttl time.Duration, max int) *routeCache {
	return &routeCache{m: map[routeKey]routeEntry{}, ttl: ttl, max: max, now: time.Now}
}

func (c *routeCache) get(k routeKey, name string) (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || !c.now().Before(e.expires) {
		return 0, false
	}
	idx, ok := e.owner[name]
	return idx, ok
}

func (c *routeCache) put(k routeKey, owner map[string]int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= c.max {
		now := c.now()
		for key, e := range c.m {
			if !now.Before(e.expires) {
				delete(c.m, key)
			}
		}
		for key := range c.m {
			if len(c.m) < c.max {
				break
			}
			delete(c.m, key)
		}
	}
	c.m[k] = routeEntry{owner: owner, expires: c.now().Add(c.ttl)}
}
