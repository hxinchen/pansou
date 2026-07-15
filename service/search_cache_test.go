package service

import (
	"testing"

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

func TestMergeCachedSearchKeepsPartialSeedNonFinal(t *testing.T) {
	existing := cachedSearchResults{Results: []model.SearchResult{{UniqueID: "one"}}}
	merged := mergeCachedSearch(existing, []model.SearchResult{{UniqueID: "two"}}, false)
	if len(merged.Results) != 2 || merged.Complete {
		t.Fatalf("merged cache = %+v", merged)
	}
}
