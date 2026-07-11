package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"pansou/config"
	"pansou/plugin"
	"pansou/sourceconfig"
	"pansou/storage"
)

func bootstrapSourceRuntime(ctx context.Context, store *storage.Store) (*sourceconfig.Service, error) {
	if store == nil {
		return nil, nil
	}
	names := plugin.GetRegisteredPluginNames()
	catalog, err := sourceconfig.DefaultCatalog(names)
	if err != nil {
		return nil, err
	}
	seed := sourceSeedConfig(names)
	payload, err := json.Marshal(seed)
	if err != nil {
		return nil, fmt.Errorf("encode initial source config: %w", err)
	}
	record, _, err := store.InitializeSearchSourceConfig(ctx, storage.InitializeSearchSourceConfigInput{
		SchemaVersion: seed.SchemaVersion,
		Config:        payload,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize search source config: %w", err)
	}
	var storedConfig sourceconfig.Config
	if err := json.Unmarshal(record.Config, &storedConfig); err != nil {
		return nil, fmt.Errorf("decode stored source config: %w", err)
	}
	storedConfig, err = catalog.Validate(storedConfig)
	if err != nil {
		return nil, err
	}
	builder := func(ctx context.Context, version int64, candidate sourceconfig.Config) (*sourceconfig.Snapshot, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		manager := plugin.NewPluginManager()
		for _, name := range enabledPluginNames(candidate) {
			instance, createErr := plugin.CreateRegisteredPlugin(name)
			if createErr != nil { _ = manager.Close(); return nil, createErr }
			if managed, ok := instance.(plugin.ManagedCredentialPlugin); ok { managed.SetManagedCredentialMode(true) }
			if configurable, ok := instance.(plugin.RuntimeConfigurablePlugin); ok {
				if applyErr := configurable.ApplyRuntimeConfig(candidate.Plugins[name].Config); applyErr != nil { _ = manager.Close(); return nil, fmt.Errorf("configure plugin %s: %w", name, applyErr) }
			}
			if registerErr := manager.RegisterPluginChecked(instance); registerErr != nil { _ = manager.Close(); return nil, registerErr }
		}
		return sourceconfig.NewSnapshot(version, candidate, manager), nil
	}
	initial, err := builder(ctx, record.Version, storedConfig)
	if err != nil {
		return nil, fmt.Errorf("build initial source snapshot: %w", err)
	}
	manager := sourceconfig.NewManager(initial, builder)
	return &sourceconfig.Service{Store: store, Catalog: catalog, Manager: manager}, nil
}

func sourceSeedConfig(names []string) sourceconfig.Config {
	enabled := make(map[string]struct{}, len(config.AppConfig.EnabledPlugins))
	for _, name := range config.AppConfig.EnabledPlugins {
		enabled[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}
	plugins := make(map[string]sourceconfig.PluginConfig, len(names))
	for index, name := range names {
		_, isEnabled := enabled[strings.ToLower(name)]
		plugins[name] = sourceconfig.PluginConfig{Enabled: config.AppConfig.AsyncPluginEnabled && isEnabled, Order: index}
	}
	channels := make([]sourceconfig.Channel, 0, len(config.AppConfig.DefaultChannels))
	for index, channel := range config.AppConfig.DefaultChannels {
		channels = append(channels, sourceconfig.Channel{Key: channel, DisplayName: channel, Enabled: true, Order: index})
	}
	return sourceconfig.Config{SchemaVersion: 1, AsyncPluginsEnabled: config.AppConfig.AsyncPluginEnabled, Channels: channels, Plugins: plugins}
}

func enabledPluginNames(config sourceconfig.Config) []string {
	if !config.AsyncPluginsEnabled {
		return nil
	}
	type entry struct {
		key   string
		order int
	}
	items := make([]entry, 0, len(config.Plugins))
	for key, settings := range config.Plugins {
		if settings.Enabled {
			items = append(items, entry{key: key, order: settings.Order})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].order == items[j].order {
			return items[i].key < items[j].key
		}
		return items[i].order < items[j].order
	})
	result := make([]string, len(items))
	for index, item := range items {
		result[index] = item.key
	}
	return result
}
