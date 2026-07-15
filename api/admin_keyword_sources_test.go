package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"pansou/keywordsource"
	"pansou/storage"
)

func TestEncodeKeywordAPISourceBody(t *testing.T) {
	body, err := encodeKeywordAPISourceBody("json", map[string]any{"page": 2, "enabled": true})
	if err != nil {
		t.Fatalf("encode body: %v", err)
	}
	if !strings.Contains(body, `"page":2`) || !strings.Contains(body, `"enabled":true`) {
		t.Fatalf("unexpected body: %s", body)
	}

	raw, err := encodeKeywordAPISourceBody("raw", "token=value")
	if err != nil || raw != "token=value" {
		t.Fatalf("raw body = %q, err=%v", raw, err)
	}
}

func TestFlexibleStringMapAcceptsJSONScalars(t *testing.T) {
	var value flexibleStringMap
	if err := json.Unmarshal([]byte(`{"start":0,"limit":20,"enabled":true,"empty":null}`), &value); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if value["start"] != "0" || value["limit"] != "20" || value["enabled"] != "true" || value["empty"] != "" {
		t.Fatalf("unexpected scalar conversion: %#v", value)
	}
	if err := json.Unmarshal([]byte(`{"nested":{"value":1}}`), &value); err == nil {
		t.Fatal("expected nested object to be rejected")
	}
}

func TestRedactKeywordSourceListURL(t *testing.T) {
	redacted := redactKeywordSourceListURL("https://user:secret@example.com/list?token=abc&page=2")
	if strings.Contains(redacted, "secret") || strings.Contains(redacted, "abc") || strings.Contains(redacted, "page=2") {
		t.Fatalf("URL leaked a credential or query value: %s", redacted)
	}
	if !strings.Contains(redacted, "user") || !strings.Contains(redacted, "%5BREDACTED%5D") {
		t.Fatalf("URL was not usefully redacted: %s", redacted)
	}
}

func TestKeywordAPISourceIterationConfigIncludesUnlimitedAndRandomDelay(t *testing.T) {
	var request keywordAPISourceRequest
	if err := json.Unmarshal([]byte(`{
		"iteration_enabled": true,
		"iteration_location": "query",
		"iteration_path": "page",
		"iteration_start": 1,
		"iteration_step": 2,
		"iteration_count": 10,
		"iteration_delay_seconds": 3,
		"iteration_unlimited": true,
		"iteration_no_keyword_stop_count": 4,
		"iteration_stop_mode": "strict",
		"iteration_random_delay_min_seconds": -2,
		"iteration_random_delay_max_seconds": 5
	}`), &request); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	config := keywordAPISourceIterationConfig(request)
	if !config.Enabled || config.Path != "page" || config.Start != 1 || config.Step != 2 ||
		config.Count != 10 || config.DelaySeconds != 3 || !config.Unlimited ||
		config.NoKeywordStopCount != 4 || config.StopMode != keywordsource.IterationStopModeStrict || config.RandomDelayMinSeconds != -2 ||
		config.RandomDelayMaxSeconds != 5 {
		t.Fatalf("iteration config = %+v", config)
	}
	derived, value, err := keywordsource.DeriveRequest(keywordsource.RequestConfig{}, config, 0)
	if err != nil || value != 1 || derived.Query["page"] != "1" {
		t.Fatalf("first test request = value:%d query:%#v err:%v", value, derived.Query, err)
	}
}

