package service

import (
	"fmt"
	"sync"
	"time"

	"pansou/model"
	"pansou/util/cache"
)

type cachedSearchResults struct {
	Results    []model.SearchResult   `json:"results"`
	Complete   bool                   `json:"complete"`
	Execution  *model.SearchExecution `json:"execution,omitempty"`
	FreshUntil time.Time              `json:"fresh_until,omitempty"`
}

const (
	negativeSearchCacheTTL = 10 * time.Minute
	tgChannelStaleGrace    = 6 * time.Hour
)

func (c cachedSearchResults) fresh(now time.Time) bool {
	return c.FreshUntil.IsZero() || now.Before(c.FreshUntil)
}

func aggregateSearchCacheTTL(results []model.SearchResult, complete bool, regular time.Duration) time.Duration {
	if complete && len(results) == 0 {
		return negativeSearchCacheTTL
	}
	return regular
}

var searchCacheLocks [64]sync.Mutex

func searchCacheLock(key string) *sync.Mutex {
	hash := uint32(2166136261)
	for index := 0; index < len(key); index++ {
		hash ^= uint32(key[index])
		hash *= 16777619
	}
	return &searchCacheLocks[hash%uint32(len(searchCacheLocks))]
}

func loadCachedSearch(cacheStore *cache.EnhancedTwoLevelCache, key string) (cachedSearchResults, bool, error) {
	if cacheStore == nil {
		return cachedSearchResults{}, false, nil
	}
	data, hit, err := cacheStore.Get(key)
	if err != nil || !hit {
		return cachedSearchResults{}, hit, err
	}
	serializer := cacheStore.GetSerializer()
	var envelope cachedSearchResults
	if err := serializer.Deserialize(data, &envelope); err == nil {
		return envelope, true, nil
	}

	// Cache files written by older releases contained only the result slice.
	// Keep those results as a non-final seed so they cannot masquerade as a
	// complete response after an upgrade.
	var legacy []model.SearchResult
	if err := serializer.Deserialize(data, &legacy); err != nil {
		return cachedSearchResults{}, true, err
	}
	return cachedSearchResults{Results: legacy, Complete: false}, true, nil
}

func mergeCachedSearch(existing cachedSearchResults, incoming []model.SearchResult, complete bool) cachedSearchResults {
	return cachedSearchResults{
		Results:    mergeSearchResults(existing.Results, incoming),
		Complete:   existing.Complete || complete,
		FreshUntil: existing.FreshUntil,
	}
}

func mergeCachedSearchWithExecution(existing cachedSearchResults, incoming []model.SearchResult, complete bool, execution *model.SearchExecution) cachedSearchResults {
	merged := mergeCachedSearch(existing, incoming, complete)
	if execution != nil {
		copyExecution := *execution
		merged.Execution = &copyExecution
	} else {
		merged.Execution = existing.Execution
	}
	return merged
}

func storeFreshCachedSearch(cacheStore *cache.EnhancedTwoLevelCache, key string, results []model.SearchResult, freshTTL, staleGrace time.Duration, execution *model.SearchExecution) error {
	if cacheStore == nil {
		return nil
	}
	envelope := cachedSearchResults{Results: results, Complete: true, FreshUntil: time.Now().Add(freshTTL)}
	if execution != nil {
		copyExecution := *execution
		envelope.Execution = &copyExecution
	}
	data, err := cacheStore.GetSerializer().Serialize(envelope)
	if err != nil {
		return fmt.Errorf("serialize fresh search cache: %w", err)
	}
	if staleGrace < 0 {
		staleGrace = 0
	}
	if err := cacheStore.SetBothLevels(key, data, freshTTL+staleGrace); err != nil {
		return fmt.Errorf("store fresh search cache: %w", err)
	}
	return nil
}

func mergeAndStoreCachedSearch(cacheStore *cache.EnhancedTwoLevelCache, key string, incoming []model.SearchResult, ttl time.Duration, complete bool) (cachedSearchResults, error) {
	if cacheStore == nil {
		return cachedSearchResults{Results: incoming, Complete: complete}, nil
	}
	lock := searchCacheLock(key)
	lock.Lock()
	defer lock.Unlock()

	existing, hit, err := loadCachedSearch(cacheStore, key)
	if err != nil {
		return cachedSearchResults{}, fmt.Errorf("load search cache: %w", err)
	}
	if !hit {
		existing = cachedSearchResults{}
	}
	merged := mergeCachedSearch(existing, incoming, complete)
	data, err := cacheStore.GetSerializer().Serialize(merged)
	if err != nil {
		return cachedSearchResults{}, fmt.Errorf("serialize search cache: %w", err)
	}
	if err := cacheStore.SetBothLevels(key, data, ttl); err != nil {
		return cachedSearchResults{}, fmt.Errorf("store search cache: %w", err)
	}
	return merged, nil
}

func mergeAndStoreCachedSearchWithExecution(cacheStore *cache.EnhancedTwoLevelCache, key string, incoming []model.SearchResult, ttl time.Duration, complete bool, execution *model.SearchExecution) (cachedSearchResults, error) {
	if cacheStore == nil {
		return mergeCachedSearchWithExecution(cachedSearchResults{}, incoming, complete, execution), nil
	}
	lock := searchCacheLock(key)
	lock.Lock()
	defer lock.Unlock()
	existing, hit, err := loadCachedSearch(cacheStore, key)
	if err != nil {
		return cachedSearchResults{}, fmt.Errorf("load search cache: %w", err)
	}
	if !hit {
		existing = cachedSearchResults{}
	}
	merged := mergeCachedSearchWithExecution(existing, incoming, complete, execution)
	data, err := cacheStore.GetSerializer().Serialize(merged)
	if err != nil {
		return cachedSearchResults{}, fmt.Errorf("serialize search cache: %w", err)
	}
	if err := cacheStore.SetBothLevels(key, data, ttl); err != nil {
		return cachedSearchResults{}, fmt.Errorf("store search cache: %w", err)
	}
	return merged, nil
}
