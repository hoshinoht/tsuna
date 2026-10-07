package snapshot

import (
	"container/list"
	"io/fs"
	"strings"
	"sync"
	"time"

	"github.com/hoshinoht/shiori/internal/model"
)

// Cache keeps artifact bytes and decoded plans across operations of a
// long-lived process (serve). It is never mutation authority: writes still
// recheck every digest under the locks.
//
//   - Files are keyed by path and stat identity (device, inode, size, mtime,
//     ctime). Only reads trust a stat hit, and only for entries whose mtime
//     was older than RacyWindow when they were filled (an edit in the same
//     timestamp tick would keep the stat unchanged).
//   - Decoded plans are keyed by the sha256 of their JSON bytes, so a hit is
//     exact for reads and writes alike. Callers treat them as immutable.
type Cache struct {
	mu     sync.Mutex
	budget int64
	used   int64
	lru    *list.List // front = most recent; values *cacheItem
	files  map[string]*list.Element
	plans  map[string]*list.Element

	generated map[string]bool // GeneratedKey -> Markdown is the rendering

	// RacyWindow is how much older than its fill time a file's mtime must
	// be for a stat hit to be trusted.
	RacyWindow time.Duration
	now        func() time.Time

	Hits, StatMisses, PlanHits int64 // counters (tests, diagnostics)
}

type cacheItem struct {
	file bool
	key  string
	size int64

	stat   statKey
	data   []byte
	sha    string
	filled time.Time

	plan *model.Plan
}

// DefaultCacheBudget bounds cached bytes (artifacts plus an estimate for
// decoded plans).
const DefaultCacheBudget = 256 << 20

// NewCache returns an empty cache holding at most budget bytes.
func NewCache(budget int64) *Cache {
	return &Cache{budget: budget, lru: list.New(), files: map[string]*list.Element{}, plans: map[string]*list.Element{},
		RacyWindow: 2 * time.Second, now: time.Now}
}

// file returns cached bytes and digest for a path whose stat is info,
// when the entry is trustworthy.
func (c *Cache) file(abs string, info fs.FileInfo) ([]byte, string, bool) {
	k, ok := keyOf(info)
	if !ok {
		return nil, "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, hit := c.files[abs]
	if !hit {
		c.StatMisses++
		return nil, "", false
	}
	it := el.Value.(*cacheItem)
	if it.stat != k || it.filled.Sub(info.ModTime()) < c.RacyWindow {
		c.StatMisses++
		return nil, "", false
	}
	c.lru.MoveToFront(el)
	c.Hits++
	return it.data, it.sha, true
}

// putFile records the bytes read for a path with the stat taken before
// reading.
func (c *Cache) putFile(abs string, info fs.FileInfo, data []byte, sha string) {
	k, ok := keyOf(info)
	if !ok {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.remove(c.files, abs)
	it := &cacheItem{file: true, key: abs, size: int64(len(data)), stat: k, data: data, sha: sha, filled: c.now()}
	c.files[abs] = c.lru.PushFront(it)
	c.used += it.size
	c.evict()
}

// plan returns the decoded plan of JSON bytes with digest sha.
func (c *Cache) plan(sha string) *model.Plan {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, hit := c.plans[sha]
	if !hit {
		return nil
	}
	c.lru.MoveToFront(el)
	c.PlanHits++
	return el.Value.(*cacheItem).plan
}

func (c *Cache) putPlan(sha string, jsonSize int, p *model.Plan) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.remove(c.plans, sha)
	// Decoded plans keep the JSON strings alive; count them twice.
	it := &cacheItem{key: sha, size: 2 * int64(jsonSize), plan: p}
	c.plans[sha] = c.lru.PushFront(it)
	c.used += it.size
	c.evict()
}

func (c *Cache) remove(m map[string]*list.Element, key string) {
	if el, ok := m[key]; ok {
		c.used -= el.Value.(*cacheItem).size
		c.lru.Remove(el)
		delete(m, key)
	}
}

func (c *Cache) evict() {
	for c.used > c.budget && c.lru.Len() > 1 {
		it := c.lru.Back().Value.(*cacheItem)
		if it.file {
			c.remove(c.files, it.key)
		} else {
			c.remove(c.plans, it.key)
		}
	}
}

// Generated reports a cached "is this Markdown the plan's rendering"
// answer; key is GeneratedKey's.
func (c *Cache) Generated(key string) (gen, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	gen, ok = c.generated[key]
	return gen, ok
}

// SetGenerated records the answer for key (bounded; cleared when full).
func (c *Cache) SetGenerated(key string, gen bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generated == nil || len(c.generated) >= maxGenerated {
		c.generated = map[string]bool{}
	}
	c.generated[key] = gen
}

const maxGenerated = 4096

// GeneratedKey identifies a classification: the plan and Markdown digests
// and the normalized links the rendering includes.
func GeneratedKey(planSHA, mdSHA, planFile string, specFiles []string) string {
	return planSHA + "\x00" + mdSHA + "\x00" + planFile + "\x00" + strings.Join(specFiles, "\x00")
}

// SeedPlan caches the decoded form of JSON bytes the caller just wrote
// (digest sha), so the next load skips parsing them. p must equal what
// decoding those bytes yields.
func (c *Cache) SeedPlan(sha string, jsonSize int, p *model.Plan) { c.putPlan(sha, jsonSize, p) }
