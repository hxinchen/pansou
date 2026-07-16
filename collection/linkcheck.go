package collection

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

const (
	linkCheckMetricsWindow    = 5 * time.Minute
	linkCheckETAMinimumWindow = time.Minute
)

const (
	LinkCheckETAIdle                 = "idle"
	LinkCheckETACalculating          = "calculating"
	LinkCheckETAAvailable            = "available"
	LinkCheckETABacklogNotDecreasing = "backlog_not_decreasing"
	LinkCheckETAStopped              = "stopped"
)

type LinkCheckQueueSnapshot struct {
	Started                  bool
	Workers                  int
	Queued                   int
	Active                   int
	DueCount                 int64
	DueCountKnown            bool
	CompletedLastFiveMinutes int64
	FailedLastFiveMinutes    int64
	ThroughputPerMinute      float64
	NetDrainPerMinute        *float64
	ETASeconds               *int64
	ETAState                 string
	MetricsWindow            time.Duration
	MetricsSampleWindow      time.Duration
	BacklogSampleWindow      time.Duration
	BacklogUpdatedAt         time.Time
}

type linkCheckCompletionMetric struct {
	at        time.Time
	completed bool
	failed    bool
}

type linkCheckBacklogSample struct {
	at             time.Time
	dueCount       int64
	policyRevision string
}

type LinkCheckQueueConfig struct {
	Workers      int
	Buffer       int
	Timeout      time.Duration
	PollInterval time.Duration
	BatchSize    int
	Now          func() time.Time
	OnError      func(error)
}

func DefaultLinkCheckQueueConfig() LinkCheckQueueConfig {
	return LinkCheckQueueConfig{
		Workers:      2,
		Buffer:       256,
		Timeout:      15 * time.Second,
		PollInterval: time.Minute,
		BatchSize:    500,
		Now:          time.Now,
	}
}

// LinkCheckQueue performs checks asynchronously and suppresses duplicate jobs
// for the same resource while one is queued or running.
type LinkCheckQueue struct {
	repository LinkCheckRepository
	checker    LinkChecker
	config     LinkCheckQueueConfig
	jobs       chan LinkCheckCandidate

	mu                sync.Mutex
	started           bool
	stopping          bool
	cancel            context.CancelFunc
	stopDone          chan struct{}
	queued            map[int64]struct{}
	active            int
	startedAt         time.Time
	completionMetrics []linkCheckCompletionMetric
	backlogSamples    []linkCheckBacklogSample
	wg                sync.WaitGroup
}

func NewLinkCheckQueue(repository LinkCheckRepository, checker LinkChecker, config LinkCheckQueueConfig) *LinkCheckQueue {
	defaults := DefaultLinkCheckQueueConfig()
	if config.Workers <= 0 {
		config.Workers = defaults.Workers
	}
	if config.Buffer <= 0 {
		config.Buffer = defaults.Buffer
	}
	if config.Timeout <= 0 {
		config.Timeout = defaults.Timeout
	}
	if config.PollInterval <= 0 {
		config.PollInterval = defaults.PollInterval
	}
	if config.BatchSize <= 0 || config.BatchSize > 1000 {
		config.BatchSize = defaults.BatchSize
	}
	if config.Now == nil {
		config.Now = defaults.Now
	}
	return &LinkCheckQueue{
		repository: repository,
		checker:    checker,
		config:     config,
		jobs:       make(chan LinkCheckCandidate, config.Buffer),
		queued:     make(map[int64]struct{}),
	}
}

func (q *LinkCheckQueue) Start(parent context.Context) error {
	if q.repository == nil {
		return errors.New("link check repository is nil")
	}
	if q.checker == nil {
		return errors.New("link checker is nil")
	}
	if parent == nil {
		parent = context.Background()
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.started {
		return nil
	}
	if q.stopping {
		return ErrQueueStopping
	}
	ctx, cancel := context.WithCancel(parent)
	q.cancel = cancel
	q.started = true
	q.startedAt = q.config.Now().UTC()
	q.active = 0
	q.completionMetrics = nil
	q.backlogSamples = nil
	for i := 0; i < q.config.Workers; i++ {
		q.wg.Add(1)
		go q.worker(ctx)
	}
	q.wg.Add(1)
	go q.schedule(ctx)
	return nil
}

func (q *LinkCheckQueue) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	q.mu.Lock()
	if !q.started && !q.stopping {
		q.mu.Unlock()
		return nil
	}
	var done chan struct{}
	if q.started {
		q.started = false
		q.stopping = true
		q.cancel()
		done = make(chan struct{})
		q.stopDone = done
		go q.finishStop(done)
	} else {
		done = q.stopDone
	}
	q.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *LinkCheckQueue) finishStop(done chan struct{}) {
	q.wg.Wait()
	q.mu.Lock()
	if q.stopDone == done {
		q.stopping = false
		q.cancel = nil
		q.stopDone = nil
	}
	close(done)
	q.mu.Unlock()
}

