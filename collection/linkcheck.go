package collection

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
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
	Platforms                []LinkCheckPlatformSnapshot
}

type LinkCheckPlatformSnapshot struct {
	Platform     string     `json:"platform"`
	Queued       int        `json:"queued"`
	InUse        int        `json:"in_use"`
	Limit        int        `json:"limit"`
	FailStreak   int        `json:"fail_streak"`
	CircuitOpen  bool       `json:"circuit_open"`
	CircuitUntil *time.Time `json:"circuit_until,omitempty"`
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
	Workers            int
	Buffer             int
	Timeout            time.Duration
	PollInterval       time.Duration
	BacklogInterval    time.Duration
	BatchSize          int
	PlatformLimit      int
	CircuitFailures    int
	CircuitCooldown    time.Duration
	WriteBatchSize     int
	WriteFlushInterval time.Duration
	Now                func() time.Time
	OnError            func(error)
}

func DefaultLinkCheckQueueConfig() LinkCheckQueueConfig {
	return LinkCheckQueueConfig{
		Workers:            8,
		Buffer:             1024,
		Timeout:            15 * time.Second,
		PollInterval:       time.Minute,
		BacklogInterval:    5 * time.Minute,
		BatchSize:          500,
		PlatformLimit:      2,
		CircuitFailures:    5,
		CircuitCooldown:    5 * time.Minute,
		WriteBatchSize:     16,
		WriteFlushInterval: time.Second,
		Now:                time.Now,
	}
}

type linkCheckPlatformState struct {
	inUse        int
	failStreak   int
	circuitUntil time.Time
	halfOpen     bool
}

type linkCheckCompletion struct {
	candidate       LinkCheckCandidate
	result          LinkCheckResult
	checkFailed     bool
	circuitFailure  bool
	skipPersistence bool
}

