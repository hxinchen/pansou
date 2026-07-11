package keywordsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func validConfig(target string) RequestConfig {
	return RequestConfig{Method: http.MethodGet, URL: target, BodyType: BodyNone, TimeoutSeconds: 2}
}

func TestValidateRequestConfig(t *testing.T) {
	t.Parallel()
	base := validConfig("https://example.com/api")
	for _, method := range []string{"GET", "post", "PUT", "PATCH"} {
		config := base
		config.Method = method
		if err := ValidateRequestConfig(config); err != nil {
			t.Fatalf("method %s: %v", method, err)
		}
	}
	for _, proxyURL := range []string{"", "http://proxy.test:8080", "https://proxy.test", "socks5://user:pass@proxy.test:1080", "socks5h://proxy.test:1080"} {
		config := base
		config.ProxyURL = proxyURL
		if err := ValidateRequestConfig(config); err != nil {
			t.Fatalf("proxy %q: %v", proxyURL, err)
		}
	}

	invalid := []RequestConfig{
		{Method: "DELETE", URL: base.URL},
		{Method: "GET", URL: "file:///tmp/input"},
		{Method: "GET", URL: "/relative"},
		{Method: "GET", URL: base.URL, BodyType: BodyJSON, Body: "{"},
		{Method: "GET", URL: base.URL, BodyType: "xml"},
		{Method: "GET", URL: base.URL, TimeoutSeconds: 61},
		{Method: "GET", URL: base.URL, MaxRedirects: 11},
		{Method: "GET", URL: base.URL, ProxyURL: "ftp://proxy.test"},
		{Method: "GET", URL: base.URL, Headers: map[string]string{"Bad Header": "x"}},
	}
	for i, config := range invalid {
		if err := ValidateRequestConfig(config); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("invalid[%d] error = %v", i, err)
		}
	}
}

func TestExecuteBuildsQueryHeadersAndBodies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		config     RequestConfig
		wantMethod string
		wantType   string
		wantBody   string
		wantQuery  string
		wantAccept string
	}{
		{
			name:       "get query and custom accept",
			config:     RequestConfig{Method: "GET", Query: map[string]string{"q": "中文 资源"}, Headers: map[string]string{"X-Token": "abc", "Accept": "application/vnd.test+json"}},
			wantMethod: "GET", wantQuery: "中文 资源", wantAccept: "application/vnd.test+json",
		},
		{
			name:       "json",
			config:     RequestConfig{Method: "POST", BodyType: BodyJSON, Body: `{"keyword":"test"}`},
			wantMethod: "POST", wantType: "application/json", wantBody: `{"keyword":"test"}`, wantAccept: "application/json",
		},
		{
			name:       "form",
			config:     RequestConfig{Method: "PUT", BodyType: BodyForm, Form: map[string]string{"keyword": "one two"}},
			wantMethod: "PUT", wantType: "application/x-www-form-urlencoded", wantBody: "keyword=one+two", wantAccept: "application/json",
		},
		{
			name:       "raw",
			config:     RequestConfig{Method: "PATCH", BodyType: BodyRaw, Body: "raw body"},
			wantMethod: "PATCH", wantType: "text/plain; charset=utf-8", wantBody: "raw body", wantAccept: "application/json",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if r.Method != test.wantMethod || r.Header.Get("Content-Type") != test.wantType || string(body) != test.wantBody || r.Header.Get("Accept") != test.wantAccept {
					t.Errorf("request = method %s type %q accept %q body %q", r.Method, r.Header.Get("Content-Type"), r.Header.Get("Accept"), body)
				}
				if test.wantQuery != "" && r.URL.Query().Get("q") != test.wantQuery {
					t.Errorf("query = %q", r.URL.Query().Get("q"))
				}
				if test.config.Headers["X-Token"] != "" && r.Header.Get("X-Token") != "abc" {
					t.Errorf("X-Token = %q", r.Header.Get("X-Token"))
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"data":{"items":[{"name":"one"}]}}`)
			}))
			defer server.Close()
			config := test.config
			config.URL = server.URL
			config.TimeoutSeconds = 2
			response, err := Execute(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != 200 || response.SizeBytes == 0 || response.JSON == nil {
				t.Fatalf("response = %#v", response)
			}
		})
	}
}

func TestExecuteRejectsInvalidAndOversizedJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want error
	}{
		{name: "invalid", body: "<html>no</html>", want: ErrInvalidJSON},
		{name: "multiple", body: `{}` + "\n" + `{}`, want: ErrInvalidJSON},
		{name: "oversized", body: `"` + strings.Repeat("x", MaxResponseBytes) + `"`, want: ErrResponseTooLarge},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			_, err := Execute(context.Background(), validConfig(server.URL))
			if !errors.Is(err, test.want) {
				t.Fatalf("Execute error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestExecuteReturnsHTTPStatusErrorWithDecodedJSON(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":"upstream"}`)
	}))
	defer server.Close()
	response, err := Execute(context.Background(), validConfig(server.URL))
	var statusError *HTTPStatusError
	if !errors.As(err, &statusError) || statusError.StatusCode != http.StatusBadGateway {
		t.Fatalf("Execute error = %v", err)
	}
	if response.StatusCode != http.StatusBadGateway || response.JSON == nil {
		t.Fatalf("response = %#v", response)
	}
}

