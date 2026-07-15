package xdyh

import "testing"

func TestConvertToSearchResultsPreservesSourceSite(t *testing.T) {
	plugin := NewXdyhPlugin()
	results := plugin.convertToSearchResults(APIResponse{Data: []SearchResultItem{{
		Title:      "示例资源",
		SourceSite: "  示例站点  ",
		DriveLinks: []string{"https://pan.quark.cn/s/example"},
	}}}, "示例")
	if len(results) != 1 {
		t.Fatalf("result count = %d, want 1", len(results))
	}
	if results[0].SubSource != "示例站点" {
		t.Fatalf("sub_source = %q, want 示例站点", results[0].SubSource)
	}
}
