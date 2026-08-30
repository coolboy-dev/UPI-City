package explain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Cache stores explanations on disk, keyed by the facts that produced them.
//
// ─── This is the demo's insurance policy ────────────────────────────────────
//
// Generation takes seconds and depends on a model server that may be swapping,
// out of VRAM, or simply not running. None of that is acceptable during a live
// demonstration. So the explanations for the demo seeds are generated ahead of
// time, committed, and served instantly as cache hits — the model is not
// invoked at all while anyone is watching.
//
// The key is a hash of the ROUNDED facts, which is what makes this work: the
// same seed produces the same incident, the same rounded facts, and therefore
// the same key. Reproducibility is what turns a slow model into a fast one.
type Cache struct {
	mu    sync.RWMutex
	items map[string]Explanation
	path  string
}

// NewCache returns an empty in-memory cache.
func NewCache() *Cache {
	return &Cache{items: map[string]Explanation{}}
}

// LoadCache reads a cache file, returning an empty cache if it is absent.
func LoadCache(path string) *Cache {
	c := NewCache()
	c.path = path
	b, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	var items map[string]Explanation
	if json.Unmarshal(b, &items) == nil {
		c.items = items
	}
	return c
}

// Key derives the cache key from facts.
//
// Money and durations are rounded before hashing so that trivial differences
// — a rupee here, a tick there — do not produce a miss on what is recognisably
// the same incident.
func Key(f Facts) string {
	sigs := make([]string, 0, len(f.TopSignals))
	for _, s := range f.TopSignals {
		sigs = append(sigs, s.Name)
	}
	sort.Strings(sigs)

	if f.Kind == "bank-outage" {
		return fmt.Sprintf("bank-outage|w%d|f%d|a%d",
			roundTo(int64(f.TxInWindow), 100),
			roundTo(int64(f.FailedInWindow), 50),
			roundTo(int64(f.FalseAlarms), 10))
	}
	return fmt.Sprintf("%s|m%d|t%d|r%d|d%v|s%v",
		f.Kind,
		f.Members,
		roundTo(int64(f.Transactions), 10),
		roundTo(f.TotalRupees, 10000),
		f.Detected,
		sigs,
	)
}

func roundTo(v, step int64) int64 {
	if step <= 1 {
		return v
	}
	return (v + step/2) / step * step
}

// Get returns a cached explanation.
func (c *Cache) Get(f Facts) (Explanation, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.items[Key(f)]
	return e, ok
}

// Put stores an explanation.
func (c *Cache) Put(f Facts, e Explanation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[Key(f)] = e
}

// Len reports how many explanations are cached.
func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}

// Save writes the cache to disk.
func (c *Cache) Save(path string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c.items, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
