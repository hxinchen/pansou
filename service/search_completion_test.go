package service

import (
	"net/http"
	"testing"
	"time"

	"pansou/config"
	"pansou/model"
	"pansou/plugin"
)

type partialResultPlugin struct{}

func (*partialResultPlugin) Name() string             { return "slow" }
func (*partialResultPlugin) Priority() int            { return 1 }
func (*partialResultPlugin) SkipServiceFilter() bool  { return false }
func (*partialResultPlugin) SetMainCacheKey(string)   {}
func (*partialResultPlugin) SetCurrentKeyword(string) {}
func (*partialResultPlugin) Search(string, map[string]interface{}) ([]model.SearchResult, error) {
	return nil, nil
}
func (*partialResultPlugin) AsyncSearch(string, func(*http.Client, string, map[string]interface{}) ([]model.SearchResult, error), string, map[string]interface{}) ([]model.SearchResult, error) {
	return nil, nil
}
func (*partialResultPlugin) SearchWithResult(string, map[string]interface{}) (model.PluginSearchResult, error) {
	return model.PluginSearchResult{
		Results: []model.SearchResult{{UniqueID: "slow-1", Links: []model.Link{{URL: "https://example.com/1"}}}},
		IsFinal: false,
	}, nil
}

func TestPluginPartialResultPropagatesToSearchResponse(t *testing.T) {
	previous := config.AppConfig
	config.AppConfig = &config.Config{
		AsyncPluginEnabled: true, DefaultConcurrency: 1, PluginTimeout: time.Second,
	}
	defer func() { config.AppConfig = previous }()

	manager := plugin.NewPluginManager()
	manager.RegisterPlugin(&partialResultPlugin{})
	search := NewSearchService(manager)
	response, err := search.Search("sample", []string{}, 1, true, "all", "plugin", []string{"slow"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Completion != model.SearchCompletionPartial {
		t.Fatalf("completion = %q", response.Completion)
	}
	if len(response.PartialSources) != 1 || response.PartialSources[0] != "plugin:slow" {
		t.Fatalf("partial sources = %#v", response.PartialSources)
	}
}

var _ plugin.AsyncSearchPlugin = (*partialResultPlugin)(nil)
