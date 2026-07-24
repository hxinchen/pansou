package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"pansou/collection"
	"pansou/model"
	"pansou/plugin"
	"pansou/storage"
)

type fakeLiveSearch struct {
	mu       sync.Mutex
	calls    int
	forced   []bool
	response model.SearchResponse
	err      error
}

func (f *fakeLiveSearch) Search(_ string, _ []string, _ int, force bool, _ string, _ string, _ []string, _ []string, _ map[string]interface{}) (model.SearchResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.forced = append(f.forced, force)
	return f.response, f.err
}

func (f *fakeLiveSearch) GetPluginManager() *plugin.PluginManager { return nil }

type contextAwareLiveSearch struct {
	fakeLiveSearch
	contextErr error
}

type blockingLiveSearch struct {
	mu       sync.Mutex
	calls    int
	started  chan struct{}
	release  chan struct{}
	startOne sync.Once
}

func (s *blockingLiveSearch) Search(string, []string, int, bool, string, string, []string, []string, map[string]interface{}) (model.SearchResponse, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	s.startOne.Do(func() { close(s.started) })
	<-s.release
	return sampleLiveResponse(), nil
}

func (*blockingLiveSearch) GetPluginManager() *plugin.PluginManager { return nil }

type resolvingLiveSearch struct {
	fakeLiveSearch
	channels     []string
	plugins      []string
	requiresLive bool
}

func (f *resolvingLiveSearch) ResolveSearchRequest(_ context.Context, request ContextSearchRequest) (ContextSearchRequest, error) {
	request.Channels = append([]string(nil), f.channels...)
	request.Plugins = append([]string(nil), f.plugins...)
	request.requiresLiveTG = f.requiresLive
	return request, nil
}

func TestHybridSearchCustomChannelBypassesDatabase(t *testing.T) {
	store := &fakeResourceStore{pages: []storage.ResourcePage{{Total: 1}}}
	live := &resolvingLiveSearch{
		fakeLiveSearch: fakeLiveSearch{response: sampleLiveResponse()},
		channels:       []string{"custom_channel"},
		requiresLive:   true,
	}
	hybrid := NewHybridSearchService(live, store, time.Hour)

	_, err := hybrid.Search("sample", []string{"custom_channel"}, 1, false, "all", "tg", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.queries) != 0 {
		t.Fatalf("database queries = %d, want 0 for custom-channel live search", len(store.queries))
	}
	if live.calls != 1 || live.forced[0] {
		t.Fatalf("live calls/force = %d/%v, want 1/false", live.calls, live.forced)
	}
}

func (f *contextAwareLiveSearch) SearchContext(ctx context.Context, request ContextSearchRequest) (model.SearchResponse, error) {
	f.contextErr = ctx.Err()
	return f.Search(
		request.Keyword, request.Channels, request.Concurrency, request.ForceRefresh,
		request.ResultType, request.SourceType, request.Plugins, request.CloudTypes, request.Ext,
	)
}

type fakeResourceStore struct {
	mu         sync.Mutex
	pages      []storage.ResourcePage
	err        error
	queries    []storage.ResourceFilter
	upserts    int
	lastUpsert model.SearchResponse
}

func (f *fakeResourceStore) SearchResources(_ context.Context, filter storage.ResourceFilter) (storage.ResourcePage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, filter)
	if f.err != nil {
		return storage.ResourcePage{}, f.err
	}
	if len(f.pages) == 0 {
		return storage.ResourcePage{}, nil
	}
	page := f.pages[0]
	f.pages = f.pages[1:]
	return page, nil
}

func (f *fakeResourceStore) UpsertSearchResponse(_ context.Context, _ string, _ string, _ string, response model.SearchResponse) (storage.UpsertSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts++
	f.lastUpsert = response
	return storage.UpsertSummary{Seen: 1, Inserted: 1}, nil
}

