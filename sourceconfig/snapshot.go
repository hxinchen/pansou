package sourceconfig

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"

	"pansou/plugin"
)

type Snapshot struct {
	Version       int64
	Config        Config
	PluginManager *plugin.PluginManager

	refs    atomic.Int64
	retired atomic.Bool
	closed  atomic.Bool
	done    chan struct{}
}

func NewSnapshot(version int64, config Config, manager *plugin.PluginManager) *Snapshot {
	return &Snapshot{Version: version, Config: cloneConfig(config), PluginManager: manager, done: make(chan struct{})}
}

func (s *Snapshot) Channels() []string {
	if s == nil {
		return nil
	}
	channels := make([]string, 0, len(s.Config.Channels))
	for _, channel := range s.Config.Channels {
		if channel.Enabled {
			channels = append(channels, channel.Key)
		}
	}
	return channels
}

func (s *Snapshot) PluginNames() []string {
	if s == nil || !s.Config.AsyncPluginsEnabled {
		return nil
	}
	type ordered struct {
		name  string
		order int
	}
	items := make([]ordered, 0, len(s.Config.Plugins))
	for name, settings := range s.Config.Plugins {
		if settings.Enabled {
			items = append(items, ordered{name, settings.Order})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].order == items[j].order {
			return items[i].name < items[j].name
		}
		return items[i].order < items[j].order
	})
	result := make([]string, len(items))
	for index, item := range items {
		result[index] = item.name
	}
	return result
}

func (s *Snapshot) close() {
	if s == nil || !s.closed.CompareAndSwap(false, true) {
		return
	}
	if s.PluginManager != nil {
		_ = s.PluginManager.Close()
	}
	close(s.done)
}

type Builder func(context.Context, int64, Config) (*Snapshot, error)

type Manager struct {
	current atomic.Pointer[Snapshot]
	update  sync.Mutex
	closed  atomic.Bool
	build   Builder
}

func NewManager(initial *Snapshot, builder Builder) *Manager {
	manager := &Manager{build: builder}
	if initial != nil {
		manager.current.Store(initial)
	}
	return manager
}

type Lease struct {
	snapshot *Snapshot
	once     sync.Once
}

func (m *Manager) Acquire() (*Lease, error) {
	if m == nil || m.closed.Load() {
		return nil, ErrClosed
	}
	for {
		snapshot := m.current.Load()
		if snapshot == nil {
			return nil, errors.New("source snapshot is unavailable")
		}
		snapshot.refs.Add(1)
		if !snapshot.closed.Load() {
			return &Lease{snapshot: snapshot}, nil
		}
		if snapshot.refs.Add(-1) == 0 && snapshot.retired.Load() {
			snapshot.close()
		}
	}
}

func (l *Lease) Snapshot() *Snapshot {
	if l == nil {
		return nil
	}
	return l.snapshot
}

func (l *Lease) Release() {
	if l == nil || l.snapshot == nil {
		return
	}
	l.once.Do(func() {
		if l.snapshot.refs.Add(-1) == 0 && l.snapshot.retired.Load() {
			l.snapshot.close()
		}
	})
}

func (m *Manager) BuildCandidate(ctx context.Context, version int64, config Config) (*Snapshot, error) {
	if m == nil || m.build == nil {
		return nil, errors.New("source snapshot builder is unavailable")
	}
	if m.closed.Load() {
		return nil, ErrClosed
	}
	return m.build(ctx, version, cloneConfig(config))
}

func (m *Manager) UpdateLock() func() {
	m.update.Lock()
	return m.update.Unlock
}

func (m *Manager) Publish(candidate *Snapshot) error {
	if candidate == nil {
		return errors.New("candidate snapshot is nil")
	}
	if m == nil || m.closed.Load() {
		candidate.close()
		return ErrClosed
	}
	old := m.current.Swap(candidate)
	retire(old)
	return nil
}

func (m *Manager) Close(ctx context.Context) error {
	if m == nil || !m.closed.CompareAndSwap(false, true) {
		return nil
	}
	current := m.current.Swap(nil)
	retire(current)
	if current == nil {
		return nil
	}
	select {
	case <-current.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func retire(snapshot *Snapshot) {
	if snapshot == nil {
		return
	}
	snapshot.retired.Store(true)
	if snapshot.refs.Load() == 0 {
		snapshot.close()
	}
}

func cloneConfig(config Config) Config {
	copyConfig := config
	copyConfig.Channels = append([]Channel(nil), config.Channels...)
	copyConfig.Plugins = make(map[string]PluginConfig, len(config.Plugins))
	for name, settings := range config.Plugins {
		copySettings := settings
		copySettings.Config = make(map[string]any, len(settings.Config))
		for key, value := range settings.Config {
			copySettings.Config[key] = value
		}
		copyConfig.Plugins[name] = copySettings
	}
	return copyConfig
}
