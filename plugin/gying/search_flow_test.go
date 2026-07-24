package gying

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cloudscraper "github.com/Advik-B/cloudscraper/lib"
)

func TestFetchDetailContextCancelsUpstreamRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Second):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200}`))
		}
	}))
	defer server.Close()

	p := &GyingPlugin{baseURL: server.URL}
	scraper, err := cloudscraper.New(cloudscraper.WithSessionConfig(false, time.Hour, 0))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = p.fetchDetailContext(ctx, "1", "mv", scraper)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("request cancellation took %v", elapsed)
	}
}

func TestFetchSearchSuggestionsRequestsIdentityEncoding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept-Encoding"); got != "identity" {
			t.Errorf("Accept-Encoding = %q, want identity", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()
	scraper, err := cloudscraper.New(cloudscraper.WithSessionConfig(false, time.Hour, 0))
	if err != nil {
		t.Fatal(err)
	}
	items, err := (&GyingPlugin{baseURL: server.URL}).fetchSearchSuggestionsContext(context.Background(), "电影", scraper)
	if err != nil || len(items) != 0 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
}

func TestSearchWithScraperUsesAdaptiveFirstBatch(t *testing.T) {
	var detailRequests atomic.Int32
	items := make([]SearchSuggestItem, 15)
	for index := range items {
		items[index] = SearchSuggestItem{Title: fmt.Sprintf("测试电影 %02d", index), ID: fmt.Sprint(index), Dir: "mv", Year: 2025 - index}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/res/search_suggest":
			_ = json.NewEncoder(w).Encode(items)
		case strings.HasPrefix(r.URL.Path, "/res/downurl/"):
			detailRequests.Add(1)
			detail := DetailData{Code: http.StatusOK}
			detail.Panlist.Name = []string{"资源"}
			detail.Panlist.URL = []string{"https://pan.quark.cn/s/example" + strings.TrimPrefix(r.URL.Path, "/res/downurl/mv/")}
			detail.Panlist.P = []string{""}
			detail.Panlist.Type = []int{2}
			detail.Panlist.Time = []string{"2天前"}
			_ = json.NewEncoder(w).Encode(detail)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	p := &GyingPlugin{baseURL: server.URL}
	scraper, err := cloudscraper.New(cloudscraper.WithSessionConfig(false, time.Hour, 0))
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := p.searchWithScraperContext(context.Background(), "测试电影", scraper)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Complete || outcome.Stats.Candidates != 15 || outcome.Stats.Attempted != InitialDetailBatch || outcome.Stats.Succeeded != InitialDetailBatch {
		t.Fatalf("outcome stats = %#v, complete=%v", outcome.Stats, outcome.Complete)
	}
	if len(outcome.Results) != InitialDetailBatch || int(detailRequests.Load()) != InitialDetailBatch {
		t.Fatalf("results=%d detail requests=%d", len(outcome.Results), detailRequests.Load())
	}
	if age := time.Since(outcome.Results[0].Datetime); age < 48*time.Hour || age > 72*time.Hour {
		t.Fatalf("result datetime did not use upstream update time: age=%v", age)
	}
}
