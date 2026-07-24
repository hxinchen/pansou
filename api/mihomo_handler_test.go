package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"pansou/mihomo"
)

type fakeMihomoRuntime struct {
	overview          mihomo.Overview
	group             string
	name              string
	subscriptions     []mihomo.Subscription
	lastInput         mihomo.SubscriptionInput
	lastPatch         mihomo.SubscriptionPatch
	lastID            string
	subscriptionError error
}

func (f *fakeMihomoRuntime) Overview(context.Context, bool) (mihomo.Overview, error) {
	return f.overview, nil
}

func (f *fakeMihomoRuntime) Select(_ context.Context, group, name string) (mihomo.Overview, error) {
	f.group, f.name = group, name
	return f.overview, nil
}

func (f *fakeMihomoRuntime) TestLatency(_ context.Context, group string) (mihomo.LatencyTestResponse, error) {
	f.group = group
	return mihomo.LatencyTestResponse{Overview: f.overview, Summary: mihomo.LatencyTestSummary{Group: group, Total: 2, Succeeded: 1, Failed: 1}}, nil
}

func (f *fakeMihomoRuntime) ListSubscriptions(context.Context) ([]mihomo.Subscription, error) {
	return f.subscriptions, nil
}

func (f *fakeMihomoRuntime) CreateSubscription(_ context.Context, input mihomo.SubscriptionInput) (mihomo.Subscription, error) {
	f.lastInput = input
	if f.subscriptionError != nil {
		return mihomo.Subscription{}, f.subscriptionError
	}
	return mihomo.Subscription{ID: "new", Name: input.Name}, nil
}

func TestMihomoHandlerRejectsDuplicateSubscription(t *testing.T) {
	gin.SetMode(gin.TestMode)
	runtime := &fakeMihomoRuntime{subscriptionError: mihomo.ErrDuplicateSubscription}
	router := gin.New()
	NewMihomoHandler(runtime).Register(router.Group("/api/admin"))
	request := httptest.NewRequest(http.MethodPost, "/api/admin/mihomo/subscriptions", strings.NewReader(`{"name":"重复订阅","url":"https://example.com/sub"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("duplicate subscription status=%d body=%s", response.Code, response.Body.String())
	}
}

func (f *fakeMihomoRuntime) UpdateSubscription(_ context.Context, id string, patch mihomo.SubscriptionPatch) (mihomo.Subscription, error) {
	f.lastID, f.lastPatch = id, patch
	return mihomo.Subscription{ID: id}, nil
}

func (f *fakeMihomoRuntime) DeleteSubscription(_ context.Context, id string) error {
	f.lastID = id
	return nil
}

func (f *fakeMihomoRuntime) UpdateSubscriptionNow(_ context.Context, id string) (mihomo.Subscription, error) {
	f.lastID = id
	return mihomo.Subscription{ID: id, Updating: false}, nil
}

func TestMihomoHandlerRegistersOverviewAndSelection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	runtime := &fakeMihomoRuntime{overview: mihomo.Overview{Configured: true, Available: true}}
	router := gin.New()
	NewMihomoHandler(runtime).Register(router.Group("/api/admin"))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/admin/mihomo/overview", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("overview status = %d body=%s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/admin/mihomo/selection", strings.NewReader(`{"group":"良心云","name":"自动选择"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || runtime.group != "良心云" || runtime.name != "自动选择" {
		t.Fatalf("selection status=%d group=%q name=%q body=%s", response.Code, runtime.group, runtime.name, response.Body.String())
	}

	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/admin/mihomo/latency-test", strings.NewReader(`{"group":"良心云"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || runtime.group != "良心云" {
		t.Fatalf("latency test status=%d group=%q body=%s", response.Code, runtime.group, response.Body.String())
	}
}

func TestMihomoHandlerRegistersSubscriptionRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	runtime := &fakeMihomoRuntime{subscriptions: []mihomo.Subscription{{ID: "sub-1", Name: "机场一"}}}
	router := gin.New()
	NewMihomoHandler(runtime).Register(router.Group("/api/admin"))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/admin/mihomo/subscriptions", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "sub-1") {
		t.Fatalf("list subscriptions status=%d body=%s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/admin/mihomo/subscriptions", strings.NewReader(`{"name":"机场二","url":"https://example.com/sub","interval_seconds":3600,"fetch_via":"auto"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || runtime.lastInput.Name != "机场二" || runtime.lastInput.IntervalSeconds != 3600 || runtime.lastInput.FetchVia != "auto" {
		t.Fatalf("create subscription status=%d input=%+v body=%s", response.Code, runtime.lastInput, response.Body.String())
	}

	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPatch, "/api/admin/mihomo/subscriptions/sub-1", strings.NewReader(`{"name":"机场一更新","interval_seconds":1800,"fetch_via":"fallback"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || runtime.lastID != "sub-1" || runtime.lastPatch.Name == nil || *runtime.lastPatch.Name != "机场一更新" || runtime.lastPatch.FetchVia == nil || *runtime.lastPatch.FetchVia != "fallback" {
		t.Fatalf("update subscription status=%d id=%q patch=%+v body=%s", response.Code, runtime.lastID, runtime.lastPatch, response.Body.String())
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/admin/mihomo/subscriptions/sub-1/update", nil))
	if response.Code != http.StatusOK || runtime.lastID != "sub-1" {
		t.Fatalf("update-now status=%d id=%q body=%s", response.Code, runtime.lastID, response.Body.String())
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/admin/mihomo/subscriptions/sub-1", nil))
	if response.Code != http.StatusNoContent || runtime.lastID != "sub-1" {
		t.Fatalf("delete subscription status=%d id=%q body=%s", response.Code, runtime.lastID, response.Body.String())
	}
}
