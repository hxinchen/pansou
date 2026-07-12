package keywordsync

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pansou/keywordsource"
	"pansou/storage"
)

type fakeKeywordSourceStore struct {
	completed          storage.KeywordAPISourceSyncInput
	completeContextErr error
	failedMessage      string
	failedRequestCount int
	failedFailureCount int
}

type fakeTrackedKeywordSourceStore struct {
	fakeKeywordSourceStore
	claim                 *storage.KeywordAPISyncClaim
	claimed               bool
	begun                 []storage.KeywordAPISyncIterationInput
	iterations            []storage.KeywordAPISyncIterationInput
	finalized             storage.KeywordAPISyncFinalizeInput
	finalizeErr           error
	failedRun             bool
	failedRunRequestCount int
	failedRunFailureCount int
	interrupted           bool
}

func (f *fakeTrackedKeywordSourceStore) EnqueueKeywordAPISourceSync(context.Context, int64, string, time.Time) (storage.KeywordAPISyncRun, bool, error) {
	return storage.KeywordAPISyncRun{}, false, nil
}

func (f *fakeTrackedKeywordSourceStore) EnqueueDueKeywordAPISourceSync(context.Context, time.Time) (*storage.KeywordAPISyncRun, error) {
	return nil, nil
}

func (f *fakeTrackedKeywordSourceStore) ClaimNextKeywordAPISyncRun(context.Context, string, string, time.Time, time.Time) (*storage.KeywordAPISyncClaim, error) {
	if f.claimed || f.claim == nil {
		return nil, nil
	}
	f.claimed = true
	return f.claim, nil
}

func (f *fakeTrackedKeywordSourceStore) RenewKeywordAPISyncRunLease(context.Context, int64, string, string, time.Time, time.Time) error {
	return nil
}

func (f *fakeTrackedKeywordSourceStore) BeginKeywordAPISyncIteration(_ context.Context, input storage.KeywordAPISyncIterationInput) (storage.KeywordAPISyncIteration, error) {
	f.begun = append(f.begun, input)
	return storage.KeywordAPISyncIteration{RunID: input.RunID, Sequence: input.Sequence, Status: input.Status}, nil
}

func (f *fakeTrackedKeywordSourceStore) CompleteKeywordAPISyncIteration(_ context.Context, input storage.KeywordAPISyncIterationInput) (storage.KeywordAPISyncIteration, error) {
	f.iterations = append(f.iterations, input)
	return storage.KeywordAPISyncIteration{RunID: input.RunID, Sequence: input.Sequence, Status: input.Status}, nil
}

func (f *fakeTrackedKeywordSourceStore) FinalizeKeywordAPISyncRun(_ context.Context, input storage.KeywordAPISyncFinalizeInput) (storage.KeywordAPISyncRun, storage.KeywordAPISourceSyncResult, error) {
	f.finalized = input
	if f.finalizeErr != nil {
		return storage.KeywordAPISyncRun{}, storage.KeywordAPISourceSyncResult{}, f.finalizeErr
	}
	return storage.KeywordAPISyncRun{ID: input.RunID, Status: input.Status}, storage.KeywordAPISourceSyncResult{}, nil
}

func (f *fakeTrackedKeywordSourceStore) FailKeywordAPISyncRun(_ context.Context, _ int64, _, _, _ string, _ time.Time, requestCount, failureCount int) (storage.KeywordAPISyncRun, error) {
	f.failedRun = true
	f.failedRunRequestCount = requestCount
	f.failedRunFailureCount = failureCount
	return storage.KeywordAPISyncRun{}, nil
}

func (f *fakeTrackedKeywordSourceStore) InterruptKeywordAPISyncRun(context.Context, int64, string, string, string, time.Time) (storage.KeywordAPISyncRun, error) {
	f.interrupted = true
	return storage.KeywordAPISyncRun{}, nil
}

func (f *fakeTrackedKeywordSourceStore) RecoverExpiredKeywordAPISyncRuns(context.Context, time.Time) (int, error) {
	return 0, nil
}