func (q *LinkCheckQueue) Enqueue(ctx context.Context, candidate LinkCheckCandidate) error {
	if candidate.ResourceID == 0 || candidate.URL == "" {
		return errors.New("invalid link check candidate")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.started {
		return ErrQueueNotStarted
	}
	if _, exists := q.queued[candidate.ResourceID]; exists {
		return nil
	}
	select {
	case q.jobs <- candidate:
		q.queued[candidate.ResourceID] = struct{}{}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return ErrQueueFull
	}
}

func (q *LinkCheckQueue) worker(ctx context.Context) {
	defer q.wg.Done()
	for {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case candidate := <-q.jobs:
			q.mu.Lock()
			q.active++
			q.mu.Unlock()
			q.process(ctx, candidate)
		}
	}
}

func (q *LinkCheckQueue) schedule(ctx context.Context) {
	defer q.wg.Done()
	q.dispatch(ctx)
	ticker := time.NewTicker(q.config.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			q.dispatch(ctx)
		}
	}
}

func (q *LinkCheckQueue) dispatch(ctx context.Context) {
	now := q.config.Now().UTC()
	if counter, ok := q.repository.(LinkCheckBacklogCounter); ok {
		observation, err := counter.CountDueLinkChecks(ctx, now)
		if err != nil {
			if ctx.Err() == nil {
				q.report(fmt.Errorf("count due link checks: %w", err))
			}
		} else {
			q.ObserveBacklog(observation.DueCount, now, observation.PolicyRevision)
		}
	}
	candidates, err := q.repository.DueLinkChecks(ctx, q.config.BatchSize, now)
	if err != nil {
		if ctx.Err() == nil {
			q.report(fmt.Errorf("list due link checks: %w", err))
		}
		return
	}
	for _, candidate := range candidates {
		if err := q.Enqueue(ctx, candidate); err != nil {
			if errors.Is(err, ErrQueueFull) || errors.Is(err, ErrQueueNotStarted) || ctx.Err() != nil {
				return
			}
			q.report(fmt.Errorf("enqueue due link check for resource %d: %w", candidate.ResourceID, err))
		}
	}
}

