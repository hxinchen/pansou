package api

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pansou/storage"
)

type fakeAdminOverviewStore struct {
	snapshotFn func(context.Context) (storage.OverviewStats, error)
	activityFn func(context.Context) (*storage.CollectionRun, []storage.CollectionRun, error)
	trendsFn   func(context.Context, int) ([]storage.TrendPoint, error)

	snapshotCalls atomic.Int32
	activityCalls atomic.Int32
	trendsCalls   atomic.Int32
}

func (s *fakeAdminOverviewStore) OverviewSnapshot(ctx context.Context) (storage.OverviewStats, error) {
	s.snapshotCalls.Add(1)
	if s.snapshotFn != nil {
		return s.snapshotFn(ctx)
	}
	return storage.OverviewStats{}, nil
}

func (s *fakeAdminOverviewStore) OverviewActivity(ctx context.Context) (*storage.CollectionRun, []storage.CollectionRun, error) {
	s.activityCalls.Add(1)
	if s.activityFn != nil {
		return s.activityFn(ctx)
	}
	return nil, []storage.CollectionRun{}, nil
}

func (s *fakeAdminOverviewStore) Trends(ctx context.Context, days int) ([]storage.TrendPoint, error) {
	s.trendsCalls.Add(1)
	if s.trendsFn != nil {
		return s.trendsFn(ctx, days)
	}
	return []storage.TrendPoint{}, nil
}

type adminOverviewTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *adminOverviewTestClock) Time() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *adminOverviewTestClock) Add(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

func newTestAdminOverviewCache(store adminOverviewStore, clock *adminOverviewTestClock) *adminOverviewCache {
	return newAdminOverviewCacheWithConfig(store, adminOverviewCacheConfig{
		ttl:            10 * time.Second,
		refreshTimeout: time.Second,
		maxEntries:     16,
		now:            clock.Time,
	})
}

func TestAdminOverviewCacheCachesHeavyStatsButReloadsActivity(t *testing.T) {
	clock := &adminOverviewTestClock{now: time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC)}
	store := &fakeAdminOverviewStore{
		snapshotFn: func(context.Context) (storage.OverviewStats, error) {
			return storage.OverviewStats{ResourceCount: 42}, nil
		},
		activityFn: func(context.Context) (*storage.CollectionRun, []storage.CollectionRun, error) {
			return &storage.CollectionRun{ID: 7, Status: "running"}, []storage.CollectionRun{{ID: 6}}, nil
		},
		trendsFn: func(_ context.Context, days int) ([]storage.TrendPoint, error) {
			return []storage.TrendPoint{{NewCount: int64(days)}}, nil
		},
	}
	cache := newTestAdminOverviewCache(store, clock)

	first, err := cache.dashboard(context.Background(), 7, false)
	if err != nil {
		t.Fatalf("first dashboard: %v", err)
	}
	second, err := cache.dashboard(context.Background(), 7, false)
	if err != nil {
		t.Fatalf("second dashboard: %v", err)
	}
	if first.stats.ResourceCount != 42 || second.stats.ResourceCount != 42 {
		t.Fatalf("resource count = %d/%d, want 42", first.stats.ResourceCount, second.stats.ResourceCount)
	}
	if second.stats.ActiveRun == nil || second.stats.ActiveRun.ID != 7 || len(second.stats.RecentRuns) != 1 {
		t.Fatalf("activity not merged: %+v", second.stats)
	}
	if first.generatedAt != clock.Time() || first.stale || first.refreshing {
		t.Fatalf("unexpected first metadata: %+v", first)
	}
	if got := store.snapshotCalls.Load(); got != 1 {
		t.Fatalf("snapshot calls = %d, want 1", got)
	}
	if got := store.trendsCalls.Load(); got != 1 {
		t.Fatalf("trends calls = %d, want 1", got)
	}
	if got := store.activityCalls.Load(); got != 2 {
		t.Fatalf("activity calls = %d, want 2", got)
	}
}