func (f *fakeKeywordSourceStore) ClaimDueKeywordAPISource(context.Context, time.Time) (*storage.KeywordAPISource, error) {
	return nil, nil
}
func (f *fakeKeywordSourceStore) ClaimKeywordAPISourceForSync(context.Context, int64, time.Time) (storage.KeywordAPISource, error) {
	return storage.KeywordAPISource{}, nil
}
func (f *fakeKeywordSourceStore) CompleteKeywordAPISourceSync(ctx context.Context, input storage.KeywordAPISourceSyncInput) (storage.KeywordAPISourceSyncResult, error) {
	f.completed = input
	f.completeContextErr = ctx.Err()
	return storage.KeywordAPISourceSyncResult{Source: storage.KeywordAPISource{LastStatus: input.Status}}, nil
}
func (f *fakeKeywordSourceStore) FailKeywordAPISourceSync(_ context.Context, _ int64, message string, _ time.Time) (storage.KeywordAPISource, error) {
	f.failedMessage = message
	return storage.KeywordAPISource{}, nil
}
func (f *fakeKeywordSourceStore) FailKeywordAPISourceSyncWithStats(_ context.Context, _ int64, message string, _ time.Time, requestCount, failureCount int) (storage.KeywordAPISource, error) {
	f.failedMessage = message
	f.failedRequestCount = requestCount
	f.failedFailureCount = failureCount
	return storage.KeywordAPISource{}, nil
}

func TestRequestConfigDecodesStoredFormBody(t *testing.T) {
	config, err := RequestConfig(storage.KeywordAPISource{
		RequestMethod: "POST", RequestURL: "https://example.com/keywords",
		BodyType: "form", RequestBody: `{"scope":"all","page":"2"}`, TimeoutSeconds: 15,
	})
	if err != nil {
		t.Fatalf("RequestConfig: %v", err)
	}
	if config.BodyType != keywordsource.BodyForm || config.Form["scope"] != "all" || config.Form["page"] != "2" {
		t.Fatalf("unexpected form config: %+v", config)
	}
}

func TestRequestConfigRejectsInvalidStoredForm(t *testing.T) {
	_, err := RequestConfig(storage.KeywordAPISource{
		RequestMethod: "POST", RequestURL: "https://example.com/keywords",
		BodyType: "form", RequestBody: `{bad`, TimeoutSeconds: 15,
	})
	if err == nil {
		t.Fatal("expected invalid stored form body error")
	}
}

func TestSyncClaimedCombinesIterationsAndRecordsPartialSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		start, _ := strconv.Atoi(request.URL.Query().Get("start"))
		if start == 40 {
			http.Error(w, `{"error":"temporary"}`, http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"items":[{"title":"shared"},{"title":"item-%d"}]}`, start)
	}))
	defer server.Close()

	store := &fakeKeywordSourceStore{}
	service := newService(store, Config{})
	result, err := service.syncClaimed(context.Background(), storage.KeywordAPISource{
		ID: 9, RequestMethod: http.MethodGet, RequestURL: server.URL, BodyType: "none", TimeoutSeconds: 5,
		ResponsePath: "items[].title", IterationEnabled: true, IterationLocation: "query",
		IterationPath: "start", IterationStart: 0, IterationStep: 20, IterationCount: 3,
	})
	if err != nil {
		t.Fatalf("syncClaimed: %v", err)
	}
	if result.Source.LastStatus != storage.KeywordAPISourceStatusPartial {
		t.Fatalf("status = %q", result.Source.LastStatus)
	}
	input := store.completed
	if input.RequestCount != 3 || input.SuccessCount != 2 || input.FailureCount != 1 {
		t.Fatalf("unexpected counts: %+v", input)
	}
	if len(input.Values) != 3 || input.Status != storage.KeywordAPISourceStatusPartial || input.ErrorMessage == "" {
		t.Fatalf("unexpected completion: %+v", input)
	}
}

func TestExecuteTrackedRunPersistsEveryIterationAndFinalTotals(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		start, _ := strconv.Atoi(request.URL.Query().Get("start"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"items":[{"title":"shared"},{"title":"item-%d"}]}`, start)
	}))
	defer server.Close()

	sourceID := int64(17)
	store := &fakeTrackedKeywordSourceStore{}
	store.claim = &storage.KeywordAPISyncClaim{
		Run: storage.KeywordAPISyncRun{ID: 81, SourceID: &sourceID, Status: storage.KeywordAPISyncRunStatusRunning},
		Source: storage.KeywordAPISource{
			ID: sourceID, RequestMethod: http.MethodGet, RequestURL: server.URL,
			BodyType: "none", TimeoutSeconds: 5, ResponsePath: "items[].title",
			IterationEnabled: true, IterationLocation: "query", IterationPath: "start",
			IterationStart: 0, IterationStep: 20, IterationCount: 3,
		},
	}
	service := newService(store, Config{})
	if err := service.executeTrackedRun(context.Background(), store, *store.claim, "lease-token"); err != nil {
		t.Fatalf("executeTrackedRun: %v", err)
	}
	if store.failedRun || store.interrupted {
		t.Fatalf("unexpected terminal path: failed=%v interrupted=%v", store.failedRun, store.interrupted)
	}
	if len(store.begun) != 3 || len(store.iterations) != 3 {
		t.Fatalf("iteration records: begun=%d completed=%d", len(store.begun), len(store.iterations))
	}
	if store.iterations[0].CrossIterationNew != 2 || store.iterations[1].CrossIterationNew != 1 || store.iterations[2].CrossIterationNew != 1 {
		t.Fatalf("cross-iteration counts = %d,%d,%d", store.iterations[0].CrossIterationNew, store.iterations[1].CrossIterationNew, store.iterations[2].CrossIterationNew)
	}
	if store.finalized.RequestCount != 3 || store.finalized.SuccessCount != 3 || store.finalized.FailureCount != 0 || store.finalized.RawItemCount != 6 {
		t.Fatalf("finalized counts = %+v", store.finalized)
	}
	if len(store.finalized.Values) != 4 {
		t.Fatalf("final unique values = %#v", store.finalized.Values)
	}
	if len(store.finalized.ValueSequences) != 4 || store.finalized.ValueSequences[0] != 1 ||
		store.finalized.ValueSequences[1] != 1 || store.finalized.ValueSequences[2] != 2 || store.finalized.ValueSequences[3] != 3 {
		t.Fatalf("final value sequences = %#v", store.finalized.ValueSequences)
	}
}

