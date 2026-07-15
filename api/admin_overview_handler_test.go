package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"pansou/storage"
)

type adminOverviewAPIPayload struct {
	Code int `json:"code"`
	Data struct {
		ResourceCount    int64                                      `json:"resource_count"`
		SourceTypeTotals map[string]storage.SourceContributionTotal `json:"source_type_totals"`
		TopSourcesByType map[string][]storage.SourceContribution    `json:"top_sources_by_type"`
		ActiveRun        *storage.CollectionRun                     `json:"active_run"`
		RecentRuns       []storage.CollectionRun                    `json:"recent_runs"`
		Trends           []storage.TrendPoint                       `json:"trends"`
		GeneratedAt      time.Time                                  `json:"generated_at"`
		Stale            bool                                       `json:"stale"`
		Refreshing       bool                                       `json:"refreshing"`
	} `json:"data"`
}

func adminOverviewTestRouter(handler *AdminHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/overview", handler.overview)
	router.GET("/trends", handler.trends)
	return router
}

func performAdminOverviewRequest(t *testing.T, router http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestAdminOverviewAPIIncludesTrendsMetadataAndSupportsForce(t *testing.T) {
	clock := &adminOverviewTestClock{now: time.Date(2026, 7, 13, 8, 30, 0, 0, time.UTC)}
	var resourceCount atomic.Int64
	resourceCount.Store(10)
	store := &fakeAdminOverviewStore{
		snapshotFn: func(context.Context) (storage.OverviewStats, error) {
			return storage.OverviewStats{
				ResourceCount: resourceCount.Load(),
				SourceTypeTotals: map[string]storage.SourceContributionTotal{
					"plugin": {SourceType: "plugin", ResourceCount: 8, DiscoveryCount: 12},
					"tg":     {SourceType: "tg", ResourceCount: 5, DiscoveryCount: 7},
				},
				TopSourcesByType: map[string][]storage.SourceContribution{
					"plugin": {{SourceType: "plugin", SourceKey: "xdyh", ResourceCount: 8, DiscoveryCount: 12}},
					"tg":     {},
				},
			}, nil
		},
		activityFn: func(context.Context) (*storage.CollectionRun, []storage.CollectionRun, error) {
			return &storage.CollectionRun{ID: 3, Status: "running"}, []storage.CollectionRun{{ID: 2}}, nil
		},
		trendsFn: func(_ context.Context, days int) ([]storage.TrendPoint, error) {
			return []storage.TrendPoint{{NewCount: int64(days)}}, nil
		},
	}
	cache := newTestAdminOverviewCache(store, clock)
	handler := &AdminHandler{store: &storage.Store{}, overviewCache: cache}
	router := adminOverviewTestRouter(handler)

	firstResponse := performAdminOverviewRequest(t, router, "/overview?days=7")
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("first status = %d, body=%s", firstResponse.Code, firstResponse.Body.String())
	}
	var first adminOverviewAPIPayload
	if err := json.Unmarshal(firstResponse.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if first.Code != 0 || first.Data.ResourceCount != 10 || len(first.Data.Trends) != 1 || first.Data.Trends[0].NewCount != 7 {
		t.Fatalf("unexpected first response: %+v", first)
	}
	if first.Data.SourceTypeTotals["plugin"].ResourceCount != 8 ||
		len(first.Data.TopSourcesByType["plugin"]) != 1 || first.Data.TopSourcesByType["plugin"][0].SourceKey != "xdyh" {
		t.Fatalf("unexpected source contribution response: %+v", first.Data)
	}
	if first.Data.GeneratedAt != clock.Time() || first.Data.Stale || first.Data.Refreshing {
		t.Fatalf("unexpected cache metadata: %+v", first.Data)
	}
	if first.Data.ActiveRun == nil || first.Data.ActiveRun.ID != 3 || len(first.Data.RecentRuns) != 1 {
		t.Fatalf("unexpected activity: %+v", first.Data)
	}

	resourceCount.Store(20)
	cachedResponse := performAdminOverviewRequest(t, router, "/overview?days=7")
	var cached adminOverviewAPIPayload
	if err := json.Unmarshal(cachedResponse.Body.Bytes(), &cached); err != nil {
		t.Fatalf("decode cached response: %v", err)
	}
	if cached.Data.ResourceCount != 10 {
		t.Fatalf("cached resource count = %d, want 10", cached.Data.ResourceCount)
	}

	forcedResponse := performAdminOverviewRequest(t, router, "/overview?days=7&force=1")
	if forcedResponse.Code != http.StatusOK {
		t.Fatalf("forced status = %d, body=%s", forcedResponse.Code, forcedResponse.Body.String())
	}
	var forced adminOverviewAPIPayload
	if err := json.Unmarshal(forcedResponse.Body.Bytes(), &forced); err != nil {
		t.Fatalf("decode forced response: %v", err)
	}
	if forced.Data.ResourceCount != 20 {
		t.Fatalf("forced resource count = %d, want 20", forced.Data.ResourceCount)
	}
	if got := store.snapshotCalls.Load(); got != 2 {
		t.Fatalf("snapshot calls = %d, want 2", got)
	}

	trendsResponse := performAdminOverviewRequest(t, router, "/trends?days=7")
	if trendsResponse.Code != http.StatusOK {
		t.Fatalf("trends status = %d, body=%s", trendsResponse.Code, trendsResponse.Body.String())
	}
	var trendsPayload struct {
		Code int                  `json:"code"`
		Data []storage.TrendPoint `json:"data"`
	}
	if err := json.Unmarshal(trendsResponse.Body.Bytes(), &trendsPayload); err != nil {
		t.Fatalf("decode trends response: %v", err)
	}
	if trendsPayload.Code != 0 || len(trendsPayload.Data) != 1 || trendsPayload.Data[0].NewCount != 7 {
		t.Fatalf("legacy trends response changed: %+v", trendsPayload)
	}
	if got := store.snapshotCalls.Load(); got != 2 {
		t.Fatalf("legacy trends did not reuse cache; snapshot calls = %d", got)
	}
}

func TestAdminOverviewAPINormalizesTrendDayBounds(t *testing.T) {
	clock := &adminOverviewTestClock{now: time.Date(2026, 7, 13, 8, 30, 0, 0, time.UTC)}
	var (
		mu   sync.Mutex
		days []int
	)
	store := &fakeAdminOverviewStore{
		trendsFn: func(_ context.Context, value int) ([]storage.TrendPoint, error) {
			mu.Lock()
			days = append(days, value)
			mu.Unlock()
			return []storage.TrendPoint{}, nil
		},
	}
	cache := newTestAdminOverviewCache(store, clock)
	handler := &AdminHandler{store: &storage.Store{}, overviewCache: cache}
	router := adminOverviewTestRouter(handler)

	for _, path := range []string{"/trends?days=0", "/trends?days=367"} {
		response := performAdminOverviewRequest(t, router, path)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body=%s", path, response.Code, response.Body.String())
		}
	}
	mu.Lock()
	got := append([]int(nil), days...)
	mu.Unlock()
	if len(got) != 2 || got[0] != 1 || got[1] != 366 {
		t.Fatalf("trend days = %v, want [1 366]", got)
	}
}
