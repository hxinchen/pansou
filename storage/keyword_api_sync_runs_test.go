package storage

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestKeywordAPISyncRequestSummaryRedactsSecrets(t *testing.T) {
	cooldown := int64(90)
	summary := keywordAPISyncRequestSummary(KeywordAPISource{
		RequestMethod: "POST",
		RequestURL:    "https://user:password@example.test/api/path-secret/items?token=query-secret&page=2#fragment",
		RequestHeaders: map[string]string{
			"X-Token": "header-secret", "Accept": "application/json",
		},
		QueryParams:            map[string]string{"lang": "zh", "api_key": "query-map-secret"},
		BodyType:               "json",
		RequestBody:            `{"password":"body-secret"}`,
		ProxyURL:               "socks5h://proxy-user:proxy-secret@proxy.example.test:1080?token=proxy-query",
		TimeoutSeconds:         12,
		ResponsePath:           "data.items[].name",
		DefaultKeywordType:     "movie",
		DefaultKeywordEnabled:  true,
		DefaultPriority:        7,
		DefaultCooldownSeconds: &cooldown,
		IterationEnabled:       true,
		IterationLocation:      "query",
		IterationPath:          "offset",
		IterationCount:         3,
		IterationStep:          20,
	})
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, secret := range []string{"password", "path-secret", "query-secret", "header-secret", "query-map-secret", "body-secret", "proxy-secret", "proxy-query"} {
		if strings.Contains(text, secret) {
			t.Fatalf("request summary leaked %q: %s", secret, text)
		}
	}
	if summary.RequestURL != "https://example.test" || summary.ProxyScheme != "socks5h" || !summary.HasRequestBody {
		t.Fatalf("summary = %+v", summary)
	}
	if !reflect.DeepEqual(summary.HeaderKeys, []string{"Accept", "X-Token"}) ||
		!reflect.DeepEqual(summary.QueryKeys, []string{"api_key", "lang"}) {
		t.Fatalf("summary keys = headers:%v query:%v", summary.HeaderKeys, summary.QueryKeys)
	}
}

func TestKeywordAPISourceConfigChanged(t *testing.T) {
	cooldown := int64(30)
	base := KeywordAPISource{
		Name: "Source", Enabled: true, SyncIntervalSeconds: 3600,
		RequestMethod: "GET", RequestURL: "https://example.test/items",
		RequestHeaders: map[string]string{"Accept": "application/json"}, QueryParams: map[string]string{"page": "1"},
		BodyType: "none", TimeoutSeconds: 15, ResponsePath: "items[]",
		DefaultKeywordType: "general", DefaultKeywordEnabled: true, DefaultCooldownSeconds: &cooldown,
		IterationLocation: "query", IterationCount: 1,
	}
	nonConfig := base
	nonConfig.Name = "Renamed"
	nonConfig.Enabled = false
	nonConfig.SyncIntervalSeconds = 7200
	if keywordAPISourceConfigChanged(base, nonConfig) {
		t.Fatal("name, enabled, and schedule changes must not bump the config revision")
	}
	changed := base
	changed.QueryParams = map[string]string{"page": "2"}
	if !keywordAPISourceConfigChanged(base, changed) {
		t.Fatal("query parameter value change did not bump the config revision")
	}
	equalCooldown := base
	equalValue := int64(30)
	equalCooldown.DefaultCooldownSeconds = &equalValue
	if keywordAPISourceConfigChanged(base, equalCooldown) {
		t.Fatal("equal cooldown pointers must not bump the config revision")
	}
	changedCooldown := base
	changedValue := int64(31)
	changedCooldown.DefaultCooldownSeconds = &changedValue
	if !keywordAPISourceConfigChanged(base, changedCooldown) {
		t.Fatal("cooldown value change did not bump the config revision")
	}
}

func TestNormalizeKeywordAPISyncSamples(t *testing.T) {
	long := strings.Repeat("界", 125)
	got := normalizeKeywordAPISyncSamples([]string{"", " one ", long, "three", "four", "five", "six"})
	if len(got) != 5 || got[0] != "one" || len([]rune(got[1])) != 120 || got[4] != "five" {
		t.Fatalf("samples = %#v", got)
	}
}

func TestNormalizeKeywordAPISyncFinalizePreservesZeroRequestFailures(t *testing.T) {
	if _, _, _, _, _, err := normalizeKeywordAPISyncFinalize(KeywordAPISyncFinalizeInput{
		Status: KeywordAPISyncRunStatusSuccess, RequestCount: 2, SuccessCount: 2,
	}); err != nil {
		t.Fatalf("valid finalize: %v", err)
	}
	if _, _, _, _, _, err := normalizeKeywordAPISyncFinalize(KeywordAPISyncFinalizeInput{
		Status: KeywordAPISyncRunStatusPartial, RequestCount: 2, SuccessCount: 2,
	}); err == nil {
		t.Fatal("partial finalize without failures was accepted")
	}
}
