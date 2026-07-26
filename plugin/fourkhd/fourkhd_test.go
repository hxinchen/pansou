package fourkhd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testPlugin(serverURL, apiKey string) *Plugin {
	p := New()
	p.apiURL = serverURL
	p.apiKeyProvider = func() string { return apiKey }
	p.gate = newRateGate(0)
	return p
}

func TestSearchSuccessBuildsQueryAndConvertsResults(t *testing.T) {
	var query url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"message":"success","data":[{"title":" 扫毒风暴 4K ","url":"https://pan.quark.cn/s/one","is_type":"quark","msg_date":"2026-07-13 01:18:53"},{"title":"重复","url":"https://pan.quark.cn/s/one","is_type":"quark"},{"title":"扫毒风暴 百度版","url":"https://pan.baidu.com/s/two?pwd=abcd","is_type":"baidu"},{"title":"错误类型","url":"https://pan.quark.cn/s/three","is_type":"baidu"}],"total":4}`))
	}))
	defer server.Close()

	p := testPlugin(server.URL, "test-secret")
	results, err := p.searchImpl(nil, "扫毒风暴", nil)
	if err != nil {
		t.Fatal(err)
	}
	if query.Get("api_key") != "test-secret" || query.Get("q") != "扫毒风暴" || query.Get("pan") != supportedPanQuery {
		t.Fatalf("unexpected query: %#v", query)
	}
	if len(results) != 2 {
		t.Fatalf("results=%d, want 2", len(results))
	}
	if results[0].Title != "扫毒风暴 4K" || results[0].Links[0].Type != "quark" || results[0].Datetime.IsZero() {
		t.Fatalf("unexpected first result: %#v", results[0])
	}
	if results[1].Links[0].Type != "baidu" || results[1].Links[0].Password != "abcd" {
		t.Fatalf("unexpected second result: %#v", results[1])
	}
}

func TestNormalizeLinkTypeSupportsAllRequestedPanTypes(t *testing.T) {
	tests := []struct {
		declared string
		rawURL   string
		want     string
	}{
		{declared: "quark", rawURL: "https://pan.quark.cn/s/quark", want: "quark"},
		{declared: "baidu", rawURL: "https://pan.baidu.com/s/baidu", want: "baidu"},
		{declared: "tianyiyun", rawURL: "https://cloud.189.cn/t/tianyi", want: "tianyi"},
		{declared: "aliyun", rawURL: "https://www.alipan.com/s/aliyun", want: "aliyun"},
		{declared: "uc", rawURL: "https://drive.uc.cn/s/uc", want: "uc"},
		{declared: "115", rawURL: "https://115.com/s/one115", want: "115"},
		{declared: "123", rawURL: "https://www.123684.com/s/one123", want: "123"},
		{declared: "pikpak", rawURL: "https://mypikpak.com/s/pikpak", want: "pikpak"},
		{declared: "xunlei", rawURL: "https://pan.xunlei.com/s/xunlei", want: "xunlei"},
		{declared: "magnet", rawURL: "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567", want: "magnet"},
		{declared: "ed2k", rawURL: "ed2k://|file|example.mkv|123|0123456789ABCDEF0123456789ABCDEF|/", want: "ed2k"},
	}
	for _, test := range tests {
		t.Run(test.declared, func(t *testing.T) {
			got, ok := normalizeLinkType(test.declared, test.rawURL)
			if !ok || got != test.want {
				t.Fatalf("normalizeLinkType(%q, %q)=(%q, %v), want (%q, true)", test.declared, test.rawURL, got, ok, test.want)
			}
		})
	}
}

func TestNormalizeLinkTypeRejectsDeclaredTypeMismatch(t *testing.T) {
	if got, ok := normalizeLinkType("baidu", "https://pan.quark.cn/s/wrong"); ok || got != "" {
		t.Fatalf("normalizeLinkType mismatch=(%q, %v), want rejected", got, ok)
	}
}

func TestSearchRejectsMissingAPIKeyBeforeRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()

	p := testPlugin(server.URL, "")
	_, err := p.searchImpl(nil, "keyword", nil)
	if err == nil || !strings.Contains(err.Error(), apiKeyEnv) {
		t.Fatalf("error=%v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls=%d, want 0", calls.Load())
	}
}

func TestSearchHandlesRateLimitAndInvalidJSON(t *testing.T) {
	for _, test := range []struct {
		name       string
		statusCode int
		body       string
		want       string
	}{
		{name: "rate limit", statusCode: http.StatusTooManyRequests, body: `{"message":"slow down"}`, want: "HTTP 429"},
		{name: "invalid JSON", statusCode: http.StatusOK, body: `{`, want: "invalid upstream response"},
		{name: "API failure", statusCode: http.StatusOK, body: `{"code":403,"message":"bad key"}`, want: "code 403"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.statusCode)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			_, err := testPlugin(server.URL, "secret-value").searchImpl(nil, "keyword", nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("error leaked API key: %v", err)
			}
		})
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, errors.New(req.URL.String())
}

func TestSearchDoesNotLeakAPIKeyFromTransportError(t *testing.T) {
	p := testPlugin("https://example.invalid/search", "transport-secret")
	p.client = &http.Client{Transport: failingTransport{}}
	_, err := p.searchImpl(nil, "keyword", nil)
	if err == nil || strings.Contains(err.Error(), "transport-secret") || strings.Contains(err.Error(), "api_key") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestSearchWithResultCachesAndSingleflights(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		_, _ = w.Write([]byte(`{"code":200,"data":[{"title":"keyword result","url":"https://pan.quark.cn/s/one","is_type":"quark"}]}`))
	}))
	defer server.Close()

	p := testPlugin(server.URL, "test-secret")
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := p.SearchWithResult("keyword", nil)
			if err != nil || !result.IsFinal || len(result.Results) != 1 {
				t.Errorf("result=%#v, error=%v", result, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("upstream calls=%d, want 1", calls.Load())
	}
	if _, err := p.SearchWithResult("keyword", map[string]interface{}{"refresh": true}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls after refresh=%d, want 2", calls.Load())
	}
}

func TestRateGateSerializesConcurrentRequests(t *testing.T) {
	const interval = 25 * time.Millisecond
	gate := newRateGate(interval)
	starts := make([]time.Time, 0, 3)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := gate.Wait(context.Background()); err != nil {
				t.Errorf("Wait: %v", err)
				return
			}
			mu.Lock()
			starts = append(starts, time.Now())
			mu.Unlock()
		}()
	}
	wg.Wait()
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
	for index := 1; index < len(starts); index++ {
		if gap := starts[index].Sub(starts[index-1]); gap < 20*time.Millisecond {
			t.Fatalf("request gap=%v, want at least 20ms", gap)
		}
	}
}

func TestRateGateHonorsContextCancellation(t *testing.T) {
	gate := newRateGate(time.Second)
	if err := gate.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := gate.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait error=%v", err)
	}
}