func sampleLiveResponse() model.SearchResponse {
	return model.SearchResponse{
		Total: 1,
		Results: []model.SearchResult{{
			UniqueID: "tg-1", Channel: "channel-a", Datetime: time.Now(), Title: "sample",
			Links: []model.Link{{Type: "quark", URL: "https://pan.quark.cn/s/example"}},
		}},
	}
}

func TestHybridSearchDatabaseHitReturnsWithoutLiveSearch(t *testing.T) {
	now := time.Now()
	store := &fakeResourceStore{pages: []storage.ResourcePage{{
		Total: 1,
		Items: []storage.Resource{{
			ID: 1, URL: "https://pan.quark.cn/s/example", NormalizedURL: "https://pan.quark.cn/s/example",
			Platform: "quark", Title: "stored", CheckStatus: storage.CheckValid, LastSeenAt: now,
			Sources: []storage.ResourceSource{{SourceType: "tg", SourceKey: "channel-a", UniqueID: "tg-1"}},
		}},
	}}}
	live := &fakeLiveSearch{response: sampleLiveResponse()}
	hybrid := NewHybridSearchService(live, store, time.Hour)

	response, err := hybrid.Search("sample", []string{"channel-a"}, 1, false, "all", "tg", nil, nil, nil)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if response.Total != 1 {
		t.Fatalf("Total = %d, want 1", response.Total)
	}
	if live.calls != 0 {
		t.Fatalf("live calls = %d, want 0", live.calls)
	}
}

func TestHybridSearchCoalescesConcurrentIdenticalRequests(t *testing.T) {
	live := &blockingLiveSearch{started: make(chan struct{}), release: make(chan struct{})}
	store := &fakeResourceStore{pages: []storage.ResourcePage{{}}}
	hybrid := NewHybridSearchService(live, store, time.Hour)

	const callers = 8
	start := make(chan struct{})
	errorsByCaller := make(chan error, callers)
	var wait sync.WaitGroup
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := hybrid.Search("sample", []string{"channel-a"}, 1, false, "all", "tg", nil, nil, nil)
			errorsByCaller <- err
		}()
	}
	close(start)
	<-live.started
	time.Sleep(25 * time.Millisecond)
	close(live.release)
	wait.Wait()
	close(errorsByCaller)
	for err := range errorsByCaller {
		if err != nil {
			t.Fatal(err)
		}
	}
	live.mu.Lock()
	calls := live.calls
	live.mu.Unlock()
	if calls != 1 {
		t.Fatalf("live calls = %d, want 1", calls)
	}
	if store.upserts != 1 {
		t.Fatalf("upserts = %d, want 1", store.upserts)
	}
}

