package sourceconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"pansou/storage"
)

type Service struct {
	Store   *storage.Store
	Catalog *Catalog
	Manager *Manager
}

type CurrentState struct {
	Version   int64          `json:"version"`
	Config    Config         `json:"config"`
	UpdatedAt time.Time      `json:"updated_at"`
	Snapshot  SnapshotStatus `json:"snapshot"`
}

type SnapshotStatus struct {
	Status      string   `json:"status"`
	Version     int64    `json:"version"`
	Channels    int      `json:"channels"`
	Plugins     int      `json:"plugins"`
	PluginNames []string `json:"plugin_names"`
}

func (s *Service) Current(ctx context.Context) (CurrentState, error) {
	if s == nil || s.Store == nil || s.Catalog == nil || s.Manager == nil {
		return CurrentState{}, errors.New("source config service is unavailable")
	}
	record, err := s.Store.GetSearchSourceConfig(ctx)
	if err != nil {
		return CurrentState{}, err
	}
	var config Config
	if err := json.Unmarshal(record.Config, &config); err != nil {
		return CurrentState{}, fmt.Errorf("decode search source config: %w", err)
	}
	config, err = s.Catalog.Validate(config)
	if err != nil {
		return CurrentState{}, err
	}
	lease, err := s.Manager.Acquire()
	if err != nil {
		return CurrentState{}, err
	}
	defer lease.Release()
	snapshot := lease.Snapshot()
	status := SnapshotStatus{Status: "healthy"}
	if snapshot != nil {
		status.Version = snapshot.Version
		status.Channels = len(snapshot.Channels())
		status.PluginNames = snapshot.PluginNames()
		status.Plugins = len(status.PluginNames)
	}
	return CurrentState{Version: record.Version, Config: config, UpdatedAt: record.UpdatedAt, Snapshot: status}, nil
}

func (s *Service) Validate(config Config) (Config, error) {
	if s == nil || s.Catalog == nil {
		return Config{}, errors.New("source catalog is unavailable")
	}
	return s.Catalog.Validate(config)
}

func (s *Service) Update(ctx context.Context, expectedVersion int64, config Config, actorUserID *int64) (CurrentState, error) {
	if s == nil || s.Store == nil || s.Catalog == nil || s.Manager == nil {
		return CurrentState{}, errors.New("source config service is unavailable")
	}
	releaseUpdate := s.Manager.UpdateLock()
	defer releaseUpdate()
	current, err := s.Store.GetSearchSourceConfig(ctx)
	if err != nil {
		return CurrentState{}, err
	}
	if current.Version != expectedVersion {
		return CurrentState{}, storage.ErrConflict
	}
	validated, err := s.Catalog.Validate(config)
	if err != nil {
		return CurrentState{}, err
	}
	candidate, err := s.Manager.BuildCandidate(ctx, expectedVersion+1, validated)
	if err != nil {
		_, _ = s.Store.RecordSearchSourceConfigEvent(ctx, storage.CreateSearchSourceConfigEventInput{
			ActorUserID: actorUserID, BaseVersion: expectedVersion, Result: storage.SourceConfigEventFailed,
			ErrorCode: "source_init_failed", ChangeSummary: configSummary(validated),
		})
		return CurrentState{}, fmt.Errorf("build source snapshot: %w", err)
	}
	discard := true
	defer func() {
		if discard && candidate != nil && candidate.PluginManager != nil {
			_ = candidate.PluginManager.Close()
		}
	}()
	payload, err := json.Marshal(validated)
	if err != nil {
		return CurrentState{}, fmt.Errorf("encode search source config: %w", err)
	}
	record, err := s.Store.CompareAndSwapSearchSourceConfig(ctx, storage.UpdateSearchSourceConfigInput{
		ExpectedVersion: expectedVersion, SchemaVersion: validated.SchemaVersion, Config: payload,
		UpdatedBy: actorUserID, ChangeSummary: configSummary(validated),
	})
	if err != nil {
		return CurrentState{}, err
	}
	if err := s.Manager.Publish(candidate); err != nil {
		return CurrentState{}, err
	}
	discard = false
	return CurrentState{
		Version: record.Version, Config: validated, UpdatedAt: record.UpdatedAt,
		Snapshot: SnapshotStatus{Status: "healthy", Version: candidate.Version, Channels: len(candidate.Channels()), Plugins: len(candidate.PluginNames()), PluginNames: candidate.PluginNames()},
	}, nil
}

func configSummary(config Config) map[string]any {
	enabledChannels := 0
	for _, channel := range config.Channels {
		if channel.Enabled {
			enabledChannels++
		}
	}
	enabledPlugins := 0
	for _, settings := range config.Plugins {
		if settings.Enabled {
			enabledPlugins++
		}
	}
	return map[string]any{"channels": enabledChannels, "plugins": enabledPlugins, "plugin_master_enabled": config.AsyncPluginsEnabled}
}
