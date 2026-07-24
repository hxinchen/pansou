package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const subscriptionTestConfig = `mixed-port: 7890
mode: rule
proxy-groups:
  - name: 良心云
    type: select
    proxies: [自动选择, 故障转移]
  - name: 自动选择
    type: url-test
    proxies: [节点A]
  - name: 故障转移
    type: fallback
    proxies: [节点A]
`

type subscriptionController struct {
	server       *httptest.Server
	failReload   atomic.Bool
	reloadCount  atomic.Int32
	updateCount  atomic.Int32
	lastReloadMu sync.Mutex
	lastReload   map[string]string
}

func newSubscriptionController(t *testing.T) *subscriptionController {
	t.Helper()
	fixture := &subscriptionController{}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/configs":
			fixture.reloadCount.Add(1)
			if r.URL.Query().Get("force") != "true" {
				t.Errorf("reload force query = %q", r.URL.Query().Get("force"))
			}
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			fixture.lastReloadMu.Lock()
			fixture.lastReload = body
			fixture.lastReloadMu.Unlock()
			if fixture.failReload.Load() {
				http.Error(w, "reload failed", http.StatusBadGateway)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/providers/proxies":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"providers":{"良心云":{"proxies":[{},{}]}}}`))
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/providers/proxies/"):
			fixture.updateCount.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func newSubscriptionService(t *testing.T, controller *subscriptionController) (*Service, string) {
	t.Helper()
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(configPath, []byte(subscriptionTestConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Config{
		ControllerURL: controller.server.URL,
		ManagedGroups: []string{"良心云"},
		ConfigPath:    configPath,
		ReloadPath:    "/etc/mihomo/config.yaml",
		Timeout:       2 * time.Second,
		DelayTestURL:  "http://www.gstatic.com/generate_204",
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, configPath
}

func TestSubscriptionLifecycleUpdatesMihomoConfig(t *testing.T) {
	controller := newSubscriptionController(t)
	service, configPath := newSubscriptionService(t, controller)
	ctx := context.Background()

	created, err := service.CreateSubscription(ctx, SubscriptionInput{
		Name: "主力订阅", URL: "https://subscription.example.com/api/v1/client?token=secret", IntervalSeconds: 1800, FetchVia: fetchViaAuto,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.URLMasked != "https://subscription.example.com/•••" || strings.Contains(created.URLMasked, "secret") {
		t.Fatalf("created subscription = %+v", created)
	}
	if created.Path != "./providers/"+created.Provider+".yaml" {
		t.Fatalf("provider path = %q", created.Path)
	}
	if !sameStrings(created.Groups, []string{"良心云", "自动选择", "故障转移"}) {
		t.Fatalf("subscription groups = %v", created.Groups)
	}
	if created.FetchVia != fetchViaAuto {
		t.Fatalf("created fetch via = %q", created.FetchVia)
	}

	assertSubscriptionConfig(t, configPath, created.Provider, 1800, "自动选择", []string{"良心云", "自动选择", "故障转移"})
	controller.lastReloadMu.Lock()
	if controller.lastReload["path"] != "/etc/mihomo/config.yaml" {
		t.Fatalf("reload payload = %#v", controller.lastReload)
	}
	controller.lastReloadMu.Unlock()

	name := "备用订阅"
	interval := 21600
	fetchVia := fetchViaFallback
	updated, err := service.UpdateSubscription(ctx, created.ID, SubscriptionPatch{Name: &name, IntervalSeconds: &interval, FetchVia: &fetchVia})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != name || updated.IntervalSeconds != interval || updated.FetchVia != fetchViaFallback {
		t.Fatalf("updated subscription = %+v", updated)
	}

	if _, err := service.UpdateSubscriptionNow(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if controller.updateCount.Load() != 1 {
		t.Fatalf("provider update count = %d", controller.updateCount.Load())
	}

	if err := service.DeleteSubscription(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	items, err := service.ListSubscriptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !items[0].Builtin || items[0].NodeCount != 2 {
		t.Fatalf("subscriptions after delete = %+v", items)
	}
	assertSubscriptionConfig(t, configPath, "", 0, "", nil)
}

func TestSubscriptionReloadFailureRestoresConfig(t *testing.T) {
	controller := newSubscriptionController(t)
	service, configPath := newSubscriptionService(t, controller)
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	controller.failReload.Store(true)
	if _, err := service.CreateSubscription(context.Background(), SubscriptionInput{Name: "失败订阅", URL: "https://example.com/sub", IntervalSeconds: 3600}); err == nil {
		t.Fatal("expected reload failure")
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("config was not restored\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestSubscriptionValidationRejectsPrivateURLs(t *testing.T) {
	for _, rawURL := range []string{"http://127.0.0.1/sub", "http://10.0.0.1/sub", "https://localhost/sub", "ftp://example.com/sub"} {
		if err := validateSubscription("订阅", rawURL); err == nil {
			t.Fatalf("expected %q to be rejected", rawURL)
		}
	}
}

func TestSubscriptionValidationRejectsUnknownFetchRoute(t *testing.T) {
	controller := newSubscriptionController(t)
	service, _ := newSubscriptionService(t, controller)
	if _, err := service.CreateSubscription(context.Background(), SubscriptionInput{Name: "订阅", URL: "https://example.com/sub", FetchVia: "unknown"}); !errors.Is(err, ErrInvalidSubscription) {
		t.Fatalf("create error = %v", err)
	}
}

func TestSubscriptionURLDeduplicationNormalizesQueryAndFragment(t *testing.T) {
	controller := newSubscriptionController(t)
	service, _ := newSubscriptionService(t, controller)
	if _, err := service.CreateSubscription(context.Background(), SubscriptionInput{Name: "订阅一", URL: "https://EXAMPLE.com/sub?b=2&a=1", IntervalSeconds: 3600}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateSubscription(context.Background(), SubscriptionInput{Name: "订阅二", URL: "https://example.com/sub?a=1&b=2#ignored", IntervalSeconds: 3600}); !errors.Is(err, ErrDuplicateSubscription) {
		t.Fatalf("duplicate create error = %v", err)
	}
}

func TestControllerProxyNodeKeysDeduplicateAndIgnorePolicies(t *testing.T) {
	proxies := []json.RawMessage{
		json.RawMessage(`{"name":"节点 A","type":"Vless"}`),
		json.RawMessage(`{"name":"节点 A","type":"Vless"}`),
		json.RawMessage(`{"name":"节点 B","type":"Hysteria2"}`),
		json.RawMessage(`{"name":"剩余流量：100 GB","type":"Vless"}`),
		json.RawMessage(`{"name":"DIRECT","type":"Direct"}`),
	}
	keys, duplicates := controllerProxyNodeKeys(proxies)
	if len(keys) != 2 || duplicates != 1 {
		t.Fatalf("node keys=%d duplicates=%d", len(keys), duplicates)
	}
}

func TestConcurrentSubscriptionUpdatesKeepConfigValid(t *testing.T) {
	controller := newSubscriptionController(t)
	service, configPath := newSubscriptionService(t, controller)
	created, err := service.CreateSubscription(context.Background(), SubscriptionInput{Name: "并发订阅", URL: "https://example.com/sub", IntervalSeconds: 3600})
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for index := 0; index < 8; index++ {
		group.Add(1)
		go func(value int) {
			defer group.Done()
			interval := 900 + value*300
			_, _ = service.UpdateSubscription(context.Background(), created.ID, SubscriptionPatch{IntervalSeconds: &interval})
		}(index)
	}
	group.Wait()
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("config is invalid YAML: %v\n%s", err, data)
	}
	items, err := service.ListSubscriptions(context.Background())
	if err != nil || len(items) != 2 || !items[0].Builtin {
		t.Fatalf("subscriptions after concurrent updates = %+v err=%v", items, err)
	}
}

func assertSubscriptionConfig(t *testing.T, configPath, providerName string, interval int, expectedProxy string, expectedGroups []string) {
	t.Helper()
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	providers, groups, err := (&Service{configPath: configPath}).readSubscriptionConfig()
	if err != nil {
		t.Fatalf("read subscription config: %v\n%s", err, data)
	}
	if providerName == "" {
		for name := range providers {
			if strings.HasPrefix(name, providerPrefix) {
				t.Fatalf("provider %q still exists", name)
			}
		}
		return
	}
	provider, ok := providers[providerName]
	if !ok || provider.Interval != interval || provider.Proxy != expectedProxy {
		t.Fatalf("provider %q = %+v found=%v", providerName, provider, ok)
	}
	if !sameStrings(groups[providerName], expectedGroups) {
		t.Fatalf("provider groups = %v", groups[providerName])
	}
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[string]int, len(left))
	for _, value := range left {
		seen[value]++
	}
	for _, value := range right {
		seen[value]--
		if seen[value] < 0 {
			return false
		}
	}
	return true
}
