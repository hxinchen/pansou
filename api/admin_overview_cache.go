package api

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"pansou/storage"
)

const (
	defaultAdminOverviewCacheTTL       = 10 * time.Second
	defaultAdminOverviewRefreshTimeout = 15 * time.Second
	defaultAdminOverviewCacheEntries   = 16
	defaultAdminOverviewTrendDays      = 7
)

type adminOverviewStore interface {
	OverviewSnapshot(context.Context) (storage.OverviewStats, error)
	OverviewActivity(context.Context) (*storage.CollectionRun, []storage.CollectionRun, error)
	Trends(context.Context, int) ([]storage.TrendPoint, error)
}

type adminOverviewCacheConfig struct {
	ttl            time.Duration
	refreshTimeout time.Duration
	maxEntries     int
	now            func() time.Time
}

type adminOverviewCacheEntry struct {
	stats       storage.OverviewStats
	trends      []storage.TrendPoint
	generatedAt time.Time
	expiresAt   time.Time
	lastAccess  time.Time
}

type adminOverviewSnapshot struct {
	stats       storage.OverviewStats
	trends      []storage.TrendPoint
	generatedAt time.Time
	stale       bool
	refreshing  bool
}

type adminOverviewActivity struct {
	activeRun  *storage.CollectionRun
	recentRuns []storage.CollectionRun
	available  bool
}

// adminOverviewCache keeps only the expensive dashboard snapshot in the
// ten-second cache. Collection activity is queried on every overview request;
// its last successful value is retained solely as an outage fallback.
type adminOverviewCache struct {
	store          adminOverviewStore
	ttl            time.Duration
	refreshTimeout time.Duration
	maxEntries     int
	now            func() time.Time

	mu         sync.Mutex
	entries    map[int]adminOverviewCacheEntry
	refreshing map[int]bool
	activity   adminOverviewActivity
	group      singleflight.Group
}

func newAdminOverviewCache(store adminOverviewStore) *adminOverviewCache {
	return newAdminOverviewCacheWithConfig(store, adminOverviewCacheConfig{})
}