func TestExecuteTrackedRunRecordsFailureWhenFinalWriteFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"items":[{"title":"alpha"}]}`)
	}))
	defer server.Close()

	sourceID := int64(18)
	store := &fakeTrackedKeywordSourceStore{finalizeErr: errors.New("database write failed")}
	claim := storage.KeywordAPISyncClaim{
		Run: storage.KeywordAPISyncRun{ID: 82, SourceID: &sourceID, Status: storage.KeywordAPISyncRunStatusRunning},
		Source: storage.KeywordAPISource{
			ID: sourceID, RequestMethod: http.MethodGet, RequestURL: server.URL,
			BodyType: "none", TimeoutSeconds: 5, ResponsePath: "items[].title",
		},
	}
	service := newService(store, Config{})
	err := service.executeTrackedRun(context.Background(), store, claim, "lease-token")
	if err == nil || !strings.Contains(err.Error(), "database write failed") {
		t.Fatalf("executeTrackedRun error = %v", err)
	}
	if !store.failedRun || store.failedRunRequestCount != 1 || store.failedRunFailureCount != 0 {
		t.Fatalf("failed terminal record = %+v", store)
	}
}

func TestSyncClaimedStopsAfterConsecutiveEmptyIterationsAndResetsOnDuplicates(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		page, _ := strconv.Atoi(request.URL.Query().Get("page"))
		w.Header().Set("Content-Type", "application/json")
		switch page {
		case 1, 3:
			_, _ = fmt.Fprint(w, `{"items":[{"title":"shared"}]}`)
		case 0, 2, 4, 5:
			_, _ = fmt.Fprint(w, `{"items":[]}`)
		default:
			_, _ = fmt.Fprint(w, `{"items":[{"title":"too-late"}]}`)
		}
	}))
	defer server.Close()

	store := &fakeKeywordSourceStore{}
	service := newService(store, Config{})
	service.waitDelay = func(context.Context, time.Duration) error { return nil }
	_, err := service.syncClaimed(context.Background(), storage.KeywordAPISource{
		ID: 10, RequestMethod: http.MethodGet, RequestURL: server.URL, BodyType: "none", TimeoutSeconds: 5,
		ResponsePath: "items[].title", IterationEnabled: true, IterationLocation: "query",
		IterationPath: "page", IterationStart: 0, IterationStep: 1, IterationCount: 10,
		IterationNoKeywordStopCount: 2,
	})
	if err != nil {
		t.Fatalf("syncClaimed: %v", err)
	}
	input := store.completed
	if requests.Load() != 6 || input.RequestCount != 6 || input.SuccessCount != 6 || input.FailureCount != 0 {
		t.Fatalf("unexpected counts: requests=%d input=%+v", requests.Load(), input)
	}
	if len(input.Values) != 1 || input.Values[0] != "shared" || input.Status != storage.KeywordAPISourceStatusSuccess {
		t.Fatalf("unexpected completion: %+v", input)
	}
}

func TestSyncClaimedUnlimitedStopsAfterResponseFailures(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{name: "HTTP status", statusCode: http.StatusBadGateway, body: `{"error":"temporary"}`},
		{name: "invalid JSON", statusCode: http.StatusOK, body: `{`},
		{name: "extraction error", statusCode: http.StatusOK, body: `{"items":[{"title":{"nested":true}}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.statusCode)
				_, _ = fmt.Fprint(w, test.body)
			}))
			defer server.Close()

			store := &fakeKeywordSourceStore{}
			service := newService(store, Config{})
			service.waitDelay = func(context.Context, time.Duration) error { return nil }
			_, err := service.syncClaimed(context.Background(), storage.KeywordAPISource{
				ID: 11, RequestMethod: http.MethodGet, RequestURL: server.URL, BodyType: "none", TimeoutSeconds: 5,
				ResponsePath: "items[].title", IterationEnabled: true, IterationUnlimited: true,
				IterationLocation: "query", IterationPath: "page", IterationStep: 1, IterationCount: 1,
				IterationNoKeywordStopCount: 2,
			})
			if err == nil {
				t.Fatal("expected all-failed synchronization error")
			}
			if requests.Load() != 2 || store.failedRequestCount != 2 || store.failedFailureCount != 2 {
				t.Fatalf("unexpected failure counts: requests=%d store=%+v", requests.Load(), store)
			}
		})
	}
}

