package service

import (
	"context"
	"log"
	"sync"
	"time"

	searchscheduler "pansou/service/scheduler"
	"pansou/storage"
)

type SchedulerMetricsRecorder struct {
	store    *storage.Store
	interval time.Duration
	mu       sync.Mutex
	previous map[string]searchscheduler.SourceSnapshot
}

func NewSchedulerMetricsRecorder(store *storage.Store, interval time.Duration) *SchedulerMetricsRecorder {
	if interval <= 0 {
		interval = time.Minute
	}
	return &SchedulerMetricsRecorder{store: store, interval: interval, previous: make(map[string]searchscheduler.SourceSnapshot)}
}

func (r *SchedulerMetricsRecorder) Start(ctx context.Context) {
	if r == nil || r.store == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				r.flush(ctx)
			case <-ctx.Done():
				flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				r.flush(flushCtx)
				cancel()
				return
			}
		}
	}()
}

func (r *SchedulerMetricsRecorder) flush(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot := GlobalSearchScheduler().Snapshot()
	now := time.Now().UTC()
	deltas := make([]storage.SearchSourceMetricDelta, 0, len(snapshot.Sources))
	nextPrevious := make(map[string]searchscheduler.SourceSnapshot, len(r.previous)+len(snapshot.Sources))
	for source, previous := range r.previous {
		nextPrevious[source] = previous
	}
	for _, current := range snapshot.Sources {
		previous := r.previous[current.Source]
		if current.Runs < previous.Runs {
			previous = searchscheduler.SourceSnapshot{}
		}
		deltaRuns := current.Runs - previous.Runs
		if deltaRuns == 0 && current.Skipped == previous.Skipped {
			nextPrevious[current.Source] = current
			continue
		}
		currentDuration := current.TotalDurationMS
		previousDuration := previous.TotalDurationMS
		if currentDuration < previousDuration {
			previousDuration = 0
		}
		deltas = append(deltas, storage.SearchSourceMetricDelta{
			Date: now, Source: current.Source, Runs: deltaRuns,
			Failures: subtractMetric(current.Failures, previous.Failures), Timeouts: subtractMetric(current.Timeouts, previous.Timeouts),
			RateLimited: subtractMetric(current.RateLimited, previous.RateLimited), Skipped: subtractMetric(current.Skipped, previous.Skipped),
			ResultCount: subtractMetric(current.ResultCount, previous.ResultCount), UniqueCount: subtractMetric(current.UniqueCount, previous.UniqueCount),
			TotalDurationMS: currentDuration - previousDuration, P50MS: current.P50MS, P95MS: current.P95MS, MaxMS: current.MaxMS,
		})
		nextPrevious[current.Source] = current
	}
	if err := r.store.RecordSearchSourceMetrics(ctx, deltas); err != nil {
		log.Printf("persist search scheduler metrics: %v", err)
		return
	}
	r.previous = nextPrevious
}

func subtractMetric(current, previous uint64) uint64 {
	if current < previous {
		return current
	}
	return current - previous
}
