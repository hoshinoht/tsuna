package fetch

import (
	"container/list"
	"context"
	"errors"
	"sync"
	"time"
)

const (
	defaultCacheTTL        = 10 * time.Minute
	defaultCacheMaxBytes   = 64 << 20
	defaultCacheMaxEntries = 64
	// A single document may use at most this share of the byte budget;
	// larger extractions are served but not cached.
	maxDocShareOfCache = 4
	docOverheadBytes   = 512
)

// docCache is a TTL + LRU cache of extractions, bounded by entry count and
// by approximate memory (the strings each document holds).
type docCache struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxBytes   int64
	maxEntries int
	now        func() time.Time

	lru   *list.List               // front = most recent; values are *cacheEntry
	byID  map[string]*list.Element // document ID -> entry
	byKey map[string]string        // request URL -> document ID
	bytes int64
}

type cacheEntry struct {
	doc     *document
	keys    []string
	expires time.Time
	size    int64
}

func newDocCache(ttl time.Duration, maxBytes int64, maxEntries int, now func() time.Time) *docCache {
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}
	if maxBytes <= 0 {
		maxBytes = defaultCacheMaxBytes
	}
	if maxEntries <= 0 {
		maxEntries = defaultCacheMaxEntries
	}
	return &docCache{
		ttl: ttl, maxBytes: maxBytes, maxEntries: maxEntries, now: now,
		lru: list.New(), byID: map[string]*list.Element{}, byKey: map[string]string{},
	}
}

func docSize(d *document) int64 {
	return int64(len(d.markdown)+len(d.title)+len(d.finalURL)+len(d.key)) + docOverheadBytes
}

func (c *docCache) getByID(id string) *document {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.liveLocked(id)
}

func (c *docCache) getByKey(key string) *document {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, ok := c.byKey[key]
	if !ok {
		return nil
	}
	return c.liveLocked(id)
}

func (c *docCache) liveLocked(id string) *document {
	el, ok := c.byID[id]
	if !ok {
		return nil
	}
	e := el.Value.(*cacheEntry)
	if !c.now().Before(e.expires) {
		c.removeLocked(el)
		return nil
	}
	c.lru.MoveToFront(el)
	return e.doc
}

// put stores d under its ID and request URL. It reports false when d is
// too large to cache.
func (c *docCache) put(d *document) bool {
	size := docSize(d)
	if size > c.maxBytes/maxDocShareOfCache {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	// The URL now points at this version; drop its link to an older one.
	if oldID, ok := c.byKey[d.key]; ok && oldID != d.id {
		if el, ok := c.byID[oldID]; ok {
			e := el.Value.(*cacheEntry)
			e.keys = removeString(e.keys, d.key)
		}
	}
	c.byKey[d.key] = d.id

	if el, ok := c.byID[d.id]; ok {
		e := el.Value.(*cacheEntry)
		e.expires = c.now().Add(c.ttl)
		if !containsString(e.keys, d.key) {
			e.keys = append(e.keys, d.key)
		}
		c.lru.MoveToFront(el)
		return true
	}

	e := &cacheEntry{doc: d, keys: []string{d.key}, expires: c.now().Add(c.ttl), size: size}
	c.byID[d.id] = c.lru.PushFront(e)
	c.bytes += size
	c.evictLocked()
	return true
}

func (c *docCache) evictLocked() {
	now := c.now()
	for el := c.lru.Back(); el != nil; {
		prev := el.Prev()
		if !now.Before(el.Value.(*cacheEntry).expires) {
			c.removeLocked(el)
		}
		el = prev
	}
	for c.lru.Len() > c.maxEntries || c.bytes > c.maxBytes {
		c.removeLocked(c.lru.Back())
	}
}

func (c *docCache) removeLocked(el *list.Element) {
	e := el.Value.(*cacheEntry)
	c.lru.Remove(el)
	delete(c.byID, e.doc.id)
	for _, k := range e.keys {
		if c.byKey[k] == e.doc.id {
			delete(c.byKey, k)
		}
	}
	c.bytes -= e.size
}

func (c *docCache) stats() (entries int, bytes int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.Len(), c.bytes
}

func containsString(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func removeString(ss []string, s string) []string {
	out := ss[:0]
	for _, x := range ss {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

// flightGroup collapses concurrent loads of the same URL into one download.
// The shared work runs on its own context and is canceled only when every
// waiting caller has gone away, so one impatient caller cannot fail the
// others.
type flightGroup struct {
	mu    sync.Mutex
	calls map[string]*flightCall
}

type flightCall struct {
	done    chan struct{}
	doc     *document
	err     *ToolError
	waiters int
	cancel  context.CancelFunc
}

func newFlightGroup() *flightGroup { return &flightGroup{calls: map[string]*flightCall{}} }

func (g *flightGroup) do(ctx context.Context, key string, fn func(context.Context) (*document, *ToolError)) (*document, *ToolError) {
	g.mu.Lock()
	c, ok := g.calls[key]
	if !ok {
		workCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		c = &flightCall{done: make(chan struct{}), cancel: cancel}
		g.calls[key] = c
		go func() {
			c.doc, c.err = fn(workCtx)
			g.mu.Lock()
			if g.calls[key] == c {
				delete(g.calls, key)
			}
			g.mu.Unlock()
			close(c.done)
			cancel()
		}()
	}
	c.waiters++
	g.mu.Unlock()

	select {
	case <-c.done:
		return c.doc, c.err
	case <-ctx.Done():
		g.mu.Lock()
		c.waiters--
		if c.waiters == 0 {
			c.cancel()
			if g.calls[key] == c {
				delete(g.calls, key)
			}
		}
		g.mu.Unlock()
		return nil, contextError(ctx)
	}
}

func contextError(ctx context.Context) *ToolError {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &ToolError{Code: "timeout", Message: "fetch did not finish before the caller's deadline"}
	}
	return &ToolError{Code: "canceled", Message: "fetch was canceled"}
}
