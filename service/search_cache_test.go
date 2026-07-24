package service

import (
	"testing"
	"time"

	"pansou/model"
)

func TestMergeCachedSearchNeverShrinksOrDowngradesCompleteness(t *testing.T) {
	existing := cachedSearchResults{
		Results:  []model.SearchResult{{UniqueID: "one"}, {UniqueID: "two"}},
		Complete: true,
	}
	merged := mergeCachedSearch(existing, []model.SearchResult{{UniqueID: "one"}}, false)
	if len(merged.Results) != 2 {
		t.Fatalf("merged results = %d, want 2", len(merged.Results))
	}
	if !merged.Complete {
		t.Fatal("partial update downgraded a complete cache entry")
	}
}

func TestCachedSearchFreshness(t *testing.T) {
	now := time.Now()
	if !(cachedSearchResults{}).fresh(now) {
		t.Fatal("legacy cache entries must remain fresh until their storage TTL expires")
	}
	if (cachedSearchResults{FreshUntil: now.Add(-time.Second)}).fresh(now) {
		t.Fatal("expired freshness marker was treated as fresh")
	}
	if !(cachedSearchResults{FreshUntil: now.Add(time.Second)}).fresh(now) {
		t.Fatal("future freshness marker was treated as stale")
	}
}

func TestAggregateSearchCacheTTLUsesShortNegativeTTL(t *testing.T) {
	regular := time.Hour
	if got := aggregateSearchCacheTTL(nil, true, regular); got != negativeSearchCacheTTL {
		t.Fatalf("negative ttl = %v", got)
	}
	if got := aggregateSearchCacheTTL(nil, false, regular); got != regular {
		t.Fatalf("partial empty ttl = %v", got)
	}
	if got := aggregateSearchCacheTTL([]model.SearchResult{{UniqueID: "one"}}, true, regular); got != regular {
		t.Fatalf("non-empty ttl = %v", got)
	}
}

func TestMergeCachedSearchKeepsPartialSeedNonFinal(t *testing.T) {
	existing := cachedSearchResults{Results: []model.SearchResult{{UniqueID: "one"}}}
	merged := mergeCachedSearch(existing, []model.SearchResult{{UniqueID: "two"}}, false)
	if len(merged.Results) != 2 || merged.Complete {
		t.Fatalf("merged cache = %+v", merged)
	}
}
