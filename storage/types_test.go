package storage

import (
	"testing"
	"time"
)

func TestToSearchResultPreservesSourceKindsAndUniqueKeys(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		resource    Resource
		wantID      string
		wantChannel string
	}{
		{
			name: "telegram",
			resource: Resource{NormalizedURL: "https://pan.example/a", URL: "https://pan.example/a", LastSeenAt: now,
				Sources: []ResourceSource{{SourceType: "tg", SourceKey: "movies", UniqueID: "message-1"}}},
			wantID: "https://pan.example/a", wantChannel: "movies",
		},
		{
			name: "plugin",
			resource: Resource{NormalizedURL: "https://pan.example/b", URL: "https://pan.example/b", LastSeenAt: now,
				Sources: []ResourceSource{{SourceType: "plugin", SourceKey: "pansearch", UniqueID: "pansearch-1"}}},
			wantID: "pansearch-https://pan.example/b", wantChannel: "",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result := test.resource.ToSearchResult()
			if result.UniqueID != test.wantID || result.Channel != test.wantChannel {
				t.Fatalf("ToSearchResult source = (%q,%q), want (%q,%q)", result.UniqueID, result.Channel, test.wantID, test.wantChannel)
			}
		})
	}
}

func TestInferAndParseSourcesDoNotUseTrigger(t *testing.T) {
	t.Parallel()
	typeName, key := parseMergedSource("plugin:pansearch")
	if typeName != "plugin" || key != "pansearch" {
		t.Fatalf("parseMergedSource() = (%q,%q)", typeName, key)
	}
	typeName, key = parseMergedSource("")
	if typeName != "unknown" || key != "" {
		t.Fatalf("empty parseMergedSource() = (%q,%q)", typeName, key)
	}
}
