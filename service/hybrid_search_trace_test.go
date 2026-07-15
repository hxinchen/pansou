package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"pansou/model"
	"pansou/plugin"
	"pansou/storage"
)

func tracedSearchContext() (context.Context, *SearchTrace) {
	trace := NewSearchTrace()
	return ContextWithSearchTrace(context.Background(), trace), trace
}

func tracedHybridRequest(force bool) ContextSearchRequest {
	return ContextSearchRequest{
		Keyword: "sample", Channels: []string{"channel-a"}, Concurrency: 1,
		ForceRefresh: force, ResultType: "all", SourceType: "tg",
		Identity: SearchIdentity{Actor: SearchActorUser, UserID: 1},
	}
}

func storedTracePage(lastSeen time.Time) storage.ResourcePage {
	return storage.ResourcePage{Total: 1, Items: []storage.Resource{{
		ID: 1, URL: "https://pan.quark.cn/s/example", NormalizedURL: "https://pan.quark.cn/s/example",
		Platform: "quark", Title: "stored", CheckStatus: storage.CheckValid, LastSeenAt: lastSeen,
		Sources: []storage.ResourceSource{{SourceType: "tg", SourceKey: "channel-a", UniqueID: "tg-1"}},
	}}}
}

func TestHybridSearchTraceCachePaths(t *testing.T) {
	tests := []struct {
		name  string
		store *fakeResourceStore
		force bool
		want  SearchCacheStatus
	}{
		{name: "database hit", store: &fakeResourceStore{pages: []storage.ResourcePage{storedTracePage(time.Now())}}, want: SearchCacheHit},
		{name: "database miss", store: &fakeResourceStore{}, want: SearchCacheMiss},
		{name: "database unavailable", store: &fakeResourceStore{err: errors.New("database down")}, want: SearchCacheBypass},
		{name: "forced refresh", store: &fakeResourceStore{}, force: true, want: SearchCacheRefresh},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, trace := tracedSearchContext()
			hybrid := NewHybridSearchService(&fakeLiveSearch{response: sampleLiveResponse()}, test.store, time.Hour)
			if _, err := hybrid.SearchContext(ctx, tracedHybridRequest(test.force)); err != nil {
				t.Fatalf("SearchContext: %v", err)
			}
			if got := trace.Status(); got != test.want {
				t.Fatalf("cache status = %q, want %q", got, test.want)
			}
		})
	}
}

type backgroundTraceSearch struct {
	done chan struct{}
}

func (s *backgroundTraceSearch) Search(string, []string, int, bool, string, string, []string, []string, map[string]interface{}) (model.SearchResponse, error) {
	return sampleLiveResponse(), nil
}

func (s *backgroundTraceSearch) SearchContext(ctx context.Context, _ ContextSearchRequest) (model.SearchResponse, error) {
	MarkSearchCacheStatus(ctx, SearchCacheRefresh)
	close(s.done)
	return sampleLiveResponse(), nil
}

func (*backgroundTraceSearch) GetPluginManager() *plugin.PluginManager { return nil }

func TestHybridBackgroundRefreshUsesIndependentTrace(t *testing.T) {
	ctx, trace := tracedSearchContext()
	live := &backgroundTraceSearch{done: make(chan struct{})}
	store := &fakeResourceStore{pages: []storage.ResourcePage{storedTracePage(time.Now().Add(-2 * time.Hour))}}
	hybrid := NewHybridSearchService(live, store, time.Hour)
	if _, err := hybrid.SearchContext(ctx, tracedHybridRequest(false)); err != nil {
		t.Fatalf("SearchContext: %v", err)
	}
	select {
	case <-live.done:
	case <-time.After(time.Second):
		t.Fatal("background refresh did not run")
	}
	if got := trace.Status(); got != SearchCacheHit {
		t.Fatalf("request trace changed by background refresh: %q", got)
	}
}
