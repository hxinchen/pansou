package storage

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCollectionDetailPayloadShapesStayBounded(t *testing.T) {
	runs := RunPage{Items: make([]CollectionRun, 20), Total: 1200, Page: 1, PageSize: 20}
	for index := range runs.Items {
		runs.Items[index] = compactRunListItem(CollectionRun{
			ID: int64(index + 1), Trigger: "scheduled", Status: RunRunning, TotalItems: 1200,
			ErrorMessage: strings.Repeat("错", runErrorSummaryLimit),
			CurrentItem:  &CollectionRunItem{ID: 99, SourceSummary: map[string]any{"large": strings.Repeat("值", 4096)}},
			Items:        []CollectionRunItem{{ID: 100, SourceSummary: map[string]any{"large": strings.Repeat("值", 4096)}}},
		})
	}
	for _, run := range runs.Items {
		if run.ErrorMessage != "" || run.CurrentItem != nil || run.Items != nil {
			t.Fatalf("run list item retained detail fields: %#v", run)
		}
	}
	assertJSONSizeBelow(t, "run list", runs, 100*1024)
	if encoded, err := json.Marshal(runs); err != nil {
		t.Fatalf("marshal compact run list: %v", err)
	} else if strings.Contains(string(encoded), "error_message") || strings.Contains(string(encoded), "current_item") || strings.Contains(string(encoded), "source_summary") {
		t.Fatalf("run list leaked detail fields: %s", encoded)
	}

	summary := CollectionRun{ID: 1, Trigger: "scheduled", Status: RunRunning, TotalItems: 1200,
		CurrentItem: &CollectionRunItem{ID: 2, Keyword: "current", KeywordType: DefaultKeywordType, Status: RunRunning}}
	assertJSONSizeBelow(t, "run summary", summary, 20*1024)

	items := RunItemPage{Items: make([]CollectionRunItem, 30), Total: 1200, Page: 1, PageSize: 30}
	for index := range items.Items {
		items.Items[index] = CollectionRunItem{ID: int64(index + 1), Keyword: strings.Repeat("关", 80),
			KeywordType: DefaultKeywordType, Status: RunFailed, ErrorMessage: strings.Repeat("错", 500), SourceTotal: 150, SourceFailed: 150}
	}
	assertJSONSizeBelow(t, "run item page", items, 250*1024)

	sources := RunSourcePage{Items: make([]RunSource, 50), Total: 150, Page: 1, PageSize: 50}
	for index := range sources.Items {
		sources.Items[index] = RunSource{Key: "plugin", Type: "plugin", Status: RunFailed, Attempts: 3,
			DurationMS: 1200, Error: strings.Repeat("错", 1000)}
	}
	assertJSONSizeBelow(t, "run source page", sources, 250*1024)
}

func TestResourceSummaryOmitsFullAssociations(t *testing.T) {
	encoded, err := json.Marshal(Resource{ID: 1, SourceCount: 931, KeywordCount: 292,
		SourcePreview: []ResourceSourcePreview{{ID: 1, SourceType: "plugin", SourceKey: "pansearch"}}})
	if err != nil {
		t.Fatalf("marshal resource summary: %v", err)
	}
	text := string(encoded)
	for _, field := range []string{`"sources"`, `"keywords"`, `"password"`, `"content"`} {
		if strings.Contains(text, field) {
			t.Fatalf("resource summary contains omitted field %s: %s", field, text)
		}
	}

	encoded, err = json.Marshal(ResourceSourcePage{Items: []ResourceSource{{
		ID: 1, ResourceID: 1, SourceType: "plugin", SourceKey: "pansearch", Title: "preview",
	}}, Total: 1, Page: 1, PageSize: 50})
	if err != nil {
		t.Fatalf("marshal resource source page: %v", err)
	}
	text = string(encoded)
	for _, field := range []string{`"content"`, `"source_metadata"`} {
		if strings.Contains(text, field) {
			t.Fatalf("resource source page contains omitted field %s: %s", field, text)
		}
	}
}

func TestFormatRunErrorSummaryCountsFailedItemsAndHandlesMissingDetail(t *testing.T) {
	messages := []string{"", "second failure", "third failure", "fourth failure", "fifth failure"}
	summary := formatRunErrorSummary(messages, 8)
	if !strings.Contains(summary, runMissingErrorDetailText) {
		t.Fatalf("summary missing empty-error placeholder: %q", summary)
	}
	if !strings.Contains(summary, "... and 3 more") {
		t.Fatalf("summary has inaccurate remaining count: %q", summary)
	}
	if strings.Count(summary, "; ") != 5 {
		t.Fatalf("summary should contain five displayed failures and one remainder: %q", summary)
	}
}

func TestFormatRunErrorSummaryKeepsFiveBoundedMessages(t *testing.T) {
	messages := []string{
		strings.Repeat("a", runErrorItemLimit+100), strings.Repeat("b", runErrorItemLimit+100),
		strings.Repeat("c", runErrorItemLimit+100), strings.Repeat("d", runErrorItemLimit+100),
		strings.Repeat("e", runErrorItemLimit+100),
	}
	summary := formatRunErrorSummary(messages, len(messages))
	if len([]rune(summary)) > runErrorSummaryLimit {
		t.Fatalf("summary length = %d, want <= %d", len([]rune(summary)), runErrorSummaryLimit)
	}
	for _, prefix := range []string{"a", "b", "c", "d", "e"} {
		if !strings.Contains(summary, prefix) {
			t.Fatalf("summary dropped one of five messages: %q", summary)
		}
	}
}

func assertJSONSizeBelow(t *testing.T, name string, value any, limit int) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %s: %v", name, err)
	}
	if len(encoded) >= limit {
		t.Fatalf("%s payload = %d bytes, want < %d", name, len(encoded), limit)
	}
}
