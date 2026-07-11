package api

import (
	"encoding/json"
	"strings"
	"testing"
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
