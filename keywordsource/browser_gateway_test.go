package keywordsource

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExecuteViaBrowserGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/fetch" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var request browserGatewayRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode gateway request: %v", err)
		}
		if request.Method != "GET" || request.URL != "https://example.test/items" || request.Query["page"] != "2" || request.Session != "movies" {
			t.Fatalf("gateway request = %+v", request)
		}
		_ = json.NewEncoder(w).Encode(browserGatewayResponse{
			StatusCode:  http.StatusOK,
			ContentType: "application/json",
			Body:        `{"items":[{"title":"Alpha"},{"title":"Beta"}]}`,
		})
	}))
	defer server.Close()

	result, err := Test(context.Background(), RequestConfig{
		Executor: ExecutorBrowser, BrowserGateway: server.URL + "/fetch", BrowserSession: "movies",
		Method: http.MethodGet, URL: "https://example.test/items", Query: map[string]string{"page": "2"},
		BodyType: BodyNone, TimeoutSeconds: 5,
	}, "items[].title")
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if result.StatusCode != http.StatusOK || result.Extraction == nil || result.Extraction.UniqueCount != 2 {
		t.Fatalf("result = %+v", result)
	}
	if result.Extraction.Values[0].Value != "Alpha" || result.Extraction.Values[1].Value != "Beta" {
		t.Fatalf("values = %+v", result.Extraction.Values)
	}
}