func TestSyncClaimedUnlimitedCountsDerivationErrorsTowardStop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"items":[{"title":"first"}]}`)
	}))
	defer server.Close()

	store := &fakeKeywordSourceStore{}
	service := newService(store, Config{})
	service.waitDelay = func(context.Context, time.Duration) error { return nil }
	_, err := service.syncClaimed(context.Background(), storage.KeywordAPISource{
		ID: 12, RequestMethod: http.MethodGet, RequestURL: server.URL, BodyType: "none", TimeoutSeconds: 5,
		ResponsePath: "items[].title", IterationEnabled: true, IterationUnlimited: true,
		IterationLocation: "query", IterationPath: "page", IterationStart: math.MaxInt64,
		IterationStep: 1, IterationCount: 1, IterationNoKeywordStopCount: 2,
	})
	if err != nil {
		t.Fatalf("syncClaimed: %v", err)
	}
	input := store.completed
	if input.RequestCount != 3 || input.SuccessCount != 1 || input.FailureCount != 2 || input.Status != storage.KeywordAPISourceStatusPartial {
		t.Fatalf("unexpected completion: %+v", input)
	}
	if len(input.Values) != 1 || input.Values[0] != "first" || !strings.Contains(input.ErrorMessage, "overflows int64") {
		t.Fatalf("unexpected values or error: %+v", input)
	}
}

func TestSyncClaimedPureEmptyUnlimitedRunIsSuccessful(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"items":[]}`)
	}))
	defer server.Close()

	store := &fakeKeywordSourceStore{}
	service := newService(store, Config{})
	service.waitDelay = func(context.Context, time.Duration) error { return nil }
	_, err := service.syncClaimed(context.Background(), storage.KeywordAPISource{
		ID: 13, RequestMethod: http.MethodGet, RequestURL: server.URL, BodyType: "none", TimeoutSeconds: 5,
		ResponsePath: "items[].title", IterationEnabled: true, IterationUnlimited: true,
		IterationLocation: "query", IterationPath: "page", IterationStep: 1, IterationCount: 1,
		IterationNoKeywordStopCount: 2,
	})
	if err != nil {
		t.Fatalf("syncClaimed: %v", err)
	}
	input := store.completed
	if requests.Load() != 2 || input.RequestCount != 2 || input.SuccessCount != 2 || input.FailureCount != 0 || input.Status != storage.KeywordAPISourceStatusSuccess || len(input.Values) != 0 {
		t.Fatalf("unexpected completion: requests=%d input=%+v", requests.Load(), input)
	}
}

