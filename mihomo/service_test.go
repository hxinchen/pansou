package mihomo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestOverviewResolvesRuleGroupAndExit(t *testing.T) {
	const secret = "controller-secret"
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/exit" && r.Header.Get("Authorization") != "Bearer "+secret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/configs":
			_ = json.NewEncoder(w).Encode(map[string]any{"mode": "rule", "mixed-port": 7890})
		case "/proxies":
			_ = json.NewEncoder(w).Encode(testProxyPayload("自动选择"))
		case "/exit":
			_ = json.NewEncoder(w).Encode(map[string]any{"ip": "18.163.102.224", "country": "HK", "region": "Hong Kong", "city": "Hong Kong", "org": "AS16509 Amazon.com"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	service, err := NewService(Config{ControllerURL: server.URL, Secret: secret, ManagedGroups: []string{"良心云"}, ExitInfoURL: server.URL + "/exit", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	overview, err := service.Overview(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if overview.Mode != "rule" || overview.GlobalCurrent != "DIRECT" {
		t.Fatalf("unexpected runtime: %+v", overview)
	}
	if overview.Group.Current != "自动选择" || overview.Group.EffectiveNode != "🇭🇰香港专线04|BGP|流媒体" {
		t.Fatalf("unexpected group resolution: %+v", overview.Group)
	}
	if overview.Exit.IP != "18.163.102.224" || overview.Exit.Country != "中国香港" {
		t.Fatalf("unexpected exit: %+v", overview.Exit)
	}
	if len(overview.Route) < 6 || overview.Route[len(overview.Route)-1].Kind != "node" {
		t.Fatalf("unexpected route: %+v", overview.Route)
	}
}

func TestSelectValidatesManagedGroupAndCandidate(t *testing.T) {
	var mu sync.Mutex
	selected := "自动选择"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/configs":
			_ = json.NewEncoder(w).Encode(map[string]any{"mode": "rule", "mixed-port": 7890})
		case r.URL.Path == "/proxies":
			mu.Lock()
			current := selected
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(testProxyPayload(current))
		case r.URL.Path == "/proxies/良心云" && r.Method == http.MethodPut:
			var payload map[string]string
			_ = json.NewDecoder(r.Body).Decode(&payload)
			mu.Lock()
			selected = payload["name"]
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	service, err := NewService(Config{ControllerURL: server.URL, ManagedGroups: []string{"良心云"}, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	overview, err := service.Select(context.Background(), "良心云", "🇯🇵日本高速01|CTCU|0.5x")
	if err != nil {
		t.Fatal(err)
	}
	if overview.Group.Current != "🇯🇵日本高速01|CTCU|0.5x" || overview.Group.EffectiveNode != overview.Group.Current {
		t.Fatalf("selection was not applied: %+v", overview.Group)
	}
	if _, err := service.Select(context.Background(), "GLOBAL", "DIRECT"); err != ErrGroupNotManaged {
		t.Fatalf("unmanaged group error = %v", err)
	}
	if _, err := service.Select(context.Background(), "良心云", "不存在"); err != ErrInvalidSelection {
		t.Fatalf("invalid candidate error = %v", err)
	}
}

func TestLatencyTestSortsSuccessfulNodesAndMarksTimeouts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/configs":
			_ = json.NewEncoder(w).Encode(map[string]any{"mode": "rule", "mixed-port": 7890})
		case "/proxies":
			_ = json.NewEncoder(w).Encode(testProxyPayload("自动选择"))
		case "/group/良心云/delay":
			if r.URL.Query().Get("url") != "http://probe.test/generate_204" || r.URL.Query().Get("timeout") != "2000" {
				t.Fatalf("unexpected delay query: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]int{"🇯🇵日本高速01|CTCU|0.5x": 73, "自动选择": 91})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	service, err := NewService(Config{ControllerURL: server.URL, ManagedGroups: []string{"良心云"}, Timeout: time.Second, DelayTestURL: "http://probe.test/generate_204", DelayTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.TestLatency(context.Background(), "良心云")
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.Total != 4 || result.Summary.Succeeded != 2 || result.Summary.Failed != 2 {
		t.Fatalf("unexpected summary: %+v", result.Summary)
	}
	candidates := result.Overview.Group.Candidates
	if len(candidates) != 4 || candidates[0].Name != "🇯🇵日本高速01|CTCU|0.5x" || candidates[0].DelayMS != 73 || candidates[1].Name != "自动选择" {
		t.Fatalf("candidates not delay sorted: %+v", candidates)
	}
	if candidates[2].DelayMS != 0 || candidates[3].DelayMS != 0 {
		t.Fatalf("timed out candidates retained stale delays: %+v", candidates)
	}
	if result.Overview.LatencyTesting || result.Overview.LatencyTest == nil {
		t.Fatalf("unexpected latency state: %+v", result.Overview)
	}
}

func testProxyPayload(current string) map[string]any {
	return map[string]any{"proxies": map[string]any{
		"GLOBAL":             map[string]any{"name": "GLOBAL", "type": "Selector", "now": "DIRECT", "alive": true},
		"良心云":                map[string]any{"name": "良心云", "type": "Selector", "now": current, "alive": true, "all": []string{"自动选择", "故障转移", "剩余流量：824 GB", "🇭🇰香港专线04|BGP|流媒体", "🇯🇵日本高速01|CTCU|0.5x"}},
		"自动选择":               map[string]any{"name": "自动选择", "type": "URLTest", "now": "🇭🇰香港专线04|BGP|流媒体", "alive": true},
		"故障转移":               map[string]any{"name": "故障转移", "type": "Fallback", "now": "🇭🇰香港专线04|BGP|流媒体", "alive": true},
		"🇭🇰香港专线04|BGP|流媒体":   map[string]any{"name": "🇭🇰香港专线04|BGP|流媒体", "type": "Trojan", "alive": true, "history": []map[string]any{{"delay": 88}}},
		"🇯🇵日本高速01|CTCU|0.5x": map[string]any{"name": "🇯🇵日本高速01|CTCU|0.5x", "type": "Shadowsocks", "alive": true, "history": []map[string]any{{"delay": 121}}},
	}}
}