func TestExecuteLimitsRedirects(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step := 0
		_, _ = fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/"), "%d", &step)
		http.Redirect(w, r, fmt.Sprintf("/%d", step+1), http.StatusFound)
	}))
	defer server.Close()
	config := validConfig(server.URL + "/0")
	config.MaxRedirects = 2
	_, err := Execute(context.Background(), config)
	if err == nil || !strings.Contains(err.Error(), "redirect limit") {
		t.Fatalf("Execute error = %v", err)
	}
}

func TestExecuteHonorsContextCancellation(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := Execute(ctx, validConfig(server.URL))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute error = %v", err)
	}
}

func TestExecuteUsesHTTPProxy(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !r.URL.IsAbs() || r.URL.Host != "upstream.invalid" {
			t.Errorf("proxy request URL = %q", r.URL.String())
		}
		_, _ = io.WriteString(w, `{"keywords":["one"]}`)
	}))
	defer proxyServer.Close()
	config := validConfig("http://upstream.invalid/data")
	config.ProxyURL = proxyServer.URL
	response, err := Execute(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || response.StatusCode != 200 {
		t.Fatalf("proxy calls = %d response = %#v", calls.Load(), response)
	}
}

func TestTestReturnsCandidatesAndExtraction(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"items":[{"name":" One "},{"name":"one"},{"name":"Two"}]}}`)
	}))
	defer server.Close()
	result, err := Test(context.Background(), validConfig(server.URL), "data.items[].name")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 1 || result.Extraction == nil || result.Extraction.RawCount != 3 || result.Extraction.UniqueCount != 2 {
		t.Fatalf("Test result = %#v", result)
	}
}

func TestRedactError(t *testing.T) {
	t.Parallel()
	config := RequestConfig{
		URL:      "https://api-user:url-pass@example.com/items?token=url-token&plain=value",
		Headers:  map[string]string{"Authorization": "Bearer header-secret", "Cookie": "session=cookie-secret"},
		Query:    map[string]string{"api_key": "query-secret"},
		BodyType: BodyJSON,
		Body:     `{"password":"body-secret","normal":"visible"}`,
		ProxyURL: "socks5://proxy-user:proxy-pass@proxy.example:1080",
	}
	original := errors.New("Authorization=Bearer header-secret cookie:session=cookie-secret url-pass url-token query-secret body-secret proxy-user proxy-pass normal=visible")
	err := RedactError(original, config)
	for _, secret := range []string{"header-secret", "cookie-secret", "url-pass", "url-token", "query-secret", "body-secret", "proxy-user", "proxy-pass"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("redacted error contains %q: %s", secret, err)
		}
	}
	if !strings.Contains(err.Error(), "normal=visible") || !errors.Is(err, original) {
		t.Fatalf("redacted error = %q", err)
	}
	if safe := sanitizeURL(config.URL); strings.Contains(safe, "url-pass") || strings.Contains(safe, "url-token") {
		t.Fatalf("sanitizeURL() = %q", safe)
	}
}

func TestJSONNumberPrecisionIsPreserved(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"uid":9007199254740993}`)
	}))
	defer server.Close()
	response, err := Execute(context.Background(), validConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	values, err := Extract(response.JSON, "uid")
	if err != nil || len(values) != 1 || values[0] != "9007199254740993" {
		t.Fatalf("values = %#v, %v", values, err)
	}
}

func TestFormEncodingMatchesURLValues(t *testing.T) {
	t.Parallel()
	values := url.Values{"a": {"x y"}, "b": {"中文"}}
	config := RequestConfig{Method: "POST", URL: "https://example.com", BodyType: BodyForm, Form: map[string]string{"a": "x y", "b": "中文"}}
	req, err := buildRequest(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(req.Body)
	if string(body) != values.Encode() {
		t.Fatalf("form body = %q, want %q", body, values.Encode())
	}
}

func TestResponseCanBeMarshaledWithoutRawBody(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(Response{StatusCode: 200, SizeBytes: 12, JSON: map[string]any{"ok": true}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "body") {
		t.Fatalf("Response JSON unexpectedly contains body: %s", data)
	}
}