func (q *LinkCheckQueue) process(parent context.Context, candidate LinkCheckCandidate) {
	defer func() {
		q.mu.Lock()
		delete(q.queued, candidate.ResourceID)
		if q.active > 0 {
			q.active--
		}
		q.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(parent, q.config.Timeout)
	status, checkErr := q.checker.Check(ctx, candidate)
	cancel()
	if checkErr != nil {
		status = DetectionUnknown
	}
	if !isFinalDetectionStatus(status) {
		if checkErr == nil {
			checkErr = fmt.Errorf("checker returned invalid status %q", status)
		}
		status = DetectionUnknown
	}

	checkedAt := q.config.Now().UTC()
	result := LinkCheckResult{
		ResourceID: candidate.ResourceID,
		Status:     status,
		CheckedAt:  checkedAt,
	}
	if checkErr != nil {
		result.Error = checkErr.Error()
	}
	completeErr := q.repository.CompleteLinkCheck(parent, result)
	q.recordCompletion(completeErr == nil, checkErr != nil || completeErr != nil)
	if completeErr != nil {
		q.report(fmt.Errorf("complete link check for resource %d: %w", candidate.ResourceID, completeErr))
	}
}

func (q *LinkCheckQueue) ObserveBacklog(dueCount int64, at time.Time, policyRevision string) {
	if q == nil {
		return
	}
	if dueCount < 0 {
		dueCount = 0
	}
	if at.IsZero() {
		at = q.config.Now()
	}
	at = at.UTC()
	q.mu.Lock()
	defer q.mu.Unlock()
	last := len(q.backlogSamples) - 1
	if last >= 0 {
		if at.Before(q.backlogSamples[last].at) {
			return
		}
		if policyRevision != q.backlogSamples[last].policyRevision {
			q.backlogSamples = nil
			last = -1
		}
	}
	if last >= 0 {
		if at.Equal(q.backlogSamples[last].at) {
			q.backlogSamples[last].dueCount = dueCount
			q.pruneBacklogSamplesLocked(at)
			return
		}
	}
	q.backlogSamples = append(q.backlogSamples, linkCheckBacklogSample{
		at: at, dueCount: dueCount, policyRevision: policyRevision,
	})
	q.pruneBacklogSamplesLocked(at)
}

func (q *LinkCheckQueue) Snapshot() LinkCheckQueueSnapshot {
	if q == nil {
		return LinkCheckQueueSnapshot{ETAState: LinkCheckETAStopped, MetricsWindow: linkCheckMetricsWindow}
	}
	now := q.config.Now().UTC()
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pruneCompletionMetricsLocked(now)
	q.pruneBacklogSamplesLocked(now)

	snapshot := LinkCheckQueueSnapshot{
		Started:       q.started,
		Workers:       q.config.Workers,
		Queued:        len(q.queued) - q.active,
		Active:        q.active,
		ETAState:      LinkCheckETACalculating,
		MetricsWindow: linkCheckMetricsWindow,
	}
	if snapshot.Queued < 0 {
		snapshot.Queued = 0
	}
	for _, metric := range q.completionMetrics {
		if metric.completed {
			snapshot.CompletedLastFiveMinutes++
		}
		if metric.failed {
			snapshot.FailedLastFiveMinutes++
		}
	}
	metricsStart := now.Add(-linkCheckMetricsWindow)
	if !q.startedAt.IsZero() && q.startedAt.After(metricsStart) {
		metricsStart = q.startedAt
	}
	metricsElapsed := now.Sub(metricsStart)
	if metricsElapsed < 0 {
		metricsElapsed = 0
	}
	snapshot.MetricsSampleWindow = metricsElapsed
	if metricsElapsed >= linkCheckETAMinimumWindow {
		snapshot.ThroughputPerMinute = float64(snapshot.CompletedLastFiveMinutes) / metricsElapsed.Minutes()
	}

	if !snapshot.Started {
		snapshot.ETAState = LinkCheckETAStopped
	}
	if len(q.backlogSamples) == 0 {
		return snapshot
	}
	oldest := q.backlogSamples[0]
	latest := q.backlogSamples[len(q.backlogSamples)-1]
	snapshot.DueCountKnown = true
	snapshot.DueCount = latest.dueCount
	snapshot.BacklogUpdatedAt = latest.at
	snapshot.BacklogSampleWindow = latest.at.Sub(oldest.at)
	if snapshot.BacklogSampleWindow < 0 {
		snapshot.BacklogSampleWindow = 0
	}
	if !snapshot.Started {
		return snapshot
	}
	if latest.dueCount == 0 {
		zero := int64(0)
		snapshot.ETASeconds = &zero
		snapshot.ETAState = LinkCheckETAIdle
		return snapshot
	}
	if snapshot.BacklogSampleWindow < linkCheckETAMinimumWindow {
		return snapshot
	}
	drained := oldest.dueCount - latest.dueCount
	if drained <= 0 {
		snapshot.ETAState = LinkCheckETABacklogNotDecreasing
		return snapshot
	}
	rate := float64(drained) / snapshot.BacklogSampleWindow.Minutes()
	if rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		snapshot.ETAState = LinkCheckETABacklogNotDecreasing
		return snapshot
	}
	etaSeconds := int64(math.Ceil(float64(latest.dueCount) / rate * 60))
	snapshot.NetDrainPerMinute = &rate
	snapshot.ETASeconds = &etaSeconds
	snapshot.ETAState = LinkCheckETAAvailable
	return snapshot
}

func (q *LinkCheckQueue) recordCompletion(completed, failed bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	at := q.config.Now().UTC()
	q.completionMetrics = append(q.completionMetrics, linkCheckCompletionMetric{
		at: at, completed: completed, failed: failed,
	})
	q.pruneCompletionMetricsLocked(at)
}

func (q *LinkCheckQueue) pruneCompletionMetricsLocked(at time.Time) {
	cutoff := at.Add(-linkCheckMetricsWindow)
	kept := q.completionMetrics[:0]
	for _, metric := range q.completionMetrics {
		if !metric.at.Before(cutoff) {
			kept = append(kept, metric)
		}
	}
	q.completionMetrics = kept
}

func (q *LinkCheckQueue) pruneBacklogSamplesLocked(at time.Time) {
	cutoff := at.Add(-linkCheckMetricsWindow)
	first := 0
	for first < len(q.backlogSamples) && q.backlogSamples[first].at.Before(cutoff) {
		first++
	}
	if first > 0 {
		q.backlogSamples = append([]linkCheckBacklogSample(nil), q.backlogSamples[first:]...)
	}
}

func (q *LinkCheckQueue) report(err error) {
	if err != nil && q.config.OnError != nil {
		q.config.OnError(err)
	}
}

func isFinalDetectionStatus(status DetectionStatus) bool {
	switch status {
	case DetectionValid, DetectionInvalid, DetectionExpired, DetectionCancelled, DetectionViolation, DetectionLocked, DetectionUnknown, DetectionUnsupported:
		return true
	default:
		return false
	}
}
