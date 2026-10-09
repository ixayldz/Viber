package retrieval

import (
	"context"
	"errors"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
	"sort"
	"sync"
	"time"
)

type CacheEvidence struct {
	Key            string `json:"key"`
	Hit            bool   `json:"hit"`
	Retained       bool   `json:"retained"`
	EstimatedBytes int64  `json:"estimated_retained_bytes"`
	Measurement    string `json:"measurement"`
}
type cacheEntry struct {
	key, scope string
	index      *Index
	bytes      int64
	refs       int
	touched    time.Time
	retired    bool
}
type cacheBuild struct {
	done  chan struct{}
	scope string
}

// Cache holds only private immutable source indexes. Every lease remains
// charged until released, including expired or invalidated entries. Estimates
// bound admission; they are not a measurement or a hard limit of process RSS.
// At most one transient build exists in addition to the resident estimate.
type Cache struct {
	mu           sync.Mutex
	entries      map[string]*cacheEntry
	live         map[*cacheEntry]bool
	building     map[string]*cacheBuild
	scopes       map[string]string
	bytes, limit int64
	count        int
	ttl          time.Duration
	now          func() time.Time
	closed       bool
	stop         chan struct{}
	worker       sync.WaitGroup
}

func NewCache(limit int64, count int, ttl time.Duration) (*Cache, error) {
	if limit < 1 || limit > 256<<20 || count < 1 || count > 16 || ttl < time.Second || ttl > 10*time.Minute {
		return nil, c.Fail(c.InvalidArgument, "bounded retrieval cache limits required")
	}
	cache := &Cache{entries: map[string]*cacheEntry{}, live: map[*cacheEntry]bool{}, building: map[string]*cacheBuild{}, scopes: map[string]string{}, limit: limit, count: count, ttl: ttl, now: time.Now, stop: make(chan struct{})}
	cache.worker.Add(1)
	go func() {
		defer cache.worker.Done()
		ticker := time.NewTicker(min(ttl/2, 10*time.Second))
		defer ticker.Stop()
		for {
			select {
			case <-cache.stop:
				return
			case <-ticker.C:
				cache.mu.Lock()
				cache.sweep()
				cache.pruneScopes()
				cache.mu.Unlock()
			}
		}
	}()
	return cache, nil
}
func cacheKey(ctx context.Context, binding Binding, documents []Document) (string, error) {
	if !c.ValidDigest(binding.Candidate) || !c.ValidDigest(binding.Policy) {
		return "", c.Fail(c.InvalidArgument, "valid cache source binding required")
	}
	if len(documents) > 10000 {
		return "", c.Fail(c.UnsupportedCapability, "cache source count exceeded")
	}
	refs := []struct{ Path, Digest string }{}
	total := int64(0)
	seen := map[string]bool{}
	for _, doc := range documents {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		total += int64(len(doc.Bytes))
		if total > MaxBytes || len(doc.Path) > 4096 {
			return "", c.Fail(c.UnsupportedCapability, "cache source bound exceeded")
		}
		if !policy.SafePath(doc.Path) || seen[doc.Path] || !c.ValidDigest(doc.Digest) || c.HashBytes(doc.Bytes) != doc.Digest {
			return "", c.Fail(c.StoreIntegrityError, "cache source changed, unsafe or duplicated")
		}
		seen[doc.Path] = true
		refs = append(refs, struct{ Path, Digest string }{doc.Path, doc.Digest})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Path < refs[j].Path })
	sources, err := c.Digest(refs)
	if err != nil {
		return "", err
	}
	return c.Digest(struct {
		Version string
		Binding Binding
		Sources string
	}{Version, binding, sources})
}

// All helpers below require mu. Closing is safe only after the final reader.
func (cache *Cache) dispose(entry *cacheEntry) error {
	delete(cache.live, entry)
	cache.bytes -= entry.bytes
	return entry.index.Close()
}
func (cache *Cache) retire(entry *cacheEntry) error {
	if entry.retired {
		return nil
	}
	entry.retired = true
	delete(cache.entries, entry.key)
	if entry.refs == 0 {
		return cache.dispose(entry)
	}
	return nil
}
func (cache *Cache) sweep() {
	now := cache.now()
	for _, entry := range cache.entries {
		if now.Sub(entry.touched) >= cache.ttl {
			cache.retire(entry)
		}
	}
}
func (cache *Cache) pruneScopes() {
	used := map[string]bool{}
	for entry := range cache.live {
		used[entry.scope] = true
	}
	for _, pending := range cache.building {
		used[pending.scope] = true
	}
	for scope := range cache.scopes {
		if !used[scope] {
			delete(cache.scopes, scope)
		}
	}
}