// LinkCheckQueue performs checks asynchronously and suppresses duplicate jobs
// for the same resource while one is queued or running.
type LinkCheckQueue struct {
	repository LinkCheckRepository
	checker    LinkChecker
	config     LinkCheckQueueConfig
	wake       chan struct{}
	results    chan linkCheckCompletion

	mu                sync.Mutex
	started           bool
	stopping          bool
	cancel            context.CancelFunc
	stopDone          chan struct{}
	queued            map[int64]struct{}
	pending           map[string][]LinkCheckCandidate
	platformOrder     []string
	platformCursor    int
	platformStates    map[string]*linkCheckPlatformState
	active            int
	startedAt         time.Time
	completionMetrics []linkCheckCompletionMetric
	backlogSamples    []linkCheckBacklogSample
	wg                sync.WaitGroup
	workerWG          sync.WaitGroup
	workersDone       chan struct{}
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
	if config.BacklogInterval <= 0 {
		config.BacklogInterval = defaults.BacklogInterval
	}
	if config.BatchSize <= 0 || config.BatchSize > 1000 {
		config.BatchSize = defaults.BatchSize
	}
	if config.PlatformLimit <= 0 {
		config.PlatformLimit = defaults.PlatformLimit
	}
	if config.CircuitFailures <= 0 {
		config.CircuitFailures = defaults.CircuitFailures
	}
	if config.CircuitCooldown <= 0 {
		config.CircuitCooldown = defaults.CircuitCooldown
	}
	if config.WriteBatchSize <= 0 {
		config.WriteBatchSize = defaults.WriteBatchSize
	}
	if config.WriteFlushInterval <= 0 {
		config.WriteFlushInterval = defaults.WriteFlushInterval
	}
	if config.Now == nil {
		config.Now = defaults.Now
	}
	return &LinkCheckQueue{
		repository:     repository,
		checker:        checker,
		config:         config,
		wake:           make(chan struct{}, 1),
		results:        make(chan linkCheckCompletion, config.Buffer+config.Workers),
		queued:         make(map[int64]struct{}),
		pending:        make(map[string][]LinkCheckCandidate),
		platformStates: make(map[string]*linkCheckPlatformState),
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
	q.queued = make(map[int64]struct{})
	q.pending = make(map[string][]LinkCheckCandidate)
	q.platformOrder = nil
	q.platformCursor = 0
	q.platformStates = make(map[string]*linkCheckPlatformState)
	q.workersDone = make(chan struct{})
	for i := 0; i < q.config.Workers; i++ {
		q.wg.Add(1)
		q.workerWG.Add(1)
		go q.worker(ctx)
	}
	workersDone := q.workersDone
	go func() {
		q.workerWG.Wait()
		close(workersDone)
	}()
	q.wg.Add(1)
	go q.writeResults(workersDone)
	q.wg.Add(1)
	go q.schedule(ctx)
	q.signalWake()
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
	if !q.started {
		q.mu.Unlock()
		return ErrQueueNotStarted
	}
	if _, exists := q.queued[candidate.ResourceID]; exists {
		q.mu.Unlock()
		return nil
	}
	if len(q.queued)-q.active >= q.config.Buffer {
		q.mu.Unlock()
		return ErrQueueFull
	}
	platform := normalizeLinkCheckPlatform(candidate.Platform)
	candidate.Platform = platform
	if _, exists := q.pending[platform]; !exists {
		q.platformOrder = append(q.platformOrder, platform)
	}
	q.pending[platform] = append(q.pending[platform], candidate)
	q.queued[candidate.ResourceID] = struct{}{}
	q.mu.Unlock()
	q.signalWake()
	return nil
}

func (q *LinkCheckQueue) worker(ctx context.Context) {
	defer q.wg.Done()
	defer q.workerWG.Done()
	for {
		candidate, ok := q.claimNext(ctx)
		if !ok {
			return
		}
		completion := q.checkCandidate(ctx, candidate)
		q.releasePlatform(candidate.Platform, completion.circuitFailure)
		q.results <- completion
	}
}

func (q *LinkCheckQueue) claimNext(ctx context.Context) (LinkCheckCandidate, bool) {
	for {
		now := q.config.Now().UTC()
		var nextProbe time.Time
		q.mu.Lock()
		platformCount := len(q.platformOrder)
		for offset := 0; offset < platformCount; offset++ {
			index := (q.platformCursor + offset) % platformCount
			platform := q.platformOrder[index]
			pending := q.pending[platform]
			if len(pending) == 0 {
				continue
			}
			state := q.platformStates[platform]
			if state == nil {
				state = &linkCheckPlatformState{}
				q.platformStates[platform] = state
			}
			if state.circuitUntil.After(now) {
				if nextProbe.IsZero() || state.circuitUntil.Before(nextProbe) {
					nextProbe = state.circuitUntil
				}
				continue
			}
			if !state.circuitUntil.IsZero() {
				if state.halfOpen || state.inUse > 0 {
					continue
				}
				state.halfOpen = true
			} else if state.inUse >= q.config.PlatformLimit {
				continue
			}

			candidate := pending[0]
			if len(pending) == 1 {
				q.pending[platform] = nil
			} else {
				q.pending[platform] = pending[1:]
			}
			state.inUse++
			q.active++
			q.platformCursor = (index + 1) % platformCount
			q.mu.Unlock()
			q.signalWake()
			return candidate, true
		}
		q.mu.Unlock()

		var timer <-chan time.Time
		var probeTimer *time.Timer
		if !nextProbe.IsZero() {
			delay := nextProbe.Sub(now)
			if delay < 0 {
				delay = 0
			}
			probeTimer = time.NewTimer(delay)
			timer = probeTimer.C
		}
		select {
		case <-ctx.Done():
			if probeTimer != nil {
				probeTimer.Stop()
			}
			return LinkCheckCandidate{}, false
		case <-q.wake:
			if probeTimer != nil {
				probeTimer.Stop()
			}
		case <-timer:
		}
	}
}

func (q *LinkCheckQueue) releasePlatform(platform string, circuitFailure bool) {
	platform = normalizeLinkCheckPlatform(platform)
	now := q.config.Now().UTC()
	q.mu.Lock()
	state := q.platformStates[platform]
	if state == nil {
		state = &linkCheckPlatformState{}
		q.platformStates[platform] = state
	}
	if state.inUse > 0 {
		state.inUse--
	}
	if q.active > 0 {
		q.active--
	}
	if circuitFailure {
		state.failStreak++
		if state.halfOpen || state.failStreak >= q.config.CircuitFailures {
			state.circuitUntil = now.Add(q.config.CircuitCooldown)
			state.halfOpen = false
		}
	} else {
		state.failStreak = 0
		state.circuitUntil = time.Time{}
		state.halfOpen = false
	}
	q.mu.Unlock()
	q.signalWake()
}

func (q *LinkCheckQueue) schedule(ctx context.Context) {
	defer q.wg.Done()
	q.dispatch(ctx)
	q.sampleBacklog(ctx)
	pollTicker := time.NewTicker(q.config.PollInterval)
	backlogTicker := time.NewTicker(q.config.BacklogInterval)
	defer pollTicker.Stop()
	defer backlogTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-pollTicker.C:
			q.dispatch(ctx)
		case <-backlogTicker.C:
			q.sampleBacklog(ctx)
		}
	}
}

