package service

import (
	"context"
	"sync"
	"time"

	"pansou/config"
	searchscheduler "pansou/service/scheduler"
	"pansou/sourceconfig"
)

var (
	searchSchedulerMu sync.Mutex
	searchScheduler   *searchscheduler.Scheduler
)

type lazyAdmissionContextKey struct{}

type lazySearchAdmission struct {
	ctx       context.Context
	request   ContextSearchRequest
	once      sync.Once
	admission *searchscheduler.Admission
	err       error
	release   sync.Once
}

func GlobalSearchScheduler() *searchscheduler.Scheduler {
	searchSchedulerMu.Lock()
	defer searchSchedulerMu.Unlock()
	if searchScheduler == nil {
		cfg := searchscheduler.Config{}
		if config.AppConfig != nil {
			cfg = searchscheduler.Config{
				Enabled:           config.AppConfig.SearchSchedulerEnabled,
				ActiveSearches:    config.AppConfig.SearchActiveLimit,
				SearchQueue:       config.AppConfig.SearchQueueSize,
				TGWorkers:         config.AppConfig.SearchTGWorkers,
				PluginWorkers:     config.AppConfig.SearchPluginWorkers,
				CredentialWorkers: config.AppConfig.SearchCredentialWorkers,
				PerSource:         config.AppConfig.SearchPerSourceLimit,
				CircuitFailures:   config.AppConfig.SearchCircuitFailures,
				CircuitCooldown:   config.AppConfig.SearchCircuitCooldown,
			}
		}
		searchScheduler = searchscheduler.New(cfg)
	}
	return searchScheduler
}

func ResetSearchScheduler() {
	searchSchedulerMu.Lock()
	searchScheduler = nil
	searchSchedulerMu.Unlock()
}

func searchPriority(request ContextSearchRequest) searchscheduler.Priority {
	if request.Ext != nil {
		if value, ok := request.Ext["search_priority"].(string); ok && value == "background" {
			return searchscheduler.PriorityBackground
		}
	}
	if request.Identity.Actor == SearchActorCollector {
		return searchscheduler.PriorityCollection
	}
	return searchscheduler.PriorityInteractive
}

func acquireLiveSearch(ctx context.Context, request ContextSearchRequest) (*searchscheduler.Admission, error) {
	return GlobalSearchScheduler().Acquire(ctx, searchPriority(request))
}

func contextWithLazySearchAdmission(ctx context.Context, request ContextSearchRequest) (context.Context, *lazySearchAdmission) {
	lazy := &lazySearchAdmission{ctx: ctx, request: request}
	return context.WithValue(ctx, lazyAdmissionContextKey{}, lazy), lazy
}

func ensureLiveSearchAdmission(ctx context.Context) error {
	lazy, _ := ctx.Value(lazyAdmissionContextKey{}).(*lazySearchAdmission)
	if lazy == nil {
		return nil
	}
	lazy.once.Do(func() { lazy.admission, lazy.err = acquireLiveSearch(lazy.ctx, lazy.request) })
	return lazy.err
}

func (l *lazySearchAdmission) Release() {
	if l == nil {
		return
	}
	l.release.Do(func() {
		if l.admission != nil {
			l.admission.Release()
		}
	})
}

func configureSearchScheduler(snapshot *sourceconfig.Snapshot) {
	if snapshot == nil {
		return
	}
	scheduler := GlobalSearchScheduler()
	for _, channel := range snapshot.Config.Channels {
		if !channel.Enabled {
			continue
		}
		policy := searchscheduler.SourcePolicy{MaxConcurrency: channel.MaxConcurrency}
		if channel.TimeoutSeconds > 0 {
			policy.Timeout = time.Duration(channel.TimeoutSeconds) * time.Second
		}
		scheduler.ConfigureSource("tg:"+channel.Key, policy)
	}
	for name, settings := range snapshot.Config.Plugins {
		if !settings.Enabled {
			continue
		}
		policy := searchscheduler.SourcePolicy{MaxConcurrency: settings.MaxConcurrency, CircuitFailures: settings.CircuitFailures}
		if settings.TimeoutSeconds > 0 {
			policy.Timeout = time.Duration(settings.TimeoutSeconds) * time.Second
		}
		if settings.CircuitCooldownSeconds > 0 {
			policy.CircuitCooldown = time.Duration(settings.CircuitCooldownSeconds) * time.Second
		}
		scheduler.ConfigureSource("plugin:"+name, policy)
	}
}
