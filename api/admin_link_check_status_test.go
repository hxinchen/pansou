package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"pansou/collection"
	"pansou/storage"
)

type fakeLinkCheckRuntime struct {
	snapshot               collection.LinkCheckQueueSnapshot
	observedCount          int64
	observedAt             time.Time
	observedPolicyRevision string
}

func (f *fakeLinkCheckRuntime) ObserveBacklog(count int64, at time.Time, policyRevision string) {
	f.observedCount = count
	f.observedAt = at
	f.observedPolicyRevision = policyRevision
}

func (f *fakeLinkCheckRuntime) Snapshot() collection.LinkCheckQueueSnapshot {
	return f.snapshot
}

func TestAdminLinkCheckStatusReturnsServiceUnavailableWithoutStore(t *testing.T) {
	handler := NewAdminHandler(nil, nil)
	handler.linkChecks = &fakeLinkCheckRuntime{}
	router := adminLinkCheckPolicyTestRouter(handler)
	request := httptest.NewRequest(http.MethodGet, "/api/admin/link-check-status", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", response.Code, response.Body.String())
	}
}

func TestAdminLinkCheckStatusReturnsExactBacklogAndRuntimeMetrics(t *testing.T) {
	store := newAdminLinkCheckPolicyTestStore(t)
	at := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	if _, err := store.UpsertResource(context.Background(), storage.ResourceInput{
		URL: "https://status.example/pending", DiscoveredAt: at.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("insert pending resource: %v", err)
	}
	netDrain := 8.5
	etaSeconds := int64(420)
	runtime := &fakeLinkCheckRuntime{snapshot: collection.LinkCheckQueueSnapshot{
		Started: true, Workers: 4, Queued: 3, Active: 2, DueCount: 99, DueCountKnown: true,
		CompletedLastFiveMinutes: 50, FailedLastFiveMinutes: 4, ThroughputPerMinute: 10.5,
		NetDrainPerMinute: &netDrain, ETASeconds: &etaSeconds, ETAState: collection.LinkCheckETAAvailable,
		MetricsWindow: 5 * time.Minute, MetricsSampleWindow: 5 * time.Minute, BacklogSampleWindow: 2 * time.Minute,
	}}
	handler := NewAdminHandler(store, nil)
	handler.linkChecks = runtime
	handler.now = func() time.Time { return at }
	router := adminLinkCheckPolicyTestRouter(handler)

	request := httptest.NewRequest(http.MethodGet, "/api/admin/link-check-status", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Code int                          `json:"code"`
		Data adminLinkCheckStatusResponse `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if payload.Code != 0 {
		t.Fatalf("response code = %d; body=%s", payload.Code, response.Body.String())
	}
	data := payload.Data
	if data.DueCount != 1 {
		t.Fatalf("due count = %d, want exact database count 1", data.DueCount)
	}
	if data.QueuedCount != 3 || data.ActiveCount != 2 || data.WorkerCount != 4 {
		t.Fatalf("queue metrics = %+v", data)
	}
	if data.CompletedLastFiveMinutes != 50 || data.FailedLastFiveMinutes != 4 || data.ThroughputPerMinute != 10.5 {
		t.Fatalf("recent metrics = %+v", data)
	}
	if data.NetDrainPerMinute == nil || *data.NetDrainPerMinute != netDrain || data.ETASeconds == nil || *data.ETASeconds != etaSeconds || data.ETAState != collection.LinkCheckETAAvailable {
		t.Fatalf("ETA metrics = %+v", data)
	}
	if data.MetricsWindowSeconds != 300 || data.MetricsSampleWindowSeconds != 300 || data.BacklogSampleWindowSeconds != 120 || !data.ObservedAt.Equal(at) {
		t.Fatalf("sample metadata = %+v", data)
	}
	policy, err := store.GetLinkCheckPolicy(context.Background())
	if err != nil {
		t.Fatalf("get policy revision: %v", err)
	}
	if runtime.observedCount != 1 || !runtime.observedAt.Equal(at) || runtime.observedPolicyRevision != policy.Revision() {
		t.Fatalf("observed backlog = %d at %v policy %q", runtime.observedCount, runtime.observedAt, runtime.observedPolicyRevision)
	}
}

func TestAdminLinkCheckStatusRequiresRuntime(t *testing.T) {
	store := newAdminLinkCheckPolicyTestStore(t)
	router := adminLinkCheckPolicyTestRouter(NewAdminHandler(store, nil))
	request := httptest.NewRequest(http.MethodGet, "/api/admin/link-check-status", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", response.Code, response.Body.String())
	}
}
