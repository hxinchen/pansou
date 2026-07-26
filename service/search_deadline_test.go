package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"pansou/config"
	"pansou/model"
	"pansou/plugin"
)

type timedSearchPlugin struct {
	name  string
	delay time.Duration
}

func (p *timedSearchPlugin) Name() string           { return p.name }
func (*timedSearchPlugin) Priority() int            { return 1 }
func (*timedSearchPlugin) SkipServiceFilter() bool  { return false }
func (*timedSearchPlugin) SetMainCacheKey(string)   {}
func (*timedSearchPlugin) SetCurrentKeyword(string) {}
func (*timedSearchPlugin) Search(string, map[string]interface{}) ([]model.SearchResult, error) {
	return nil, nil
}
func (*timedSearchPlugin) AsyncSearch(string, func(*http.Client, string, map[string]interface{}) ([]model.SearchResult, error), string, map[string]interface{}) ([]model.SearchResult, error) {
	return nil, nil
}
func (p *timedSearchPlugin) SearchWithResult(keyword string, _ map[string]interface{}) (model.PluginSearchResult, error) {
	time.Sleep(p.delay)
	return model.PluginSearchResult{
		Results: []model.SearchResult{{
			UniqueID: p.name, Channel: "plugin:" + p.name, Datetime: time.Now(),
			Title: keyword + " " + p.name,
			Links: []model.Link{{Type: "quark", URL: "https://pan.quark.cn/s/" + p.name}},
		}},
		IsFinal: true,
	}, nil
}

func TestInteractiveDeadlineDiscardsLateResultsButCollectorWaits(t *testing.T) {
	previous := config.AppConfig
	config.AppConfig = &config.Config{
		AsyncPluginEnabled: true, DefaultConcurrency: 2, PluginTimeout: 500 * time.Millisecond,
		SearchSchedulerEnabled: false,
	}
	ResetSearchScheduler()
	defer func() {
		config.AppConfig = previous
		ResetSearchScheduler()
	}()

	manager := plugin.NewPluginManager()
	manager.RegisterPlugin(&timedSearchPlugin{name: "fast", delay: 5 * time.Millisecond})
	manager.RegisterPlugin(&timedSearchPlugin{name: "slow", delay: 120 * time.Millisecond})

	interactive := NewSearchService(manager)
	started := time.Now()
	partial, err := interactive.SearchContext(context.Background(), ContextSearchRequest{
		Keyword: "sample", Channels: []string{}, Concurrency: 2, ForceRefresh: true,
		ResultType: "all", SourceType: "plugin", Plugins: []string{"fast", "slow"},
		Identity: SearchIdentity{Actor: SearchActorUser}, ExecutionBudget: 30 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed >= 100*time.Millisecond {
		t.Fatalf("interactive search waited for late source: %s", elapsed)
	}
	if partial.Completion != model.SearchCompletionPartial || partial.StopReason != model.SearchStopReasonDeadline {
		t.Fatalf("interactive response = %+v", partial)
	}
	if len(partial.Results) != 1 || partial.Results[0].UniqueID != "fast" {
		t.Fatalf("interactive results = %+v", partial.Results)
	}
	if partial.Execution == nil || partial.Execution.Completed != 1 || partial.Execution.Cancelled != 1 {
		t.Fatalf("interactive execution = %+v", partial.Execution)
	}

	collector := NewSearchService(manager)
	complete, err := collector.SearchContext(context.Background(), ContextSearchRequest{
		Keyword: "sample", Channels: []string{}, Concurrency: 2, ForceRefresh: true,
		ResultType: "all", SourceType: "plugin", Plugins: []string{"fast", "slow"},
		Identity: SearchIdentity{Actor: SearchActorCollector},
	})
	if err != nil {
		t.Fatal(err)
	}
	if complete.Completion != model.SearchCompletionComplete || complete.StopReason != "" {
		t.Fatalf("collector response = %+v", complete)
	}
	if len(complete.Results) != 2 || complete.Execution == nil || complete.Execution.Cancelled != 0 {
		t.Fatalf("collector results = %+v, execution = %+v", complete.Results, complete.Execution)
	}
}

var _ plugin.AsyncSearchPlugin = (*timedSearchPlugin)(nil)
