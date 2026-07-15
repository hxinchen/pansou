package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"pansou/config"
	"pansou/plugin"
)

func TestSearchTraceStatusPrecedence(t *testing.T) {
	tests := []struct {
		name     string
		statuses []SearchCacheStatus
		want     SearchCacheStatus
	}{
		{name: "empty", want: SearchCacheNotApplicable},
		{name: "hit", statuses: []SearchCacheStatus{SearchCacheHit}, want: SearchCacheHit},
		{name: "bypass over hit", statuses: []SearchCacheStatus{SearchCacheHit, SearchCacheBypass}, want: SearchCacheBypass},
		{name: "miss over bypass", statuses: []SearchCacheStatus{SearchCacheBypass, SearchCacheMiss}, want: SearchCacheMiss},
		{name: "refresh over miss", statuses: []SearchCacheStatus{SearchCacheMiss, SearchCacheRefresh}, want: SearchCacheRefresh},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trace := NewSearchTrace()
			for _, status := range test.statuses {
				trace.Mark(status)
			}
			if got := trace.Status(); got != test.want {
				t.Fatalf("Status() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSearchTraceConcurrentMarks(t *testing.T) {
	trace := NewSearchTrace()
	statuses := []SearchCacheStatus{SearchCacheHit, SearchCacheBypass, SearchCacheMiss, SearchCacheRefresh}
	var wg sync.WaitGroup
	for index := 0; index < 100; index++ {
		for _, status := range statuses {
			wg.Add(1)
			go func(status SearchCacheStatus) {
				defer wg.Done()
				trace.Mark(status)
			}(status)
		}
	}
	wg.Wait()
	if got := trace.Status(); got != SearchCacheRefresh {
		t.Fatalf("Status() = %q, want %q", got, SearchCacheRefresh)
	}
}

func TestSearchTraceContext(t *testing.T) {
	trace := NewSearchTrace()
	ctx := ContextWithSearchTrace(context.Background(), trace)
	MarkSearchCacheStatus(ctx, SearchCacheHit)
	if got := SearchTraceFromContext(ctx); got != trace {
		t.Fatal("SearchTraceFromContext returned a different trace")
	}
	if got := trace.Status(); got != SearchCacheHit {
		t.Fatalf("Status() = %q, want hit", got)
	}
	MarkSearchCacheStatus(context.Background(), SearchCacheMiss)
}

func TestLiveSearchTraceCacheBypassAndRefresh(t *testing.T) {
	previous := config.AppConfig
	config.AppConfig = &config.Config{
		AsyncPluginEnabled: true,
		DefaultConcurrency: 1,
		PluginTimeout:      time.Second,
		CacheEnabled:       false,
	}
	defer func() { config.AppConfig = previous }()

	search := NewSearchService(plugin.NewPluginManager())
	for _, test := range []struct {
		name  string
		force bool
		want  SearchCacheStatus
	}{
		{name: "disabled cache", want: SearchCacheBypass},
		{name: "force refresh", force: true, want: SearchCacheRefresh},
	} {
		t.Run(test.name, func(t *testing.T) {
			trace := NewSearchTrace()
			ctx := ContextWithSearchTrace(context.Background(), trace)
			_, err := search.SearchContext(ctx, ContextSearchRequest{
				Keyword: "sample", SourceType: "plugin", ResultType: "all",
				ForceRefresh: test.force, Identity: SearchIdentity{Actor: SearchActorUser, UserID: 1},
			})
			if err != nil {
				t.Fatalf("SearchContext: %v", err)
			}
			if got := trace.Status(); got != test.want {
				t.Fatalf("cache status = %q, want %q", got, test.want)
			}
		})
	}
}
