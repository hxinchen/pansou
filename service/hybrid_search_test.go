package service

import (
	"context"
	"errors"
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

	_, err := hybrid.Search("sample", nil, 1, false, "all", "all", nil, nil, nil)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if live.calls != 1 {
		t.Fatalf("live calls = %d, want 1", live.calls)
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