func TestAdminOverviewCacheExpiredSnapshotReturnsImmediatelyAndRefreshes(t *testing.T) {
	clock := &adminOverviewTestClock{now: time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC)}
	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	var calls atomic.Int32
	store := &fakeAdminOverviewStore{
		snapshotFn: func(ctx context.Context) (storage.OverviewStats, error) {
			if calls.Add(1) == 1 {
				return storage.OverviewStats{ResourceCount: 1}, nil
			}
			close(refreshStarted)
			select {
			case <-releaseRefresh:
				return storage.OverviewStats{ResourceCount: 2}, nil
			case <-ctx.Done():
				return storage.OverviewStats{}, ctx.Err()
			}
		},
	}
	cache := newTestAdminOverviewCache(store, clock)
	if _, err := cache.dashboard(context.Background(), 7, false); err != nil {
		t.Fatalf("prime cache: %v", err)
	}
	clock.Add(11 * time.Second)

	stale, err := cache.dashboard(context.Background(), 7, false)
	if err != nil {
		t.Fatalf("stale dashboard: %v", err)
	}
	if stale.stats.ResourceCount != 1 || !stale.stale || !stale.refreshing {
		t.Fatalf("stale result = %+v, want cached value marked refreshing", stale)
	}
	select {
	case <-refreshStarted:
	case <-time.After(time.Second):
		t.Fatal("background refresh did not start")
	}
	close(releaseRefresh)

	deadline := time.Now().Add(time.Second)
	for {
		fresh, getErr := cache.snapshot(context.Background(), 7, false)
		if getErr != nil {
			t.Fatalf("read refreshed snapshot: %v", getErr)
		}
		if fresh.stats.ResourceCount == 2 && !fresh.stale {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background refresh did not publish: %+v", fresh)
		}
		time.Sleep(time.Millisecond)
	}
	if got := store.snapshotCalls.Load(); got != 2 {
		t.Fatalf("snapshot calls = %d, want 2", got)
	}
}

func TestAdminOverviewCacheForceRefreshAndFailureFallback(t *testing.T) {
	clock := &adminOverviewTestClock{now: time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC)}
	var (
		mu      sync.Mutex
		value   int64 = 1
		loadErr error
	)
	store := &fakeAdminOverviewStore{
		snapshotFn: func(context.Context) (storage.OverviewStats, error) {
			mu.Lock()
			defer mu.Unlock()
			return storage.OverviewStats{ResourceCount: value}, loadErr
		},
	}
	cache := newTestAdminOverviewCache(store, clock)
	if _, err := cache.dashboard(context.Background(), 7, false); err != nil {
		t.Fatalf("prime cache: %v", err)
	}

	mu.Lock()
	value = 2
	mu.Unlock()
	forced, err := cache.dashboard(context.Background(), 7, true)
	if err != nil {
		t.Fatalf("force refresh: %v", err)
	}
	if forced.stats.ResourceCount != 2 || forced.stale {
		t.Fatalf("forced result = %+v, want fresh value 2", forced)
	}

	mu.Lock()
	loadErr = errors.New("database unavailable")
	mu.Unlock()
	fallback, err := cache.dashboard(context.Background(), 7, true)
	if err != nil {
		t.Fatalf("force fallback: %v", err)
	}
	if fallback.stats.ResourceCount != 2 || !fallback.stale {
		t.Fatalf("fallback = %+v, want stale value 2", fallback)
	}

	cold := newTestAdminOverviewCache(store, clock)
	if _, err := cold.dashboard(context.Background(), 7, false); err == nil {
		t.Fatal("cold cache failure should return an error")
	}
}

