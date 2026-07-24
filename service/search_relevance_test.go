package service

import (
	"testing"
	"time"

	"pansou/model"
)

func TestRankSearchResultsFiltersUnrelatedDirectoryPosts(t *testing.T) {
	now := time.Now()
	results := []model.SearchResult{
		{Title: "短剧合集目录", Content: "刘晓燕等数百部短剧", Datetime: now, Links: []model.Link{{Type: "quark", URL: "https://pan.quark.cn/s/directory"}}},
		{Title: "晓燕之死：刘晓燕", Content: "", Datetime: now.Add(-time.Hour), Links: []model.Link{{Type: "quark", URL: "https://pan.quark.cn/s/exact"}}},
		{Title: "刘晓燕访谈", Content: "", Datetime: now.Add(-2 * time.Hour), Links: []model.Link{{Type: "baidu", URL: "https://pan.baidu.com/s/interview"}}},
	}

	ranked := rankSearchResults(results, "刘晓燕")
	if len(ranked) != 3 {
		t.Fatalf("ranked len = %d, want 3", len(ranked))
	}
	if ranked[0].Title != "晓燕之死：刘晓燕" && ranked[0].Title != "刘晓燕访谈" {
		t.Fatalf("first result = %q, want title match", ranked[0].Title)
	}
	merged := mergeResultsByType(ranked, "刘晓燕", nil)
	if len(merged["quark"]) != 1 || merged["quark"][0].URL != "https://pan.quark.cn/s/exact" {
		t.Fatalf("quark links = %+v, unrelated directory link should be filtered", merged["quark"])
	}
}

func TestSearchRelevanceIgnoresPunctuationAndPrefersExactTitle(t *testing.T) {
	results := []model.SearchResult{
		{Title: "2026 刘-晓 燕 课程合集", Links: []model.Link{{Type: "quark", URL: "https://pan.quark.cn/s/partial"}}},
		{Title: "刘晓燕", Links: []model.Link{{Type: "baidu", URL: "https://pan.baidu.com/s/exact"}}},
	}
	ranked := rankSearchResults(results, "刘晓燕")
	if len(ranked) != 2 || ranked[0].Title != "刘晓燕" {
		t.Fatalf("ranked = %+v", ranked)
	}
}