func TestSyncClaimedSamplesAndCombinesDelayForEachLaterIteration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"items":[{"title":"value"}]}`)
	}))
	defer server.Close()

	store := &fakeKeywordSourceStore{}
	service := newService(store, Config{})
	samples := []int{-10, 2}
	sampleCalls := 0
	service.sampleDelay = func(minSeconds, maxSeconds int) int {
		if minSeconds != -10 || maxSeconds != 2 {
			t.Fatalf("sample bounds = %d..%d", minSeconds, maxSeconds)
		}
		value := samples[sampleCalls]
		sampleCalls++
		return value
	}
	var delays []time.Duration
	service.waitDelay = func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	}
	_, err := service.syncClaimed(context.Background(), storage.KeywordAPISource{
		ID: 14, RequestMethod: http.MethodGet, RequestURL: server.URL, BodyType: "none", TimeoutSeconds: 5,
		ResponsePath: "items[].title", IterationEnabled: true, IterationLocation: "query",
		IterationPath: "page", IterationStep: 1, IterationCount: 3, IterationDelaySeconds: 5,
		IterationRandomDelayMinSeconds: -10, IterationRandomDelayMaxSeconds: 2,
	})
	if err != nil {
		t.Fatalf("syncClaimed: %v", err)
	}
	if sampleCalls != 2 || len(delays) != 2 || delays[0] != 0 || delays[1] != 7*time.Second {
		t.Fatalf("sample calls=%d delays=%v", sampleCalls, delays)
	}
}

func TestSyncClaimedCancellationDuringDelayStopsImmediately(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"items":[{"title":"value"}]}`)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &fakeKeywordSourceStore{}
	service := newService(store, Config{})
	waits := 0
	service.waitDelay = func(ctx context.Context, _ time.Duration) error {
		waits++
		cancel()
		return ctx.Err()
	}
	_, err := service.syncClaimed(ctx, storage.KeywordAPISource{
		ID: 15, RequestMethod: http.MethodGet, RequestURL: server.URL, BodyType: "none", TimeoutSeconds: 5,
		ResponsePath: "items[].title", IterationEnabled: true, IterationLocation: "query",
		IterationPath: "page", IterationStep: 1, IterationCount: 3,
	})
	if err != nil {
		t.Fatalf("syncClaimed: %v", err)
	}
	input := store.completed
	if waits != 1 || input.RequestCount != 2 || input.SuccessCount != 1 || input.FailureCount != 1 || input.Status != storage.KeywordAPISourceStatusPartial {
		t.Fatalf("unexpected cancellation result: waits=%d input=%+v", waits, input)
	}
	if input.SuccessCount+input.FailureCount != input.RequestCount {
		t.Fatalf("request accounting invariant violated: %+v", input)
	}
	if store.completeContextErr != nil || !strings.Contains(input.ErrorMessage, "iteration cancelled") {
		t.Fatalf("completion context err=%v input=%+v", store.completeContextErr, input)
	}
}

func TestSyncClaimedCancellationDuringRequestStopsImmediately(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-started
		cancel()
	}()
	store := &fakeKeywordSourceStore{}
	service := newService(store, Config{})
	service.waitDelay = func(context.Context, time.Duration) error { return nil }
	_, err := service.syncClaimed(ctx, storage.KeywordAPISource{
		ID: 16, RequestMethod: http.MethodGet, RequestURL: server.URL, BodyType: "none", TimeoutSeconds: 5,
		ResponsePath: "items[].title", IterationEnabled: true, IterationLocation: "query",
		IterationPath: "page", IterationStep: 1, IterationCount: 3,
	})
	if err == nil {
		t.Fatal("expected cancelled synchronization error")
	}
	if store.failedRequestCount != 1 || store.failedFailureCount != 1 || !strings.Contains(store.failedMessage, "context canceled") {
		t.Fatalf("unexpected cancellation failure: %+v", store)
	}
}

func TestIterationDelayHelpers(t *testing.T) {
	for index := 0; index < 1_000; index++ {
		value := sampleIterationDelay(-3, 4)
		if value < -3 || value > 4 {
			t.Fatalf("sample %d is outside closed bounds", value)
		}
	}
	if value := sampleIterationDelay(-2, -2); value != -2 {
		t.Fatalf("fixed random delay = %d", value)
	}
	if delay := combinedIterationDelay(2, -5); delay != 0 {
		t.Fatalf("clamped delay = %s", delay)
	}
	if delay := combinedIterationDelay(5, -2); delay != 3*time.Second {
		t.Fatalf("combined delay = %s", delay)
	}
}

func TestKeywordSyncSamplesAreBoundedAndSafeForDisplay(t *testing.T) {
	values := []keywordsource.KeywordValue{
		{Value: "  first\nvalue  "},
		{Value: strings.Repeat("长", 140)},
		{Value: "third"},
		{Value: "fourth"},
		{Value: "fifth"},
		{Value: "not persisted"},
	}
	samples := keywordSyncSamples(values)
	if len(samples) != keywordSyncSampleLimit {
		t.Fatalf("sample count = %d, want %d", len(samples), keywordSyncSampleLimit)
	}
	if samples[0] != "firstvalue" {
		t.Fatalf("control characters were not removed: %q", samples[0])
	}
	if len([]rune(samples[1])) != keywordSyncSampleMaxRunes {
		t.Fatalf("long sample has %d runes", len([]rune(samples[1])))
	}
}
