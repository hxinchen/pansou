package plugin

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"pansou/config"
	"pansou/model"
)

func TestAsyncSearchWithResultContinuesOriginalRequestWithoutDuplicate(t *testing.T) {
	previous := config.AppConfig
	config.AppConfig = &config.Config{
		AsyncResponseTimeoutDur:   10 * time.Millisecond,
		PluginTimeout:             time.Second,
		AsyncCacheTTLHours:        1,
		AsyncMaxBackgroundWorkers: 2,
		AsyncMaxBackgroundTasks:   10,
	}
	defer func() { config.AppConfig = previous }()

	instance := NewBaseAsyncPlugin("async-no-duplicate", 1)
	apiResponseCache.Delete("async-no-duplicate:keyword")
	var calls atomic.Int32
	search := func(*http.Client, string, map[string]interface{}) ([]model.SearchResult, error) {
		calls.Add(1)
		time.Sleep(40 * time.Millisecond)
		return []model.SearchResult{{UniqueID: "one"}}, nil
	}

	first, err := instance.AsyncSearchWithResult("keyword", search, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.IsFinal {
		t.Fatal("timed-out response was marked final")
	}
	time.Sleep(80 * time.Millisecond)
	second, err := instance.AsyncSearchWithResult("keyword", search, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !second.IsFinal || len(second.Results) != 1 {
		t.Fatalf("completed cached result = %+v", second)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("search calls = %d, want 1", got)
	}
}

func TestCompletedPluginCacheUpdateIsMonotonic(t *testing.T) {
	key := "monotonic:test"
	apiResponseCache.Delete(key)
	storeCompletedPluginResponse(key, []model.SearchResult{{UniqueID: "one"}, {UniqueID: "two"}}, time.Now())
	merged := storeCompletedPluginResponse(key, []model.SearchResult{{UniqueID: "one"}}, time.Now())
	if len(merged.Results) != 2 || !merged.Complete {
		t.Fatalf("merged plugin cache = %+v", merged)
	}
}