func newAdminOverviewCacheWithConfig(store adminOverviewStore, cfg adminOverviewCacheConfig) *adminOverviewCache {
	if store == nil {
		return nil
	}
	if cfg.ttl <= 0 {
		cfg.ttl = defaultAdminOverviewCacheTTL
	}
	if cfg.refreshTimeout <= 0 {
		cfg.refreshTimeout = defaultAdminOverviewRefreshTimeout
	}
	if cfg.maxEntries <= 0 {
		cfg.maxEntries = defaultAdminOverviewCacheEntries
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	return &adminOverviewCache{
		store:          store,
		ttl:            cfg.ttl,
		refreshTimeout: cfg.refreshTimeout,
		maxEntries:     cfg.maxEntries,
		now:            cfg.now,
		entries:        make(map[int]adminOverviewCacheEntry),
		refreshing:     make(map[int]bool),
	}
}

// warmup asynchronously prepares the default seven-day dashboard snapshot.
// It shares the same singleflight key as incoming requests, so a cold request
// arriving during startup waits for this one database refresh instead of
// starting another.
func (c *adminOverviewCache) warmup() {
	if c == nil {
		return
	}
	if !c.markRefreshing(defaultAdminOverviewTrendDays) {
		return
	}
	go c.backgroundRefresh(defaultAdminOverviewTrendDays)
}

func (c *adminOverviewCache) dashboard(ctx context.Context, days int, force bool) (adminOverviewSnapshot, error) {
	snapshot, err := c.snapshot(ctx, days, force)
	if err != nil {
		return adminOverviewSnapshot{}, err
	}
	activeRun, recentRuns, activityStale, err := c.loadActivity(ctx)
	if err != nil {
		// The expensive snapshot is still useful when only the lightweight
		// activity query is temporarily unavailable (for example, after startup
		// prewarm but before the first successful dashboard request).
		snapshot.stale = true
		return snapshot, nil
	}
	snapshot.stats.ActiveRun = activeRun
	snapshot.stats.RecentRuns = recentRuns
	snapshot.stale = snapshot.stale || activityStale
	return snapshot, nil
}

func (c *adminOverviewCache) snapshot(ctx context.Context, days int, force bool) (adminOverviewSnapshot, error) {
	if c == nil || c.store == nil {
		return adminOverviewSnapshot{}, fmt.Errorf("overview cache is disabled")
	}
	days = normalizeOverviewDays(days)
	now := c.now()

	c.mu.Lock()
	entry, cached := c.entries[days]
	if cached {
		entry.lastAccess = now
		c.entries[days] = entry
	}
	if cached && !force && now.Before(entry.expiresAt) {
		result := snapshotFromEntry(entry, false, false)
		c.mu.Unlock()
		return result, nil
	}
	if cached && !force {
		refreshing := c.refreshing[days]
		if !refreshing {
			c.refreshing[days] = true
			refreshing = true
			go c.backgroundRefresh(days)
		}
		result := snapshotFromEntry(entry, true, refreshing)
		c.mu.Unlock()
		return result, nil
	}
	c.mu.Unlock()

	refreshed, err := c.refresh(ctx, days, force)
	if err == nil {
		return snapshotFromEntry(refreshed, false, false), nil
	}

	// A synchronous force refresh must not discard a previously successful
	// snapshot. Cold-start failures still surface as errors because no truthful
	// response is available yet.
	c.mu.Lock()
	fallback, ok := c.entries[days]
	if ok {
		fallback.lastAccess = c.now()
		c.entries[days] = fallback
	}
	refreshing := c.refreshing[days]
	c.mu.Unlock()
	if ok {
		return snapshotFromEntry(fallback, true, refreshing), nil
	}
	return adminOverviewSnapshot{}, err
}

func (c *adminOverviewCache) backgroundRefresh(days int) {
	ctx, cancel := context.WithTimeout(context.Background(), c.refreshTimeout)
	defer cancel()
	_, _ = c.refresh(ctx, days, false)
	c.mu.Lock()
	delete(c.refreshing, days)
	c.mu.Unlock()
}

func (c *adminOverviewCache) refresh(ctx context.Context, days int, force bool) (adminOverviewCacheEntry, error) {
	value, err, _ := c.group.Do(strconv.Itoa(days), func() (any, error) {
		// A startup warmup and a cold request can race around singleflight's
		// completion boundary. Re-check inside the shared call so the loser uses
		// the snapshot that was just published instead of issuing a second query.
		if !force {
			c.mu.Lock()
			entry, ok := c.entries[days]
			fresh := ok && c.now().Before(entry.expiresAt)
			c.mu.Unlock()
			if fresh {
				return entry, nil
			}
		}
		entry, loadErr := c.load(ctx, days)
		if loadErr != nil {
			return adminOverviewCacheEntry{}, loadErr
		}
		c.mu.Lock()
		c.storeEntryLocked(days, entry)
		c.mu.Unlock()
		return entry, nil
	})
	if err != nil {
		return adminOverviewCacheEntry{}, err
	}
	return value.(adminOverviewCacheEntry), nil
}

func (c *adminOverviewCache) load(ctx context.Context, days int) (adminOverviewCacheEntry, error) {
	var (
		stats     storage.OverviewStats
		trends    []storage.TrendPoint
		statsErr  error
		trendsErr error
		wg        sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		stats, statsErr = c.store.OverviewSnapshot(ctx)
	}()
	go func() {
		defer wg.Done()
		trends, trendsErr = c.store.Trends(ctx, days)
	}()
	wg.Wait()
	if statsErr != nil {
		return adminOverviewCacheEntry{}, fmt.Errorf("load overview snapshot: %w", statsErr)
	}
	if trendsErr != nil {
		return adminOverviewCacheEntry{}, fmt.Errorf("load overview trends: %w", trendsErr)
	}
	generatedAt := c.now()
	return adminOverviewCacheEntry{
		stats:       stats,
		trends:      trends,
		generatedAt: generatedAt,
		expiresAt:   generatedAt.Add(c.ttl),
		lastAccess:  generatedAt,
	}, nil
}

func (c *adminOverviewCache) loadActivity(ctx context.Context) (*storage.CollectionRun, []storage.CollectionRun, bool, error) {
	activeRun, recentRuns, err := c.store.OverviewActivity(ctx)
	if err == nil {
		activity := adminOverviewActivity{
			activeRun:  cloneCollectionRun(activeRun),
			recentRuns: cloneCollectionRuns(recentRuns),
			available:  true,
		}
		c.mu.Lock()
		c.activity = activity
		c.mu.Unlock()
		return cloneCollectionRun(activity.activeRun), cloneCollectionRuns(activity.recentRuns), false, nil
	}

	c.mu.Lock()
	fallback := c.activity
	c.mu.Unlock()
	if fallback.available {
		return cloneCollectionRun(fallback.activeRun), cloneCollectionRuns(fallback.recentRuns), true, nil
	}
	return nil, nil, false, fmt.Errorf("load overview activity: %w", err)
}

func (c *adminOverviewCache) markRefreshing(days int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refreshing[days] {
		return false
	}
	c.refreshing[days] = true
	return true
}

func (c *adminOverviewCache) storeEntryLocked(days int, entry adminOverviewCacheEntry) {
	if _, exists := c.entries[days]; !exists && len(c.entries) >= c.maxEntries {
		var (
			oldestDays int
			oldestTime time.Time
			found      bool
		)
		for cachedDays, cachedEntry := range c.entries {
			if !found || cachedEntry.lastAccess.Before(oldestTime) {
				oldestDays = cachedDays
				oldestTime = cachedEntry.lastAccess
				found = true
			}
		}
		if found {
			delete(c.entries, oldestDays)
			delete(c.refreshing, oldestDays)
		}
	}
	c.entries[days] = entry
}

func snapshotFromEntry(entry adminOverviewCacheEntry, stale, refreshing bool) adminOverviewSnapshot {
	return adminOverviewSnapshot{
		stats:       entry.stats,
		trends:      entry.trends,
		generatedAt: entry.generatedAt,
		stale:       stale,
		refreshing:  refreshing,
	}
}

func normalizeOverviewDays(days int) int {
	if days < 1 {
		return 1
	}
	if days > 366 {
		return 366
	}
	return days
}

func cloneCollectionRun(run *storage.CollectionRun) *storage.CollectionRun {
	if run == nil {
		return nil
	}
	cloned := *run
	cloned.Items = append([]storage.CollectionRunItem(nil), run.Items...)
	return &cloned
}

func cloneCollectionRuns(runs []storage.CollectionRun) []storage.CollectionRun {
	if runs == nil {
		return nil
	}
	cloned := make([]storage.CollectionRun, len(runs))
	copy(cloned, runs)
	for i := range cloned {
		cloned[i].Items = append([]storage.CollectionRunItem(nil), runs[i].Items...)
	}
	return cloned
}
