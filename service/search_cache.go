package service

import (
	"fmt"
	"sync"
	"time"

	"pansou/model"
	"pansou/util/cache"
)

type cachedSearchResults struct {
	Results  []model.SearchResult `json:"results"`
	Complete bool                 `json:"complete"`
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
		Results:  mergeSearchResults(existing.Results, incoming),
		Complete: existing.Complete || complete,
	}
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