// Acquire revalidates authorized bytes before every hit. Release is idempotent.
// A revoked scope or closed owner can never publish a build started earlier.
func (cache *Cache) Acquire(ctx context.Context, scope string, binding Binding, documents []Document) (*Index, CacheEvidence, func(), error) {
	evidence := CacheEvidence{Measurement: "CONSERVATIVE_SOURCE_CHUNK_SQLITE_ESTIMATE_NOT_PEAK_RSS"}
	if !c.ValidDigest(scope) {
		return nil, evidence, nil, c.Fail(c.InvalidArgument, "task scope digest required")
	}
	key, err := cacheKey(ctx, binding, documents)
	if err != nil {
		return nil, evidence, nil, err
	}
	evidence.Key = key
	scopedKey, err := c.Digest(struct{ Scope, Key string }{scope, key})
	if err != nil {
		return nil, evidence, nil, err
	}
	for {
		if err = ctx.Err(); err != nil {
			return nil, evidence, nil, err
		}
		cache.mu.Lock()
		if cache.closed {
			cache.mu.Unlock()
			return nil, evidence, nil, c.Fail(c.StoreIntegrityError, "retrieval cache closed")
		}
		cache.sweep()
		cache.pruneScopes()
		if prior := cache.scopes[scope]; prior != scopedKey {
			for _, entry := range cache.entries {
				if entry.scope == scope {
					cache.retire(entry)
				}
			}
			cache.scopes[scope] = scopedKey
		}
		if entry := cache.entries[scopedKey]; entry != nil {
			entry.refs++
			entry.touched = cache.now()
			evidence.Hit = true
			evidence.Retained = true
			evidence.EstimatedBytes = entry.bytes
			cache.mu.Unlock()
			return entry.index, evidence, cache.release(entry), nil
		}
		if pending := cache.building[scopedKey]; pending != nil {
			done := pending.done
			cache.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, evidence, nil, ctx.Err()
			case <-done:
				continue
			}
		}
		if len(cache.building) != 0 {
			cache.pruneScopes()
			cache.mu.Unlock()
			return nil, evidence, nil, c.Fail(c.UnsupportedCapability, "retrieval build capacity occupied")
		}
		pending := &cacheBuild{done: make(chan struct{}), scope: scope}
		cache.building[scopedKey] = pending
		cache.mu.Unlock()
		index, buildErr := New(ctx, binding, documents)
		cache.mu.Lock()
		delete(cache.building, scopedKey)
		close(pending.done)
		if buildErr != nil {
			cache.pruneScopes()
			cache.mu.Unlock()
			return nil, evidence, nil, buildErr
		}
		if cache.closed || cache.scopes[scope] != scopedKey {
			cache.pruneScopes()
			cache.mu.Unlock()
			index.Close()
			return nil, evidence, nil, c.Fail(c.StaleBase, "retrieval scope revoked during build")
		}
		manifest := index.Manifest()
		estimate := manifest.SourceBytes*3 + manifest.SQLiteBytes + int64(manifest.Chunks)*256
		for _, doc := range documents {
			estimate += int64(len(doc.Path))*3 + 256
		}
		evidence.EstimatedBytes = estimate
		for cache.bytes+estimate > cache.limit || len(cache.live) >= cache.count {
			var oldest *cacheEntry
			for _, entry := range cache.entries {
				if entry.refs == 0 && (oldest == nil || entry.touched.Before(oldest.touched)) {
					oldest = entry
				}
			}
			if oldest == nil {
				break
			}
			cache.retire(oldest)
		}
		if cache.bytes+estimate > cache.limit || len(cache.live) >= cache.count {
			cache.pruneScopes()
			cache.mu.Unlock()
			index.Close()
			return nil, evidence, nil, c.Fail(c.UnsupportedCapability, "retrieval cache resident capacity occupied or exceeded")
		}
		entry := &cacheEntry{key: scopedKey, scope: scope, index: index, bytes: estimate, refs: 1, touched: cache.now()}
		cache.entries[scopedKey] = entry
		cache.live[entry] = true
		cache.bytes += estimate
		evidence.Retained = true
		cache.pruneScopes()
		cache.mu.Unlock()
		return index, evidence, cache.release(entry), nil
	}
}
func (cache *Cache) release(entry *cacheEntry) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			cache.mu.Lock()
			defer cache.mu.Unlock()
			entry.refs--
			if entry.retired && entry.refs == 0 {
				cache.dispose(entry)
			}
			cache.pruneScopes()
		})
	}
}
func (cache *Cache) InvalidateScope(scope string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	delete(cache.scopes, scope)
	for _, entry := range cache.entries {
		if entry.scope == scope {
			cache.retire(entry)
		}
	}
	cache.pruneScopes()
}
func (cache *Cache) Close() error {
	if cache == nil {
		return nil
	}
	cache.mu.Lock()
	var err error
	if !cache.closed {
		cache.closed = true
		close(cache.stop)
		for _, entry := range cache.entries {
			err = errors.Join(err, cache.retire(entry))
		}
		clear(cache.scopes)
	}
	cache.mu.Unlock()
	cache.worker.Wait()
	return err
}
