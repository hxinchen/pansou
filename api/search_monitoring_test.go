package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	accountauth "pansou/auth"
	"pansou/config"
	"pansou/model"
	"pansou/plugin"
	"pansou/service"
	"pansou/usage"
)

type monitoringSearch struct {
	status service.SearchCacheStatus
}

func (s monitoringSearch) Search(string, []string, int, bool, string, string, []string, []string, map[string]interface{}) (model.SearchResponse, error) {
	return model.SearchResponse{}, nil
}

func (s monitoringSearch) SearchContext(ctx context.Context, _ service.ContextSearchRequest) (model.SearchResponse, error) {
	service.MarkSearchCacheStatus(ctx, s.status)
	return model.SearchResponse{}, nil
}

func (monitoringSearch) GetPluginManager() *plugin.PluginManager { return nil }

func TestSearchCacheStatusResponseHeader(t *testing.T) {
	previous := config.AppConfig
	config.AppConfig = testConfig(false)
	defer func() { config.AppConfig = previous }()

	for _, status := range []service.SearchCacheStatus{
		service.SearchCacheHit,
		service.SearchCacheMiss,
		service.SearchCacheRefresh,
		service.SearchCacheBypass,
		service.SearchCacheNotApplicable,
	} {
		t.Run(string(status), func(t *testing.T) {
			router := SetupRouter(monitoringSearch{status: status})
			request := httptest.NewRequest(http.MethodGet, "/api/search?kw=test", nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if got := response.Header().Get(searchCacheStatusHeader); got != string(status) {
				t.Fatalf("%s = %q, want %q", searchCacheStatusHeader, got, status)
			}
			if exposed := response.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(exposed, searchCacheStatusHeader) {
				t.Fatalf("exposed headers %q do not contain %s", exposed, searchCacheStatusHeader)
			}
		})
	}
}

func TestSearchMonitoringFreezesClientIP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	if err := router.SetTrustedProxies([]string{"172.18.0.0/16"}); err != nil {
		t.Fatal(err)
	}
	router.GET("/test", SearchAccessMiddleware(), func(c *gin.Context) {
		c.Request.Header.Set("X-Forwarded-For", "198.51.100.99")
		c.String(http.StatusOK, c.GetString(usageSourceIPContextKey))
	})
	request := httptest.NewRequest(http.MethodGet, "/test", nil)
	request.RemoteAddr = "172.18.0.4:4567"
	request.Header.Set("X-Forwarded-For", "2001:db8::25")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if got := strings.TrimSpace(response.Body.String()); got != "2001:db8::25" {
		t.Fatalf("frozen source IP = %q, want original forwarded IPv6", got)
	}
}

func TestNormalizeUsageSourceIP(t *testing.T) {
	for input, want := range map[string]string{
		"127.0.0.1":     "internal",
		"::1":           "internal",
		"203.0.113.25":  "203.0.113.25",
		"2001:db8::123": "2001:db8::123",
	} {
		if got := normalizeUsageSourceIP(input); got != want {
			t.Errorf("normalizeUsageSourceIP(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSearchUsageRecorderReceivesTraceAndFrozenIP(t *testing.T) {
	var mu sync.Mutex
	var recorded []usage.UsageEvent
	recorder := usage.NewRecorder(usage.UsageRepositoryFunc(func(_ context.Context, events []usage.UsageEvent) error {
		mu.Lock()
		recorded = append(recorded, events...)
		mu.Unlock()
		return nil
	}), usage.RecorderConfig{QueueSize: 10, BatchSize: 10, FlushInterval: time.Hour, WriteTimeout: time.Second})
	if err := recorder.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	previousLimiter, previousRecorder := searchLimiter, usageRecorder
	SetUsageServices(nil, recorder)
	defer SetUsageServices(previousLimiter, previousRecorder)

	router := gin.New()
	router.Use(func(c *gin.Context) {
		setPrincipal(c, accountauth.Principal{UserID: 7, Username: "tester", Role: "user"}, "web")
		c.Next()
	})
	router.Use(SearchAccessMiddleware())
	router.GET("/api/search", func(c *gin.Context) {
		service.MarkSearchCacheStatus(c.Request.Context(), service.SearchCacheHit)
		c.Set(usageKeywordContextKey, "sample")
		c.Set(usageResultCountContextKey, 3)
		finalizeSearchCacheStatus(c)
		c.String(http.StatusOK, "ok")
	})
	request := httptest.NewRequest(http.MethodGet, "/api/search", nil)
	request.RemoteAddr = "127.0.0.1:4567"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := recorder.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(recorded) != 1 {
		t.Fatalf("recorded events = %d, want 1", len(recorded))
	}
	metadata := recorded[0].Metadata
	if got := metadata["cache_status"]; got != "hit" {
		t.Fatalf("cache_status = %#v, want hit", got)
	}
	if got := metadata["source_ip"]; got != "internal" {
		t.Fatalf("source_ip = %#v, want internal", got)
	}
	if got := metadata["result_count"]; got != 3 {
		t.Fatalf("result_count = %#v, want 3", got)
	}
}