func TestAdminOverviewCacheSingleflightAndEntryLimit(t *testing.T) {
	clock := &adminOverviewTestClock{now: time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC)}
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	store := &fakeAdminOverviewStore{
		snapshotFn: func(ctx context.Context) (storage.OverviewStats, error) {
			once.Do(func() { close(started) })
			select {
			case <-release:
				return storage.OverviewStats{ResourceCount: 1}, nil
			case <-ctx.Done():
				return storage.OverviewStats{}, ctx.Err()
			}
		},
	}
	cache := newTestAdminOverviewCache(store, clock)

	const requests = 24
	var wg sync.WaitGroup
	errs := make(chan error, requests)
	wg.Add(requests)
	for range requests {
		go func() {
			defer wg.Done()
			_, err := cache.snapshot(context.Background(), 7, false)
			errs <- err
		}()
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("cold refresh did not start")
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent snapshot: %v", err)
		}
	}
	if got := store.snapshotCalls.Load(); got != 1 {
		t.Fatalf("snapshot calls = %d, want 1", got)
	}
	if got := store.trendsCalls.Load(); got != 1 {
		t.Fatalf("trends calls = %d, want 1", got)
	}

	for days := 1; days <= 17; days++ {
		if days == 7 {
			continue
		}
		clock.Add(time.Millisecond)
		if _, err := cache.snapshot(context.Background(), days, false); err != nil {
			t.Fatalf("cache days=%d: %v", days, err)
		}
	}
	cache.mu.Lock()
	entryCount := len(cache.entries)
	cache.mu.Unlock()
	if entryCount != 16 {
		t.Fatalf("entry count = %d, want 16", entryCount)
	}
}

func TestAdminOverviewCacheWarmupIsAsyncAndSharedWithColdRequest(t *testing.T) {
	clock := &adminOverviewTestClock{now: time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC)}
	started := make(chan struct{})
	release := make(chan struct{})
	store := &fakeAdminOverviewStore{
		snapshotFn: func(ctx context.Context) (storage.OverviewStats, error) {
			close(started)
			select {
			case <-release:
				return storage.OverviewStats{ResourceCount: 5}, nil
			case <-ctx.Done():
				return storage.OverviewStats{}, ctx.Err()
			}
		},
	}
	cache := newTestAdminOverviewCache(store, clock)
	cache.warmup()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("warmup did not start asynchronously")
	}

	resultCh := make(chan adminOverviewSnapshot, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := cache.snapshot(context.Background(), 7, false)
		resultCh <- result
		errCh <- err
	}()
	close(release)
	if err := <-errCh; err != nil {
		t.Fatalf("cold request joining warmup: %v", err)
	}
	result := <-resultCh
	if result.stats.ResourceCount != 5 {
		t.Fatalf("resource count = %d, want 5", result.stats.ResourceCount)
	}
	if got := store.snapshotCalls.Load(); got != 1 {
		t.Fatalf("snapshot calls = %d, want one shared refresh", got)
	}
}

func TestAdminOverviewCacheUsesLastActivityOnlyOnFailure(t *testing.T) {
	clock := &adminOverviewTestClock{now: time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC)}
	var fail atomic.Bool
	store := &fakeAdminOverviewStore{
		activityFn: func(context.Context) (*storage.CollectionRun, []storage.CollectionRun, error) {
			if fail.Load() {
				return nil, nil, errors.New("activity unavailable")
			}
			return &storage.CollectionRun{ID: 9}, []storage.CollectionRun{{ID: 8}}, nil
		},
	}
	cache := newTestAdminOverviewCache(store, clock)
	if _, err := cache.dashboard(context.Background(), 7, false); err != nil {
		t.Fatalf("prime activity: %v", err)
	}
	fail.Store(true)
	result, err := cache.dashboard(context.Background(), 7, false)
	if err != nil {
		t.Fatalf("activity fallback: %v", err)
	}
	if !result.stale || result.stats.ActiveRun == nil || result.stats.ActiveRun.ID != 9 {
		t.Fatalf("activity fallback result = %+v", result)
	}
}

func TestAdminOverviewCacheStillServesSnapshotWhenInitialActivityFails(t *testing.T) {
	clock := &adminOverviewTestClock{now: time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC)}
	store := &fakeAdminOverviewStore{
		snapshotFn: func(context.Context) (storage.OverviewStats, error) {
			return storage.OverviewStats{ResourceCount: 11}, nil
		},
		activityFn: func(context.Context) (*storage.CollectionRun, []storage.CollectionRun, error) {
			return nil, nil, errors.New("activity unavailable")
		},
	}
	cache := newTestAdminOverviewCache(store, clock)
	result, err := cache.dashboard(context.Background(), 7, false)
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	if result.stats.ResourceCount != 11 || !result.stale {
		t.Fatalf("result = %+v, want usable heavy snapshot marked stale", result)
	}
}
