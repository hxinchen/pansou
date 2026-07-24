package service

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pansou/config"
	"pansou/model"
	"pansou/plugin"
	searchscheduler "pansou/service/scheduler"
)

func TestLazyAdmissionOnlyAcquiresWhenEnsured(t *testing.T) {
	previous := config.AppConfig
	config.AppConfig = &config.Config{SearchSchedulerEnabled: true, SearchActiveLimit: 1, SearchQueueSize: 1, SearchTGWorkers: 1, SearchPluginWorkers: 1, SearchCredentialWorkers: 1, SearchPerSourceLimit: 1, SearchCircuitFailures: 2, SearchCircuitCooldown: time.Second}
	ResetSearchScheduler()
	defer func() { config.AppConfig = previous; ResetSearchScheduler() }()

	ctx, lazy := contextWithLazySearchAdmission(context.Background(), ContextSearchRequest{})
	if got := GlobalSearchScheduler().Snapshot().TotalSearches; got != 0 {
		t.Fatalf("before ensure=%d", got)
	}
	if err := ensureLiveSearchAdmission(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ensureLiveSearchAdmission(ctx); err != nil {
		t.Fatal(err)
	}
	if got := GlobalSearchScheduler().Snapshot().TotalSearches; got != 1 {
		t.Fatalf("after ensure=%d", got)
	}
	lazy.Release()
	if got := GlobalSearchScheduler().Snapshot().Active; got != 0 {
		t.Fatalf("active=%d", got)
	}
}

type tieredTestPlugin struct {
	name  string
	links int
	calls *atomic.Int64
	delay time.Duration
}

func (p *tieredTestPlugin) Name() string           { return p.name }
func (*tieredTestPlugin) Priority() int            { return 1 }
func (*tieredTestPlugin) SkipServiceFilter() bool  { return false }
func (*tieredTestPlugin) SetMainCacheKey(string)   {}
func (*tieredTestPlugin) SetCurrentKeyword(string) {}
func (p *tieredTestPlugin) Search(string, map[string]interface{}) ([]model.SearchResult, error) {
	p.calls.Add(1)
	if p.delay > 0 {
		time.Sleep(p.delay)
	}
	links := make([]model.Link, p.links)
	for i := range links {
		links[i].URL = fmt.Sprintf("https://example.test/%s/%d", p.name, i)
	}
	if len(links) == 0 {
		return nil, nil
	}
	return []model.SearchResult{{UniqueID: p.name, Links: links}}, nil
}
func (*tieredTestPlugin) AsyncSearch(string, func(*http.Client, string, map[string]interface{}) ([]model.SearchResult, error), string, map[string]interface{}) ([]model.SearchResult, error) {
	return nil, nil
}

func TestTieredPluginSearchIsPolicyCompleteAndReportsDeferredSources(t *testing.T) {
	previous := config.AppConfig
	config.AppConfig = &config.Config{AsyncPluginEnabled: true, DefaultConcurrency: 16, PluginTimeout: time.Second, SearchSchedulerEnabled: false, SearchPerRequestPlugin: 16, SearchTieredRollout: true}
	ResetSearchScheduler()
	defer func() { config.AppConfig = previous; ResetSearchScheduler() }()
	manager := plugin.NewPluginManager()
	var calls atomic.Int64
	for i := 0; i < 16; i++ {
		links := 0
		if i == 0 {
			links = 30
		}
		manager.RegisterPlugin(&tieredTestPlugin{name: fmt.Sprintf("p%02d", i), links: links, calls: &calls})
	}
	search := NewSearchService(manager)
	response, err := search.Search("sample", []string{}, 16, true, "all", "plugin", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Completion != model.SearchCompletionComplete {
		t.Fatalf("completion=%s", response.Completion)
	}
	if response.Execution == nil || response.Execution.Requested != 16 || response.Execution.Executed != 15 || response.Execution.Deferred != 1 || response.Execution.Strategy != "tiered" {
		t.Fatalf("execution=%#v", response.Execution)
	}
	if calls.Load() != 15 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestPluginSearchKeepsFullCoverageWhenTieredRolloutIsDisabled(t *testing.T) {
	previous := config.AppConfig
	config.AppConfig = &config.Config{AsyncPluginEnabled: true, DefaultConcurrency: 16, PluginTimeout: time.Second, SearchSchedulerEnabled: false, SearchPerRequestPlugin: 16, SearchTieredRollout: false}
	ResetSearchScheduler()
	defer func() { config.AppConfig = previous; ResetSearchScheduler() }()
	manager := plugin.NewPluginManager()
	var calls atomic.Int64
	for i := 0; i < 16; i++ {
		links := 0
		if i == 0 {
			links = 30
		}
		manager.RegisterPlugin(&tieredTestPlugin{name: fmt.Sprintf("compat-p%02d", i), links: links, calls: &calls})
	}
	search := NewSearchService(manager)
	response, err := search.Search("sample", []string{}, 16, true, "all", "plugin", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Execution == nil || response.Execution.Executed != 16 || response.Execution.Deferred != 0 || response.Execution.Strategy != "all-sources" {
		t.Fatalf("execution=%#v", response.Execution)
	}
	if calls.Load() != 16 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

var _ plugin.AsyncSearchPlugin = (*tieredTestPlugin)(nil)

func TestTwentyDistinctColdSearchesRespectGlobalFanoutBudgets(t *testing.T) {
	previous := config.AppConfig
	config.AppConfig = &config.Config{
		AsyncPluginEnabled: true, DefaultConcurrency: 16, PluginTimeout: 5 * time.Second,
		SearchSchedulerEnabled: true, SearchActiveLimit: 8, SearchQueueSize: 100,
		SearchTGWorkers: 32, SearchPluginWorkers: 32, SearchCredentialWorkers: 16,
		SearchPerRequestTG: 20, SearchPerRequestPlugin: 16, SearchPerSourceLimit: 2,
		SearchCircuitFailures: 5, SearchCircuitCooldown: time.Minute, TGSearchWorkers: 20,
	}
	ResetSearchScheduler()
	defer func() { config.AppConfig = previous; ResetSearchScheduler() }()
	manager := plugin.NewPluginManager()
	var pluginCalls atomic.Int64
	for i := 0; i < 8; i++ {
		manager.RegisterPlugin(&tieredTestPlugin{name: fmt.Sprintf("load-p%02d", i), calls: &pluginCalls, delay: 5 * time.Millisecond})
	}
	search := NewSearchService(manager)
	var tgCalls atomic.Int64
	search.channelSearch = func(ctx context.Context, _, _ string) ([]model.SearchResult, error) {
		tgCalls.Add(1)
		select {
		case <-time.After(5 * time.Millisecond):
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	channels := make([]string, 30)
	for i := range channels {
		channels[i] = fmt.Sprintf("load_channel_%02d", i)
	}
	var wg sync.WaitGroup
	errorsCh := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			response, err := search.SearchContext(context.Background(), ContextSearchRequest{Keyword: fmt.Sprintf("cold-%02d", index), Channels: channels, ForceRefresh: true, ResultType: "all", SourceType: "all", Identity: SearchIdentity{Actor: SearchActorUser, UserID: int64(index + 1)}})
			if err != nil {
				errorsCh <- err
				return
			}
			if response.Completion != model.SearchCompletionComplete {
				errorsCh <- fmt.Errorf("completion=%s", response.Completion)
			}
		}(i)
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	snapshot := GlobalSearchScheduler().Snapshot()
	if snapshot.TotalSearches != 20 || snapshot.PeakActive != 8 || snapshot.RejectedSearches != 0 {
		t.Fatalf("scheduler=%#v", snapshot)
	}
	if snapshot.Classes[string(searchscheduler.ClassTG)].Runs != 600 || snapshot.Classes[string(searchscheduler.ClassPlugin)].Runs != 160 {
		t.Fatalf("classes=%#v", snapshot.Classes)
	}
	if tgCalls.Load() != 600 || pluginCalls.Load() != 160 {
		t.Fatalf("tg=%d plugin=%d", tgCalls.Load(), pluginCalls.Load())
	}
}
