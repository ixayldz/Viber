package retrieval

import (
	"context"
	"errors"
	c "github.com/ixayldz/Viber/internal/contracts"
	"sync"
	"testing"
	"time"
)

func testCache(t *testing.T, count int) *Cache {
	t.Helper()
	cache, err := NewCache(8<<20, count, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cache.Close() })
	return cache
}
func lease(t *testing.T, cache *Cache, scope string, b Binding, docs []Document) (*Index, CacheEvidence, func()) {
	t.Helper()
	index, e, release, err := cache.Acquire(context.Background(), c.HashBytes([]byte(scope)), b, docs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return index, e, release
}
func requireCode(t *testing.T, err error, code c.Code) {
	t.Helper()
	var failure *c.Error
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}
func TestCacheUsesImmutableAuthorizedSourcesAndTaskIsolation(t *testing.T) {
	cache := testCache(t, 4)
	docs := []Document{document("src/auth.go", "RefreshToken")}
	first, e, release := lease(t, cache, "task-a", fixtureBinding(), docs)
	release()
	if e.Hit || !e.Retained {
		t.Fatal(e)
	}
	second, e, release := lease(t, cache, "task-a", fixtureBinding(), docs)
	defer release()
	if second != first || !e.Hit || e.EstimatedBytes == 0 {
		t.Fatal("no warm hit", e)
	}
	other, e, _ := lease(t, cache, "task-b", fixtureBinding(), docs)
	if other == first || e.Hit {
		t.Fatal("task cache crossed scope")
	}
	docs[0].Bytes[0] = '!'
	_, _, _, err := cache.Acquire(context.Background(), c.HashBytes([]byte("task-a")), fixtureBinding(), docs)
	requireCode(t, err, c.StoreIntegrityError)
	result, err := first.Search(context.Background(), fixtureBinding(), "RefreshToken", "IDENTIFIER", 0, 1)
	if err != nil || len(result.Hits) != 1 {
		t.Fatal("caller corrupted leased index", err)
	}
}
func TestCacheRetiredReaderRemainsChargedAndReadable(t *testing.T) {
	cache := testCache(t, 1)
	docs := []Document{document("a.go", "old")}
	index, e, release := lease(t, cache, "task", fixtureBinding(), docs)
	cache.InvalidateScope(c.HashBytes([]byte("task")))
	cache.mu.Lock()
	charged := cache.bytes
	live := len(cache.live)
	cache.mu.Unlock()
	if charged != e.EstimatedBytes || live != 1 {
		t.Fatal("active retired lease uncharged")
	}
	changed := fixtureBinding()
	changed.Candidate = c.HashBytes([]byte("new"))
	_, _, _, err := cache.Acquire(context.Background(), c.HashBytes([]byte("task")), changed, []Document{document("a.go", "new")})
	requireCode(t, err, c.UnsupportedCapability)
	if result, err := index.Search(context.Background(), fixtureBinding(), "old", "IDENTIFIER", 0, 1); err != nil || len(result.Hits) != 1 {
		t.Fatal("evicted live reader", err)
	}
	release()
	release()
	cache.mu.Lock()
	charged = cache.bytes
	live = len(cache.live)
	cache.mu.Unlock()
	if charged != 0 || live != 0 {
		t.Fatal("lease leaked charge")
	}
	_, e, _ = lease(t, cache, "task", changed, []Document{document("a.go", "new")})
	if e.Hit {
		t.Fatal("revoked index reused")
	}
}
func TestCachePolicyCandidateAndAuthorizedSourceSetInvalidate(t *testing.T) {
	cache := testCache(t, 2)
	base := fixtureBinding()
	docs := []Document{document("a.go", "allowed"), document("secret.go", "private")}
	prior, _, release := lease(t, cache, "task", base, docs)
	release()
	changed := base
	changed.Policy = c.HashBytes([]byte("restricted"))
	next, e, release := lease(t, cache, "task", changed, docs[:1])
	release()
	if next == prior || e.Hit || next.Manifest().Documents != 1 {
		t.Fatal("stale policy cache")
	}
	changed.Candidate = c.HashBytes([]byte("next-candidate"))
	third, e, release := lease(t, cache, "task", changed, docs[:1])
	release()
	if third == next || e.Hit {
		t.Fatal("stale candidate cache")
	}
	fourth, e, _ := lease(t, cache, "task", changed, []Document{document("a.go", "updated")})
	if fourth == third || e.Hit {
		t.Fatal("source set not independently bound")
	}
}
func TestCacheExpiryLRUAndScopeMetadataBound(t *testing.T) {
	cache := testCache(t, 2)
	docs := []Document{document("a.go", "needle")}
	a, _, release := lease(t, cache, "a", fixtureBinding(), docs)
	release()
	_, _, release = lease(t, cache, "b", fixtureBinding(), docs)
	release()
	_, e, release := lease(t, cache, "a", fixtureBinding(), docs)
	release()
	if !e.Hit {
		t.Fatal("lost warm index")
	}
	_, _, release = lease(t, cache, "c", fixtureBinding(), docs)
	release()
	cache.mu.Lock()
	if len(cache.scopes) > 2 || len(cache.live) > 2 {
		t.Fatal("LRU metadata unbounded")
	}
	for _, entry := range cache.entries {
		entry.touched = time.Now().Add(-2 * time.Minute)
	}
	cache.sweep()
	cache.pruneScopes()
	if len(cache.live) != 0 || len(cache.scopes) != 0 || cache.bytes != 0 {
		t.Fatal("expiry retained source/metadata")
	}
	cache.mu.Unlock()
	rebuilt, e, _ := lease(t, cache, "a", fixtureBinding(), docs)
	if rebuilt == a || e.Hit {
		t.Fatal("expired bytes reused")
	}
}
func TestCacheClosePreservesReaderAndRefusesAdmission(t *testing.T) {
	cache := testCache(t, 2)
	docs := []Document{document("a.go", "needle")}
	index, _, release := lease(t, cache, "task", fixtureBinding(), docs)
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := index.Search(context.Background(), fixtureBinding(), "needle", "IDENTIFIER", 0, 1)
	if err != nil || len(result.Hits) != 1 {
		t.Fatal("close destroyed live reader")
	}
	_, _, _, err = cache.Acquire(context.Background(), c.HashBytes([]byte("task")), fixtureBinding(), docs)
	requireCode(t, err, c.StoreIntegrityError)
	release()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.bytes != 0 || len(cache.live) != 0 {
		t.Fatal("close reader leak")
	}
}
func TestCacheConcurrentHitsAndCanceledWaiters(t *testing.T) {
	cache := testCache(t, 4)
	docs := []Document{document("a.go", "needle")}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := cache.Acquire(canceled, c.HashBytes([]byte("task")), fixtureBinding(), docs); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	const workers = 12
	results := make(chan *Index, workers)
	failures := make(chan error, workers)
	var group sync.WaitGroup
	for n := 0; n < workers; n++ {
		group.Go(func() {
			index, _, release, err := cache.Acquire(context.Background(), c.HashBytes([]byte("task")), fixtureBinding(), docs)
			if err != nil {
				failures <- err
				return
			}
			defer release()
			if _, err = index.Search(context.Background(), fixtureBinding(), "needle", "IDENTIFIER", 0, 1); err != nil {
				failures <- err
				return
			}
			results <- index
		})
	}
	group.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	var first *Index
	for index := range results {
		if first == nil {
			first = index
		}
		if first != index {
			t.Fatal("duplicate simultaneous build")
		}
	}
}
func TestCacheBackgroundExpiryAndSmallQuotaFailClosed(t *testing.T) {
	cache, err := NewCache(8<<20, 2, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	_, _, release := lease(t, cache, "task", fixtureBinding(), []Document{document("a.go", "needle")})
	release()
	deadline := time.Now().Add(3 * time.Second)
	for {
		cache.mu.Lock()
		empty := len(cache.live) == 0
		cache.mu.Unlock()
		if empty {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle source not expired")
		}
		time.Sleep(20 * time.Millisecond)
	}
	tiny, err := NewCache(1, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer tiny.Close()
	_, _, _, err = tiny.Acquire(context.Background(), c.HashBytes([]byte("task")), fixtureBinding(), []Document{document("a.go", "needle")})
	requireCode(t, err, c.UnsupportedCapability)
	tiny.mu.Lock()
	defer tiny.mu.Unlock()
	if tiny.bytes != 0 || len(tiny.scopes) != 0 || len(tiny.live) != 0 {
		t.Fatal("rejected build leaked metadata")
	}
}
