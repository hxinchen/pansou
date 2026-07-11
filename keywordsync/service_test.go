package keywordsync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"pansou/keywordsource"
	"pansou/storage"
)

type fakeKeywordSourceStore struct {
	completed storage.KeywordAPISourceSyncInput
}

func (f *fakeKeywordSourceStore) ClaimDueKeywordAPISource(context.Context, time.Time) (*storage.KeywordAPISource, error) {
	return nil, nil
}
func (f *fakeKeywordSourceStore) ClaimKeywordAPISourceForSync(context.Context, int64, time.Time) (storage.KeywordAPISource, error) {
	return storage.KeywordAPISource{}, nil
}
func (f *fakeKeywordSourceStore) CompleteKeywordAPISourceSync(_ context.Context, input storage.KeywordAPISourceSyncInput) (storage.KeywordAPISourceSyncResult, error) {
	f.completed = input
	return storage.KeywordAPISourceSyncResult{Source: storage.KeywordAPISource{LastStatus: input.Status}}, nil
}
func (f *fakeKeywordSourceStore) FailKeywordAPISourceSync(context.Context, int64, string, time.Time) (storage.KeywordAPISource, error) {
	return storage.KeywordAPISource{}, nil
}
func (f *fakeKeywordSourceStore) FailKeywordAPISourceSyncWithStats(context.Context, int64, string, time.Time, int, int) (storage.KeywordAPISource, error) {
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