func TestHybridDatabaseSearchFiltersBeforePagingAndReadsEveryPage(t *testing.T) {
	now := time.Now()
	resources := make([]storage.Resource, 201)
	for index := range resources {
		url := fmt.Sprintf("https://pan.quark.cn/s/%03d", index)
		resources[index] = storage.Resource{
			ID: int64(index + 1), URL: url, NormalizedURL: url, Platform: "quark",
			Title: "sample resource", CheckStatus: storage.CheckValid, LastSeenAt: now,
			Sources: []storage.ResourceSource{{SourceType: "tg", SourceKey: "channel-a"}},
		}
	}
	store := &fakeResourceStore{pages: []storage.ResourcePage{
		{Total: 201, Page: 1, PageSize: 200, Items: resources[:200]},
		{Total: 201, Page: 2, PageSize: 200, Items: resources[200:]},
	}}
	hybrid := NewHybridSearchService(&fakeLiveSearch{response: sampleLiveResponse()}, store, time.Hour)

	response, err := hybrid.Search("sample", []string{"channel-a"}, 1, false, "merged_by_type", "tg", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Total != 201 {
		t.Fatalf("total = %d, want 201", response.Total)
	}
	if response.Completion != model.SearchCompletionComplete {
		t.Fatalf("completion = %q", response.Completion)
	}
	if len(store.queries) != 2 || store.queries[0].Page != 1 || store.queries[1].Page != 2 {
		t.Fatalf("database queries = %#v", store.queries)
	}
	if store.queries[0].SkipTotal || !store.queries[1].SkipTotal {
		t.Fatalf("SkipTotal by page = %v/%v, want false/true", store.queries[0].SkipTotal, store.queries[1].SkipTotal)
	}
	if store.queries[0].TitleQuery != "sample" {
		t.Fatalf("title query = %q", store.queries[0].TitleQuery)
	}
}

func TestHybridSearchResolvesSourcesBeforeDatabaseLookup(t *testing.T) {
	now := time.Now()
	store := &fakeResourceStore{pages: []storage.ResourcePage{{
		Total: 1,
		Items: []storage.Resource{{
			ID: 1, URL: "https://pan.quark.cn/s/example", NormalizedURL: "https://pan.quark.cn/s/example",
			Platform: "quark", Title: "stored", CheckStatus: storage.CheckValid, LastSeenAt: now,
			Sources: []storage.ResourceSource{{SourceType: "tg", SourceKey: "resolved_channel"}},
		}},
	}}}
	live := &resolvingLiveSearch{
		fakeLiveSearch: fakeLiveSearch{response: sampleLiveResponse()},
		channels:       []string{"resolved_channel"},
	}
	hybrid := NewHybridSearchService(live, store, time.Hour)

	_, err := hybrid.Search("sample", nil, 1, false, "all", "tg", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.queries) != 1 || !reflect.DeepEqual(store.queries[0].SourceKeys, []string{"resolved_channel"}) {
		t.Fatalf("database source keys = %v", store.queries)
	}
}

func TestHybridSearchMissCallsLiveAndPersists(t *testing.T) {
	store := &fakeResourceStore{pages: []storage.ResourcePage{{}}}
	live := &fakeLiveSearch{response: sampleLiveResponse()}
	hybrid := NewHybridSearchService(live, store, time.Hour)

	_, err := hybrid.Search("sample", nil, 1, false, "all", "tg", nil, nil, nil)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if live.calls != 1 || live.forced[0] {
		t.Fatalf("live calls/force = %d/%v, want 1/false", live.calls, live.forced)
	}
	if store.upserts != 1 {
		t.Fatalf("upserts = %d, want 1", store.upserts)
	}
	if len(store.lastUpsert.Results) != 1 {
		t.Fatalf("persisted Results = %d, want full live response", len(store.lastUpsert.Results))
	}
}

func TestHybridSearchMissPersistsFullLiveResponseBeforeFormatting(t *testing.T) {
	store := &fakeResourceStore{pages: []storage.ResourcePage{{}}}
	live := &fakeLiveSearch{response: sampleLiveResponse()}
	hybrid := NewHybridSearchService(live, store, time.Hour)

	response, err := hybrid.Search("sample", nil, 1, false, "merged_by_type", "tg", nil, nil, nil)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(response.Results) != 0 || len(response.MergedByType["quark"]) != 1 {
		t.Fatalf("formatted response = %+v", response)
	}
	if store.upserts != 1 {
		t.Fatalf("upserts = %d, want 1", store.upserts)
	}
	if len(store.lastUpsert.Results) != 1 {
		t.Fatalf("persisted Results = %d, want full live response", len(store.lastUpsert.Results))
	}
}

func TestHybridSearchForceBypassesDatabase(t *testing.T) {
	store := &fakeResourceStore{}
	live := &fakeLiveSearch{response: sampleLiveResponse()}
	hybrid := NewHybridSearchService(live, store, time.Hour)

	_, err := hybrid.Search("sample", nil, 1, true, "all", "all", nil, nil, nil)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(store.queries) != 0 {
		t.Fatalf("database queries = %d, want 0", len(store.queries))
	}
	if live.calls != 1 || !live.forced[0] {
		t.Fatalf("live calls/force = %d/%v, want 1/true", live.calls, live.forced)
	}
}

func TestHybridSearchDatabaseFailureFallsBackToLive(t *testing.T) {
	store := &fakeResourceStore{err: errors.New("database unavailable")}
	live := &fakeLiveSearch{response: sampleLiveResponse()}
	hybrid := NewHybridSearchService(live, store, time.Hour)

	_, err := hybrid.Search("sample", []string{"channel-a"}, 1, false, "all", "tg", nil, nil, nil)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if live.calls != 1 {
		t.Fatalf("live calls = %d, want 1", live.calls)
	}
}

func TestHybridSearchFallbackKeepsRequestContextActive(t *testing.T) {
	for _, test := range []struct {
		name  string
		store *fakeResourceStore
	}{
		{name: "database miss", store: &fakeResourceStore{pages: []storage.ResourcePage{{}}}},
		{name: "database failure", store: &fakeResourceStore{err: errors.New("database unavailable")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			live := &contextAwareLiveSearch{fakeLiveSearch: fakeLiveSearch{response: sampleLiveResponse()}}
			hybrid := NewHybridSearchService(live, test.store, time.Hour)

			_, err := hybrid.SearchContext(context.Background(), ContextSearchRequest{
				Keyword: "sample", Channels: []string{"channel-a"}, ResultType: "all", SourceType: "tg",
			})
			if err != nil {
				t.Fatalf("SearchContext returned error: %v", err)
			}
			if live.contextErr != nil {
				t.Fatalf("live search received canceled context: %v", live.contextErr)
			}
		})
	}
}

func TestHybridSearchAllSourcesUsesIndependentFilters(t *testing.T) {
	store := &fakeResourceStore{pages: []storage.ResourcePage{{}, {}}}
	live := &fakeLiveSearch{response: sampleLiveResponse()}
	hybrid := NewHybridSearchService(live, store, time.Hour)

	_, _ = hybrid.Search("sample", []string{"tg-a"}, 1, false, "all", "all", []string{"plugin-a"}, nil, nil)
	if len(store.queries) != 2 {
		t.Fatalf("database queries = %d, want 2", len(store.queries))
	}
	if got := store.queries[0].SourceTypes; len(got) != 1 || got[0] != "tg" {
		t.Fatalf("first source filter = %v, want tg", got)
	}
	if got := store.queries[1].SourceTypes; len(got) != 1 || got[0] != "plugin" {
		t.Fatalf("second source filter = %v, want plugin", got)
	}
}

func TestHybridSearchSkipsEmptySourceBranches(t *testing.T) {
	store := &fakeResourceStore{pages: []storage.ResourcePage{{}}}
	live := &fakeLiveSearch{response: sampleLiveResponse()}
	hybrid := NewHybridSearchService(live, store, time.Hour)

	_, _ = hybrid.Search("sample", []string{}, 1, false, "all", "all", []string{"plugin-a"}, nil, nil)
	if len(store.queries) != 1 {
		t.Fatalf("database queries = %d, want plugin query only", len(store.queries))
	}
	if got := store.queries[0].SourceTypes; len(got) != 1 || got[0] != "plugin" {
		t.Fatalf("source types = %v, want plugin", got)
	}
}

func TestHybridSearchRecorderFailureFallsBackToDirectUpsert(t *testing.T) {
	store := &fakeResourceStore{pages: []storage.ResourcePage{{}}}
	live := &fakeLiveSearch{response: sampleLiveResponse()}
	hybrid := NewHybridSearchService(live, store, time.Hour)
	recorderCalls := 0
	hybrid.SetExternalResultRecorder(func(context.Context, string, model.SearchResponse) error {
		recorderCalls++
		return collection.ErrBatchRunning
	})

	response, err := hybrid.Search("sample", nil, 1, false, "all", "tg", nil, nil, nil)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if response.Total != 1 || recorderCalls != 1 {
		t.Fatalf("response total/recorder calls = %d/%d", response.Total, recorderCalls)
	}
	if store.upserts != 1 || len(store.lastUpsert.Results) != 1 {
		t.Fatalf("busy recorder fallback upserts/results = %d/%d", store.upserts, len(store.lastUpsert.Results))
	}
}
