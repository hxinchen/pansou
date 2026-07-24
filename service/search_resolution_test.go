package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"pansou/config"
	"pansou/plugin"
	"pansou/sourceconfig"
	"pansou/tgchannel"
	"pansou/util/pool"
)

func TestResolveManagedSearchRequest(t *testing.T) {
	snapshot := sourceconfig.NewSnapshot(1, sourceconfig.Config{
		AsyncPluginsEnabled: true,
		Channels: []sourceconfig.Channel{
			{Key: "public_one", Enabled: true, Order: 0},
			{Key: "disabled_one", Enabled: false, Order: 1},
		},
		Plugins: map[string]sourceconfig.PluginConfig{
			"plugin_one": {Enabled: true},
			"plugin_off": {Enabled: false},
		},
	}, plugin.NewPluginManager())

	resolved, err := resolveManagedSearchRequest(ContextSearchRequest{}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resolved.Channels, []string{"public_one"}) {
		t.Fatalf("default channels = %v", resolved.Channels)
	}
	if !reflect.DeepEqual(resolved.Plugins, []string{"plugin_one"}) {
		t.Fatalf("default plugins = %v", resolved.Plugins)
	}
	if resolved.requiresLiveTG {
		t.Fatal("managed defaults should remain database-first")
	}

	resolved, err = resolveManagedSearchRequest(ContextSearchRequest{
		Channels: []string{"@Custom_Channel", "https://t.me/custom_channel"},
		Plugins:  []string{"PLUGIN_ONE", "plugin_off", "missing"},
	}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resolved.Channels, []string{"custom_channel"}) {
		t.Fatalf("explicit channels = %v", resolved.Channels)
	}
	if !reflect.DeepEqual(resolved.Plugins, []string{"plugin_one"}) {
		t.Fatalf("explicit plugins = %v", resolved.Plugins)
	}
	if !resolved.requiresLiveTG {
		t.Fatal("custom channel should require live TG search")
	}

	publicOnly, err := resolveManagedSearchRequest(ContextSearchRequest{
		Channels: []string{"PUBLIC_ONE"},
	}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if publicOnly.requiresLiveTG {
		t.Fatal("explicit public-only selection should remain database-first")
	}
}

func TestResolveManagedSearchRequestKeepsAllChannelsUntilTieredRollout(t *testing.T) {
	previous := config.AppConfig
	defer func() { config.AppConfig = previous }()
	channels := make([]sourceconfig.Channel, 35)
	for index := range channels {
		channels[index] = sourceconfig.Channel{Key: fmt.Sprintf("channel_%02d", index), Enabled: true}
	}
	snapshot := sourceconfig.NewSnapshot(1, sourceconfig.Config{Channels: channels}, plugin.NewPluginManager())
	config.AppConfig = &config.Config{SearchTieredRollout: false}
	compatible, err := resolveManagedSearchRequest(ContextSearchRequest{}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(compatible.Channels) != 35 {
		t.Fatalf("compatible channels=%d", len(compatible.Channels))
	}
	config.AppConfig.SearchTieredRollout = true
	tiered, err := resolveManagedSearchRequest(ContextSearchRequest{}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(tiered.Channels) != 30 {
		t.Fatalf("tiered channels=%d", len(tiered.Channels))
	}
}

func TestResolveManagedSearchRequestPreservesExplicitEmptyLists(t *testing.T) {
	snapshot := sourceconfig.NewSnapshot(1, sourceconfig.Config{
		AsyncPluginsEnabled: true,
		Channels:            []sourceconfig.Channel{{Key: "public_one", Enabled: true}},
		Plugins:             map[string]sourceconfig.PluginConfig{"plugin_one": {Enabled: true}},
	}, plugin.NewPluginManager())

	resolved, err := resolveManagedSearchRequest(ContextSearchRequest{
		Channels: []string{}, Plugins: []string{},
	}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Channels == nil || len(resolved.Channels) != 0 {
		t.Fatalf("explicit empty channels = %#v", resolved.Channels)
	}
	if resolved.Plugins == nil || len(resolved.Plugins) != 0 {
		t.Fatalf("explicit empty plugins = %#v", resolved.Plugins)
	}
}

func TestResolveManagedSearchRequestHonorsSourceType(t *testing.T) {
	snapshot := sourceconfig.NewSnapshot(1, sourceconfig.Config{
		AsyncPluginsEnabled: true,
		Channels:            []sourceconfig.Channel{{Key: "public_one", Enabled: true}},
		Plugins:             map[string]sourceconfig.PluginConfig{"plugin_one": {Enabled: true}},
	}, plugin.NewPluginManager())

	tgOnly, err := resolveManagedSearchRequest(ContextSearchRequest{SourceType: "tg"}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tgOnly.Channels, []string{"public_one"}) || tgOnly.Plugins == nil || len(tgOnly.Plugins) != 0 {
		t.Fatalf("tg-only sources = channels:%v plugins:%#v", tgOnly.Channels, tgOnly.Plugins)
	}

	pluginOnly, err := resolveManagedSearchRequest(ContextSearchRequest{SourceType: "plugin"}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if pluginOnly.Channels == nil || len(pluginOnly.Channels) != 0 || !reflect.DeepEqual(pluginOnly.Plugins, []string{"plugin_one"}) {
		t.Fatalf("plugin-only sources = channels:%#v plugins:%v", pluginOnly.Channels, pluginOnly.Plugins)
	}
}

func TestDynamicResolverUsesManagedDefaults(t *testing.T) {
	snapshot := sourceconfig.NewSnapshot(1, sourceconfig.Config{
		Channels: []sourceconfig.Channel{{Key: "public_one", Enabled: true}},
	}, plugin.NewPluginManager())
	manager := sourceconfig.NewManager(snapshot, nil)
	service := NewDynamicSearchService(manager)

	resolved, err := service.ResolveSearchRequest(context.Background(), ContextSearchRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resolved.Channels, []string{"public_one"}) {
		t.Fatalf("channels = %v", resolved.Channels)
	}
}

func TestTGSearchWorkerCount(t *testing.T) {
	for input, want := range map[int]int{0: 0, 1: 1, 20: 20, 21: 20, 500: 20} {
		if got := tgSearchWorkerCount(input); got != want {
			t.Fatalf("tgSearchWorkerCount(%d) = %d, want %d", input, got, want)
		}
	}
}

func TestExecuteTGTasksWithTimeoutRunsAllTasksWithinWorkerLimit(t *testing.T) {
	const taskCount = 120
	var executed atomic.Int32
	var active atomic.Int32
	var peak atomic.Int32
	tasks := make([]pool.Task, 0, taskCount)
	for index := 0; index < taskCount; index++ {
		value := index
		tasks = append(tasks, func() interface{} {
			current := active.Add(1)
			for {
				previous := peak.Load()
				if current <= previous || peak.CompareAndSwap(previous, current) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			active.Add(-1)
			executed.Add(1)
			return value
		})
	}

	results := executeTGTasksWithTimeout(tasks, maxTGSearchWorkers, 5*time.Second)
	if got := executed.Load(); got != taskCount {
		t.Fatalf("executed tasks = %d, want %d", got, taskCount)
	}
	if len(results) != taskCount {
		t.Fatalf("results = %d, want %d", len(results), taskCount)
	}
	if got := peak.Load(); got > maxTGSearchWorkers {
		t.Fatalf("peak concurrency = %d, max %d", got, maxTGSearchWorkers)
	}
}

func TestLiveSearchRejectsInvalidExplicitChannel(t *testing.T) {
	service := NewSearchService(plugin.NewPluginManager())
	_, err := service.SearchContext(context.Background(), ContextSearchRequest{
		Keyword: "sample", Channels: []string{"bad-channel"}, SourceType: "tg",
	})
	if !errors.Is(err, tgchannel.ErrInvalidChannel) {
		t.Fatalf("SearchContext error = %v, want ErrInvalidChannel", err)
	}
}
