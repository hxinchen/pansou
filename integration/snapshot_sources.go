package integration

import (
	"context"
	"errors"

	"pansou/collection"
	"pansou/credential"
	"pansou/service"
	"pansou/sourceconfig"
)

type SnapshotSources struct {
	Manager     *sourceconfig.Manager
	Credentials *credential.Service
}

func (s SnapshotSources) Sources(ctx context.Context, keyword collection.Keyword) ([]collection.Source, error) {
	lease, err := s.AcquireSources(ctx, keyword)
	if err != nil {
		return nil, err
	}
	defer lease.Release()
	return lease.Sources(), nil
}

func (s SnapshotSources) AcquireSources(context.Context, collection.Keyword) (collection.SourceLease, error) {
	if s.Manager == nil {
		return nil, errors.New("source snapshot manager is unavailable")
	}
	lease, err := s.Manager.Acquire()
	if err != nil {
		return nil, err
	}
	snapshot := lease.Snapshot()
	if snapshot == nil || snapshot.PluginManager == nil {
		lease.Release()
		return nil, errors.New("source snapshot is incomplete")
	}
	sources := make([]collection.Source, 0, len(snapshot.Config.Channels)+len(snapshot.Config.Plugins))
	for _, channel := range snapshot.Channels() {
		sources = append(sources, collection.Source{Key: "tg:" + channel, Type: "tg", Channels: []string{channel}, Concurrency: 1, ResultType: "all"})
	}
	for _, name := range snapshot.PluginNames() {
		sources = append(sources, collection.Source{Key: "plugin:" + name, Type: "plugin", Plugins: []string{name}, Concurrency: 1, ResultType: "all"})
	}
	searchService := service.NewSearchService(snapshot.PluginManager)
	searchService.SetCredentialService(s.Credentials)
	return &snapshotSourceLease{
		lease:    lease,
		items:    sources,
		searcher: NewLiveSearcher(searchService),
	}, nil
}

type snapshotSourceLease struct {
	lease    *sourceconfig.Lease
	items    []collection.Source
	searcher collection.LiveSearcher
}

func (l *snapshotSourceLease) Sources() []collection.Source {
	return append([]collection.Source(nil), l.items...)
}
func (l *snapshotSourceLease) Searcher() collection.LiveSearcher { return l.searcher }
func (l *snapshotSourceLease) Release()                          { l.lease.Release() }
