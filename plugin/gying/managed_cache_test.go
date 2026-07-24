package gying

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagedSearchCacheIsBounded(t *testing.T) {
	plugin := &GyingPlugin{}
	outcome := gyingSearchOutcome{Complete: true}
	for index := 0; index < managedSearchCacheMaxEntries+32; index++ {
		plugin.storeManagedSearchCache(fmt.Sprintf("key-%d", index), outcome)
	}

	if count := atomic.LoadInt64(&plugin.managedCacheCount); count > managedSearchCacheMaxEntries {
		t.Fatalf("tracked cache entries = %d, max %d", count, managedSearchCacheMaxEntries)
	}
	actual := 0
	plugin.managedSearchCache.Range(func(_, _ any) bool {
		actual++
		return true
	})
	if actual > managedSearchCacheMaxEntries {
		t.Fatalf("actual cache entries = %d, max %d", actual, managedSearchCacheMaxEntries)
	}
}

func TestManagedStateCleanupRemovesExpiredEntries(t *testing.T) {
	plugin := &GyingPlugin{}
	plugin.managedSearchCache.Store("expired", &managedSearchCacheEntry{staleUntil: time.Now().Add(-time.Second)})
	atomic.StoreInt64(&plugin.managedCacheCount, 1)

	plugin.cleanupManagedState(time.Now())
	if _, exists := plugin.managedSearchCache.Load("expired"); exists {
		t.Fatal("expired managed search entry remains cached")
	}
	if count := atomic.LoadInt64(&plugin.managedCacheCount); count != 0 {
		t.Fatalf("tracked cache entries = %d, want 0", count)
	}
}
