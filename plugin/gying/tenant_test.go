package gying

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"pansou/credential"
	"pansou/model"
	"pansou/plugin"
	"pansou/storage"
)

func TestSearchCredentialLayerFallsBackAfterHealthyZero(t *testing.T) {
	p := &GyingPlugin{BaseAsyncPlugin: plugin.NewBaseAsyncPluginWithFilter("gying", 3, true)}
	keyword := "测试电影"
	ext := map[string]interface{}{"credential_cache_scope": "user:42"}
	candidates := []storage.PluginCredential{{PublicID: "first"}, {PublicID: "second"}}
	now := time.Now()
	p.managedSearchCache.Store(p.managedSearchKey("user:42", "first", keyword), &managedSearchCacheEntry{
		outcome: gyingSearchOutcome{Complete: true}, freshUntil: now.Add(time.Minute), staleUntil: now.Add(time.Hour),
	})
	want := model.SearchResult{UniqueID: "gying-mv-1", Title: keyword, Links: []model.Link{{Type: "quark", URL: "https://pan.quark.cn/s/example"}}}
	p.managedSearchCache.Store(p.managedSearchKey("user:42", "second", keyword), &managedSearchCacheEntry{
		outcome: gyingSearchOutcome{Results: []model.SearchResult{want}, Complete: true}, freshUntil: now.Add(time.Minute), staleUntil: now.Add(time.Hour),
	})

	secret, err := json.Marshal(tenantSecret{Username: "user", Password: "pass", Cookie: "cookie"})
	if err != nil {
		t.Fatal(err)
	}
	var successes []string
	results, succeeded, err := p.SearchCredentialLayer(context.Background(), keyword, ext, candidates, credential.Access{
		Open:    func(storage.PluginCredential) ([]byte, error) { return append([]byte(nil), secret...), nil },
		Success: func(_ context.Context, publicID string) { successes = append(successes, publicID) },
	})
	if err != nil || !succeeded {
		t.Fatalf("SearchCredentialLayer() succeeded=%v err=%v", succeeded, err)
	}
	if !reflect.DeepEqual(results, []model.SearchResult{want}) {
		t.Fatalf("results = %#v", results)
	}
	if !reflect.DeepEqual(successes, []string{"first", "second"}) {
		t.Fatalf("success callbacks = %#v", successes)
	}
}

func TestSearchCredentialLayerReportsHealthyEmptyForNextLayer(t *testing.T) {
	p := &GyingPlugin{BaseAsyncPlugin: plugin.NewBaseAsyncPluginWithFilter("gying", 3, true)}
	keyword := "不存在的作品"
	scope := "user:7"
	candidate := storage.PluginCredential{PublicID: "empty"}
	now := time.Now()
	p.managedSearchCache.Store(p.managedSearchKey(scope, candidate.PublicID, keyword), &managedSearchCacheEntry{
		outcome: gyingSearchOutcome{Complete: true}, freshUntil: now.Add(time.Minute), staleUntil: now.Add(time.Hour),
	})
	secret, _ := json.Marshal(tenantSecret{Username: "user", Password: "pass", Cookie: "cookie"})
	results, succeeded, err := p.SearchCredentialLayer(context.Background(), keyword, map[string]interface{}{"credential_cache_scope": scope}, []storage.PluginCredential{candidate}, credential.Access{
		Open: func(storage.PluginCredential) ([]byte, error) { return append([]byte(nil), secret...), nil },
	})
	if len(results) != 0 || succeeded || !errors.Is(err, credential.ErrNoResults) {
		t.Fatalf("results=%d succeeded=%v err=%v", len(results), succeeded, err)
	}
}

func TestManagedSearchKeyIsIdentityScoped(t *testing.T) {
	p := &GyingPlugin{}
	left := p.managedSearchKey("user:1", "shared", " Movie ")
	right := p.managedSearchKey("user:2", "shared", "movie")
	if left == right {
		t.Fatal("managed search keys leaked across identities")
	}
	if got := p.managedSearchKey("user:1", "shared", "movie"); got != left {
		t.Fatalf("keyword normalization mismatch: %q != %q", got, left)
	}
}

func TestSortSearchSuggestionsPrefersExactAndRecent(t *testing.T) {
	p := &GyingPlugin{}
	items := []SearchSuggestItem{
		{Title: "测试电影 第二部", Year: 2020},
		{Title: "测试电影", Year: 2018},
		{Title: "测试电影", Year: 2024},
		{Title: "其他测试电影资料", Year: 2025},
	}
	p.sortSearchSuggestions(items, "测试电影")
	got := []int{items[0].Year, items[1].Year, items[2].Year, items[3].Year}
	if want := []int{2024, 2018, 2020, 2025}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sorted years = %#v, want %#v", got, want)
	}
}

func TestPartialErrorExposesSourceStatus(t *testing.T) {
	err := &gyingPartialError{stats: gyingSearchStats{Candidates: 10, Attempted: 8, Succeeded: 6, Failed: 2}, cause: context.DeadlineExceeded}
	status := err.SourceStatus()
	if status.Completion != model.SearchCompletionPartial || status.Candidates != 10 || status.Attempted != 8 || status.Succeeded != 6 || status.Failed != 2 {
		t.Fatalf("status = %#v", status)
	}
}