func (q *LinkCheckQueue) dispatch(ctx context.Context) {
	now := q.config.Now().UTC()
	fetchLimit := q.config.BatchSize * 2
	if fetchLimit > 1000 {
		fetchLimit = 1000
	}
	candidates, err := q.repository.DueLinkChecks(ctx, fetchLimit, now)
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

func (q *LinkCheckQueue) sampleBacklog(ctx context.Context) {
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
}

func (q *LinkCheckQueue) checkCandidate(parent context.Context, candidate LinkCheckCandidate) linkCheckCompletion {
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
	return linkCheckCompletion{
		candidate:       candidate,
		result:          result,
		checkFailed:     checkErr != nil,
		circuitFailure:  checkErr != nil && !errors.Is(checkErr, context.Canceled),
		skipPersistence: parent.Err() != nil && errors.Is(checkErr, context.Canceled),
	}
}

func (q *LinkCheckQueue) writeResults(workersDone <-chan struct{}) {
	defer q.wg.Done()
	_, supportsBatch := q.repository.(LinkCheckBatchRepository)
	ticker := time.NewTicker(q.config.WriteFlushInterval)
	defer ticker.Stop()
	pending := make([]linkCheckCompletion, 0, q.config.WriteBatchSize)
	flush := func() {
		if len(pending) == 0 {
			return
		}
		q.persistCompletions(pending)
		pending = pending[:0]
	}
	for {
		select {
		case completion := <-q.results:
			if completion.skipPersistence {
				q.discardCompletion(completion)
				continue
			}
			pending = append(pending, completion)
			if !supportsBatch || len(pending) >= q.config.WriteBatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-workersDone:
			for {
				select {
				case completion := <-q.results:
					if completion.skipPersistence {
						q.discardCompletion(completion)
						continue
					}
					pending = append(pending, completion)
				default:
					flush()
					return
				}
			}
		}
	}
}

func (q *LinkCheckQueue) persistCompletions(completions []linkCheckCompletion) {
	results := make([]LinkCheckResult, len(completions))
	for index := range completions {
		results[index] = completions[index].result
	}
	writeTimeout := q.config.Timeout
	if writeTimeout < 10*time.Second {
		writeTimeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()

	if batchRepository, ok := q.repository.(LinkCheckBatchRepository); ok && len(results) > 1 {
		err := batchRepository.CompleteLinkChecks(ctx, results)
		for _, completion := range completions {
			q.finalizeCompletion(completion, err)
		}
		return
	}
	for _, completion := range completions {
		err := q.repository.CompleteLinkCheck(ctx, completion.result)
		q.finalizeCompletion(completion, err)
	}
}

func (q *LinkCheckQueue) finalizeCompletion(completion linkCheckCompletion, completeErr error) {
	q.mu.Lock()
	delete(q.queued, completion.candidate.ResourceID)
	q.mu.Unlock()
	q.recordCompletion(completeErr == nil, completion.checkFailed || completeErr != nil)
	if completeErr != nil {
		q.report(fmt.Errorf("complete link check for resource %d: %w", completion.candidate.ResourceID, completeErr))
	}
	q.signalWake()
}

func (q *LinkCheckQueue) discardCompletion(completion linkCheckCompletion) {
	q.mu.Lock()
	delete(q.queued, completion.candidate.ResourceID)
	q.mu.Unlock()
	q.signalWake()
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
	for _, platform := range q.platformOrder {
		state := q.platformStates[platform]
		platformSnapshot := LinkCheckPlatformSnapshot{
			Platform: platform,
			Queued:   len(q.pending[platform]),
			Limit:    q.config.PlatformLimit,
		}
		if state != nil {
			platformSnapshot.InUse = state.inUse
			platformSnapshot.FailStreak = state.failStreak
			platformSnapshot.CircuitOpen = state.circuitUntil.After(now)
			if !state.circuitUntil.IsZero() {
				until := state.circuitUntil
				platformSnapshot.CircuitUntil = &until
			}
		}
		if platformSnapshot.Queued > 0 || platformSnapshot.InUse > 0 || platformSnapshot.FailStreak > 0 || platformSnapshot.CircuitOpen {
			snapshot.Platforms = append(snapshot.Platforms, platformSnapshot)
		}
	}
	sort.Slice(snapshot.Platforms, func(i, j int) bool {
		return snapshot.Platforms[i].Platform < snapshot.Platforms[j].Platform
	})
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

func (q *LinkCheckQueue) signalWake() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func normalizeLinkCheckPlatform(platform string) string {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform == "" {
		return "unknown"
	}
	return platform
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