func TestBindKeywordAPISourceDefaultsStopModeToStrict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"source"}`))
	context.Request.Header.Set("Content-Type", "application/json")
	request, _, ok := bindKeywordAPISource(context)
	if !ok || request.IterationStopMode != storage.KeywordAPIIterationStopModeStrict {
		t.Fatalf("request = %+v ok=%v response=%s", request, ok, recorder.Body.String())
	}
}

func TestValidateKeywordAPISourceRequestChecksUnlimitedAndRandomDelay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := keywordAPISourceRequest{
		IterationEnabled: true, IterationLocation: "query", IterationPath: "page",
		IterationCount: 1, IterationUnlimited: true, IterationNoKeywordStopCount: 1,
	}
	tests := []struct {
		name   string
		mutate func(*keywordAPISourceRequest)
	}{
		{name: "unlimited requires stop count", mutate: func(request *keywordAPISourceRequest) {
			request.IterationNoKeywordStopCount = 0
		}},
		{name: "random delay bounds", mutate: func(request *keywordAPISourceRequest) {
			request.IterationRandomDelayMinSeconds = -3601
		}},
		{name: "random delay order", mutate: func(request *keywordAPISourceRequest) {
			request.IterationRandomDelayMinSeconds = 2
			request.IterationRandomDelayMaxSeconds = 1
		}},
		{name: "invalid stop mode", mutate: func(request *keywordAPISourceRequest) {
			request.IterationStopMode = "aggressive"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := base
			test.mutate(&request)
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			if validateKeywordAPISourceRequest(context, request, "", false) {
				t.Fatal("invalid iteration config was accepted")
			}
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
			}
		})
	}

	validRecorder := httptest.NewRecorder()
	validContext, _ := gin.CreateTestContext(validRecorder)
	base.IterationRandomDelayMinSeconds = -2
	base.IterationRandomDelayMaxSeconds = 3
	if !validateKeywordAPISourceRequest(validContext, base, "", false) {
		t.Fatalf("valid unlimited iteration rejected: %s", validRecorder.Body.String())
	}
}

func TestValidateKeywordAPISourceRequestRejectsStopModeWhenIterationDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	if validateKeywordAPISourceRequest(context, keywordAPISourceRequest{IterationStopMode: "aggressive"}, "", false) {
		t.Fatal("invalid stop mode was accepted while iteration was disabled")
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestKeywordAPISourceDetailIncludesUnlimitedAndRandomDelay(t *testing.T) {
	detail := keywordAPISourceDetail(storage.KeywordAPISource{
		IterationUnlimited: true, IterationNoKeywordStopCount: 5,
		IterationStopMode:              storage.KeywordAPIIterationStopModeStrict,
		IterationRandomDelayMinSeconds: -4, IterationRandomDelayMaxSeconds: 6,
		SyncConfigRevision: 8, LastAppliedConfigRevision: 7, ResultStale: true,
	})
	if detail["iteration_unlimited"] != true || detail["iteration_no_keyword_stop_count"] != 5 ||
		detail["iteration_stop_mode"] != storage.KeywordAPIIterationStopModeStrict ||
		detail["iteration_random_delay_min_seconds"] != -4 ||
		detail["iteration_random_delay_max_seconds"] != 6 {
		t.Fatalf("detail = %#v", detail)
	}
	if detail["sync_config_revision"] != int64(8) || detail["last_applied_config_revision"] != int64(7) ||
		detail["result_stale"] != true {
		t.Fatalf("detail run state = %#v", detail)
	}
}

func TestKeywordAPISourceListItemIncludesRunState(t *testing.T) {
	run := &storage.KeywordAPISyncRun{}
	item := keywordAPISourceListItemFrom(storage.KeywordAPISource{
		ID: 9, RequestURL: "https://example.com/items?token=secret",
		SyncConfigRevision: 3, LastAppliedConfigRevision: 2, ResultStale: true,
		ActiveRun: run, LatestRun: run,
	})
	if item.SyncConfigRevision != 3 || item.LastAppliedConfigRevision != 2 || !item.ResultStale {
		t.Fatalf("list item run state = %#v", item)
	}
	if item.ActiveRun != run || item.LatestRun != run {
		t.Fatalf("list item runs = %#v", item)
	}
	if strings.Contains(item.RequestURL, "secret") {
		t.Fatalf("list URL leaked query value: %s", item.RequestURL)
	}
}

func TestRegisterKeywordAPISourceRoutesIncludesSyncRunHistory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	(&AdminHandler{}).registerKeywordAPISourceRoutes(router.Group("/api/admin"))
	routes := make(map[string]bool)
	for _, route := range router.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	for _, route := range []string{
		"GET /api/admin/keyword-api-sync-runs",
		"GET /api/admin/keyword-api-sync-runs/:id",
		"GET /api/admin/keyword-api-sync-runs/:id/iterations",
		"POST /api/admin/keyword-api-sources/:id/sync",
	} {
		if !routes[route] {
			t.Fatalf("route %q is not registered: %#v", route, routes)
		}
	}
}

func TestKeywordAPITestPreviewIsBounded(t *testing.T) {
	candidates := make([]keywordsource.FieldCandidate, 125)
	for index := range candidates {
		candidates[index] = keywordsource.FieldCandidate{
			Path: strings.Repeat("p", 400), Samples: []string{
				strings.Repeat("s", 300), strings.Repeat("t", 300), strings.Repeat("u", 300),
			},
		}
	}
	preview, total := keywordAPITestCandidates(candidates)
	if total != 125 || len(preview) != 100 || len([]rune(preview[0].Path)) > 303 || len([]rune(preview[0].Samples[0])) > 163 {
		t.Fatalf("candidate preview total=%d len=%d first=%#v", total, len(preview), preview[0])
	}
	values := make([]keywordsource.KeywordValue, 30)
	for index := range values {
		values[index] = keywordsource.KeywordValue{Value: strings.Repeat("v", 300), Normalized: strings.Repeat("n", 300)}
	}
	extraction := keywordAPITestExtraction(&keywordsource.ExtractionResult{RawCount: 40, UniqueCount: 30, Values: values})
	if extraction.RawCount != 40 || extraction.UniqueCount != 30 || len(extraction.Values) != 20 ||
		len([]rune(extraction.Values[0].Value)) > 203 {
		t.Fatalf("extraction preview = %#v", extraction)
	}
	encoded, err := json.Marshal(gin.H{"candidates": preview, "extraction": extraction})
	if err != nil {
		t.Fatalf("marshal API test preview: %v", err)
	}
	if len(encoded) >= 256*1024 {
		t.Fatalf("API test preview = %d bytes, want < 256 KiB", len(encoded))
	}
}

func TestKeywordAPISyncRunFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet,
		"/?source_id=7&status=running,partial&trigger=manual&from=2026-07-01&to=2026-07-12&page=2&page_size=999", nil)
	filter, err := keywordAPISyncRunFilter(context)
	if err != nil {
		t.Fatalf("keywordAPISyncRunFilter: %v", err)
	}
	if filter.SourceID == nil || *filter.SourceID != 7 || filter.Page != 2 || filter.PageSize != 200 {
		t.Fatalf("filter identity/page = %#v", filter)
	}
	if len(filter.Statuses) != 2 || filter.Statuses[0] != "running" || filter.Statuses[1] != "partial" ||
		len(filter.Triggers) != 1 || filter.Triggers[0] != "manual" {
		t.Fatalf("filter values = %#v", filter)
	}
	if filter.From == nil || filter.To == nil || !filter.To.After(*filter.From) {
		t.Fatalf("filter times = %#v", filter)
	}
}

func TestKeywordAPISyncRunFilterRejectsInvalidBounds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, target := range []string{
		"/?source_id=zero",
		"/?from=2026-07-13&to=2026-07-12",
		"/?from=not-a-time",
	} {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Request = httptest.NewRequest(http.MethodGet, target, nil)
		if _, err := keywordAPISyncRunFilter(context); err == nil {
			t.Fatalf("target %q should be rejected", target)
		}
	}
}

func TestBindKeywordAPISyncTrigger(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		body       string
		want       string
		wantOK     bool
		wantStatus int
	}{
		{name: "empty defaults to manual", want: "manual", wantOK: true},
		{name: "save is accepted", body: `{"trigger":"SAVE"}`, want: "save", wantOK: true},
		{name: "unknown is rejected", body: `{"trigger":"scheduled"}`, wantOK: false, wantStatus: http.StatusBadRequest},
		{name: "invalid json is rejected", body: `{`, wantOK: false, wantStatus: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
			context.Request.Header.Set("Content-Type", "application/json")
			got, ok := bindKeywordAPISyncTrigger(context)
			if got != test.want || ok != test.wantOK {
				t.Fatalf("trigger = %q, ok=%v; want %q, %v", got, ok, test.want, test.wantOK)
			}
			if test.wantStatus != 0 && recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, test.wantStatus)
			}
		})
	}
}

func TestKeywordAPISyncAcceptedResponsePreservesCompatibility(t *testing.T) {
	run := storage.KeywordAPISyncRun{ID: 42, Status: "queued"}
	response := keywordAPISyncAcceptedResponse(7, run, true)
	if response["id"] != int64(7) || response["status"] != storage.KeywordAPISourceStatusRunning ||
		response["run_status"] != "queued" || response["accepted"] != true {
		t.Fatalf("legacy response fields = %#v", response)
	}
	responseRun, ok := response["run"].(storage.KeywordAPISyncRun)
	if response["run_id"] != int64(42) || !ok || responseRun.ID != 42 || response["already_active"] != true {
		t.Fatalf("run response fields = %#v", response)
	}
}
