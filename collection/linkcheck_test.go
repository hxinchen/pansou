package collection

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeLinkCheckRepository struct {
	mu          sync.Mutex
	results     []LinkCheckResult
	completeErr error
	due         []LinkCheckCandidate
	dueCalls    int
	dueAfter    int
	dueOnce     bool
	dueErr      error
	done        chan struct{}
}

func (f *fakeLinkCheckRepository) CompleteLinkCheck(_ context.Context, result LinkCheckResult) error {
	f.mu.Lock()
	f.results = append(f.results, result)
	f.mu.Unlock()
	select {
	case f.done <- struct{}{}:
	default:
	}
	return f.completeErr
}

type linkCheckTestClock struct {
	mu  sync.Mutex
	now time.Time
}

type fakeLinkCheckBacklogRepository struct {
	*fakeLinkCheckRepository
	mu         sync.Mutex
	count      int64
	countCalls int
	done       chan struct{}
}

func (f *fakeLinkCheckBacklogRepository) CountDueLinkChecks(_ context.Context, _ time.Time) (LinkCheckBacklogObservation, error) {
	f.mu.Lock()
	f.countCalls++
	f.mu.Unlock()
	select {
	case f.done <- struct{}{}:
	default:
	}
	return LinkCheckBacklogObservation{DueCount: f.count, PolicyRevision: "policy-v1"}, nil
}

type fakeBatchLinkCheckRepository struct {
	*fakeLinkCheckRepository
	batchMu    sync.Mutex
	batchCalls int
	batches    [][]LinkCheckResult
}

func (f *fakeBatchLinkCheckRepository) CompleteLinkChecks(_ context.Context, results []LinkCheckResult) error {
	copyResults := append([]LinkCheckResult(nil), results...)
	f.batchMu.Lock()
	f.batchCalls++
	f.batches = append(f.batches, copyResults)
	f.batchMu.Unlock()
	f.fakeLinkCheckRepository.mu.Lock()
	f.fakeLinkCheckRepository.results = append(f.fakeLinkCheckRepository.results, copyResults...)
	f.fakeLinkCheckRepository.mu.Unlock()
	for range results {
		select {
		case f.done <- struct{}{}:
		default:
		}
	}
	return f.completeErr
}

func (c *linkCheckTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *linkCheckTestClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

func (f *fakeLinkCheckRepository) DueLinkChecks(_ context.Context, limit int, _ time.Time) ([]LinkCheckCandidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dueCalls++
	if f.dueErr != nil {
		return nil, f.dueErr
	}
	if f.dueAfter > 0 && f.dueCalls < f.dueAfter {
		return nil, nil
	}
	if limit > len(f.due) {
		limit = len(f.due)
	}
	result := append([]LinkCheckCandidate(nil), f.due[:limit]...)
	if f.dueOnce {
		f.due = nil
	}
	return result, nil
}

func TestLinkCheckQueueRefreshesDueLinksOnPoll(t *testing.T) {
	candidate := LinkCheckCandidate{ResourceID: 10, URL: "https://example.test/share/10", Status: DetectionUnknown}
	repository := &fakeLinkCheckRepository{due: []LinkCheckCandidate{candidate}, dueAfter: 2, dueOnce: true, done: make(chan struct{}, 1)}
	config := DefaultLinkCheckQueueConfig()
	config.Workers = 1
	config.PollInterval = 10 * time.Millisecond
	queue := NewLinkCheckQueue(repository, LinkCheckerFunc(func(context.Context, LinkCheckCandidate) (DetectionStatus, error) {
		return DetectionValid, nil
	}), config)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := queue.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-repository.done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := queue.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.dueCalls < 2 || len(repository.results) != 1 {
		t.Fatalf("poll calls=%d results=%+v", repository.dueCalls, repository.results)
	}
}

func TestLinkCheckQueueStoresUnknownOnCheckerErrorAndDeduplicates(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	repository := &fakeLinkCheckRepository{done: make(chan struct{}, 1)}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	checks := 0
	var checksMu sync.Mutex
	checker := LinkCheckerFunc(func(ctx context.Context, _ LinkCheckCandidate) (DetectionStatus, error) {
		checksMu.Lock()
		checks++
		checksMu.Unlock()
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return DetectionPending, errors.New("temporary checker error")
		case <-ctx.Done():
			return DetectionUnknown, ctx.Err()
		}
	})
	config := DefaultLinkCheckQueueConfig()
	config.Workers = 1
	config.Now = func() time.Time { return now }
	queue := NewLinkCheckQueue(repository, checker, config)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := queue.Start(ctx); err != nil {
		t.Fatal(err)
	}
	candidate := LinkCheckCandidate{ResourceID: 5, URL: "https://example.test/share/5", IsNew: true}
	if err := queue.Enqueue(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := queue.Enqueue(ctx, candidate); err != nil {
		t.Fatalf("duplicate enqueue: %v", err)
	}
	close(release)
	select {
	case <-repository.done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := queue.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot := queue.Snapshot()
	if snapshot.CompletedLastFiveMinutes != 1 || snapshot.FailedLastFiveMinutes != 1 {
		t.Fatalf("queue metrics = completed %d failed %d, want 1 and 1", snapshot.CompletedLastFiveMinutes, snapshot.FailedLastFiveMinutes)
	}

	checksMu.Lock()
	if checks != 1 {
		t.Fatalf("checker calls = %d, want 1", checks)
	}
	checksMu.Unlock()
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if len(repository.results) != 1 {
		t.Fatalf("stored results = %d", len(repository.results))
	}
	result := repository.results[0]
	if result.Status != DetectionUnknown || result.Error == "" || !result.CheckedAt.Equal(now) {
		t.Fatalf("stored result = %+v", result)
	}
}

func TestLinkCheckQueueDispatchesDueLinksImmediately(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	candidate := LinkCheckCandidate{ResourceID: 9, URL: "https://example.test/share/9", Status: DetectionValid}
	repository := &fakeLinkCheckRepository{due: []LinkCheckCandidate{candidate}, dueOnce: true, done: make(chan struct{}, 1)}
	config := DefaultLinkCheckQueueConfig()
	config.Workers = 1
	config.PollInterval = time.Hour
	config.Now = func() time.Time { return now }
	queue := NewLinkCheckQueue(repository, LinkCheckerFunc(func(context.Context, LinkCheckCandidate) (DetectionStatus, error) {
		return DetectionValid, nil
	}), config)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := queue.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-repository.done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := queue.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.dueCalls != 1 || len(repository.results) != 1 || repository.results[0].ResourceID != candidate.ResourceID {
		t.Fatalf("dispatch calls=%d results=%+v", repository.dueCalls, repository.results)
	}
}

func TestLinkCheckQueueSamplesBacklogDuringDispatch(t *testing.T) {
	repository := &fakeLinkCheckBacklogRepository{
		fakeLinkCheckRepository: &fakeLinkCheckRepository{done: make(chan struct{}, 1)},
		count:                   42,
		done:                    make(chan struct{}, 1),
	}
	config := DefaultLinkCheckQueueConfig()
	config.Workers = 1
	config.PollInterval = time.Hour
	queue := NewLinkCheckQueue(repository, LinkCheckerFunc(func(context.Context, LinkCheckCandidate) (DetectionStatus, error) {
		return DetectionValid, nil
	}), config)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := queue.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-repository.done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	snapshot := queue.Snapshot()
	if !snapshot.DueCountKnown || snapshot.DueCount != 42 || snapshot.ETAState != LinkCheckETACalculating {
		t.Fatalf("backlog snapshot = %+v", snapshot)
	}
	if err := queue.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestLinkCheckQueueReturnsFullWithoutDroppingQueuedWork(t *testing.T) {
	repository := &fakeLinkCheckRepository{done: make(chan struct{}, 2)}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	config := DefaultLinkCheckQueueConfig()
	config.Workers = 1
	config.Buffer = 1
	config.PollInterval = time.Hour
	queue := NewLinkCheckQueue(repository, LinkCheckerFunc(func(context.Context, LinkCheckCandidate) (DetectionStatus, error) {
		once.Do(func() { close(entered) })
		<-release
		return DetectionValid, nil
	}), config)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := queue.Start(ctx); err != nil {
		t.Fatal(err)
	}
	first := LinkCheckCandidate{ResourceID: 21, URL: "https://example.test/share/21"}
	if err := queue.Enqueue(ctx, first); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	second := LinkCheckCandidate{ResourceID: 22, URL: "https://example.test/share/22"}
	if err := queue.Enqueue(ctx, second); err != nil {
		t.Fatal(err)
	}
	third := LinkCheckCandidate{ResourceID: 23, URL: "https://example.test/share/23"}
	if err := queue.Enqueue(ctx, third); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("third enqueue error = %v, want ErrQueueFull", err)
	}
	close(release)
	for range 2 {
		select {
		case <-repository.done:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if err := queue.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if len(repository.results) != 2 {
		t.Fatalf("completed results = %d, want 2", len(repository.results))
	}
}

func TestLinkCheckQueueSnapshotTracksQueuedActiveAndRecentMetrics(t *testing.T) {
	clock := &linkCheckTestClock{now: time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)}
	repository := &fakeLinkCheckRepository{done: make(chan struct{}, 2)}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	config := DefaultLinkCheckQueueConfig()
	config.Workers = 1
	config.Buffer = 2
	config.PollInterval = time.Hour
	config.Now = clock.Now
	queue := NewLinkCheckQueue(repository, LinkCheckerFunc(func(context.Context, LinkCheckCandidate) (DetectionStatus, error) {
		once.Do(func() { close(entered) })
		<-release
		return DetectionValid, nil
	}), config)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := queue.Start(ctx); err != nil {
		t.Fatal(err)
	}
	first := LinkCheckCandidate{ResourceID: 31, URL: "https://example.test/share/31"}
	second := LinkCheckCandidate{ResourceID: 32, URL: "https://example.test/share/32"}
	if err := queue.Enqueue(ctx, first); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if snapshot := queue.Snapshot(); snapshot.Active != 1 || snapshot.Queued != 0 || !snapshot.Started {
		t.Fatalf("active snapshot = %+v", snapshot)
	}
	if err := queue.Enqueue(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(ctx, second); err != nil {
		t.Fatal(err)
	}
	if snapshot := queue.Snapshot(); snapshot.Active != 1 || snapshot.Queued != 1 {
		t.Fatalf("queued snapshot = %+v", snapshot)
	}
	close(release)
	for range 2 {
		select {
		case <-repository.done:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	for {
		snapshot := queue.Snapshot()
		if snapshot.Active == 0 && snapshot.Queued == 0 {
			if snapshot.CompletedLastFiveMinutes != 2 || snapshot.FailedLastFiveMinutes != 0 {
				t.Fatalf("completed snapshot = %+v", snapshot)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	if err := queue.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if snapshot := queue.Snapshot(); snapshot.Started || snapshot.ETAState != LinkCheckETAStopped {
		t.Fatalf("stopped snapshot = %+v", snapshot)
	}
}

func TestLinkCheckQueueSnapshotExcludesFailedPersistenceFromThroughput(t *testing.T) {
	clock := &linkCheckTestClock{now: time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)}
	repository := &fakeLinkCheckRepository{completeErr: errors.New("write failed"), done: make(chan struct{}, 1)}
	config := DefaultLinkCheckQueueConfig()
	config.Workers = 1
	config.PollInterval = time.Hour
	config.Now = clock.Now
	queue := NewLinkCheckQueue(repository, LinkCheckerFunc(func(context.Context, LinkCheckCandidate) (DetectionStatus, error) {
		return DetectionValid, nil
	}), config)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := queue.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(ctx, LinkCheckCandidate{ResourceID: 41, URL: "https://example.test/share/41"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-repository.done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := queue.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot := queue.Snapshot()
	if snapshot.CompletedLastFiveMinutes != 0 || snapshot.FailedLastFiveMinutes != 1 {
		t.Fatalf("failed persistence snapshot = %+v", snapshot)
	}
	clock.Advance(linkCheckMetricsWindow + time.Second)
	snapshot = queue.Snapshot()
	if snapshot.CompletedLastFiveMinutes != 0 || snapshot.FailedLastFiveMinutes != 0 {
		t.Fatalf("expired metrics snapshot = %+v", snapshot)
	}
}

func TestLinkCheckQueueSnapshotPrunesOutOfOrderCompletionMetrics(t *testing.T) {
	now := time.Date(2026, 7, 17, 9, 30, 0, 0, time.UTC)
	config := DefaultLinkCheckQueueConfig()
	config.Now = func() time.Time { return now }
	queue := NewLinkCheckQueue(&fakeLinkCheckRepository{}, LinkCheckerFunc(func(context.Context, LinkCheckCandidate) (DetectionStatus, error) {
		return DetectionValid, nil
	}), config)
	queue.mu.Lock()
	queue.started = true
	queue.startedAt = now.Add(-10 * time.Minute)
	queue.completionMetrics = []linkCheckCompletionMetric{
		{at: now.Add(-time.Minute), completed: true},
		{at: now.Add(-6 * time.Minute), completed: true, failed: true},
	}
	queue.mu.Unlock()
	snapshot := queue.Snapshot()
	if snapshot.CompletedLastFiveMinutes != 1 || snapshot.FailedLastFiveMinutes != 0 {
		t.Fatalf("out-of-order metrics snapshot = %+v", snapshot)
	}
}

func TestLinkCheckQueueRejectsRestartWhileTimedOutStopIsFinishing(t *testing.T) {
	repository := &fakeLinkCheckRepository{done: make(chan struct{}, 1)}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	config := DefaultLinkCheckQueueConfig()
	config.Workers = 1
	config.PollInterval = time.Hour
	queue := NewLinkCheckQueue(repository, LinkCheckerFunc(func(context.Context, LinkCheckCandidate) (DetectionStatus, error) {
		once.Do(func() { close(entered) })
		<-release
		return DetectionValid, nil
	}), config)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := queue.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(ctx, LinkCheckCandidate{ResourceID: 51, URL: "https://example.test/share/51"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err := queue.Stop(stopCtx)
	stopCancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed stop error = %v, want deadline exceeded", err)
	}
	if err := queue.Start(ctx); !errors.Is(err, ErrQueueStopping) {
		t.Fatalf("restart error = %v, want ErrQueueStopping", err)
	}
	close(release)
	if err := queue.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := queue.Start(ctx); err != nil {
		t.Fatalf("restart after stop: %v", err)
	}
	if err := queue.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestLinkCheckQueueDoesNotPersistShutdownCancellation(t *testing.T) {
	repository := &fakeLinkCheckRepository{done: make(chan struct{}, 1)}
	entered := make(chan struct{})
	checker := LinkCheckerFunc(func(ctx context.Context, _ LinkCheckCandidate) (DetectionStatus, error) {
		close(entered)
		<-ctx.Done()
		return DetectionUnknown, ctx.Err()
	})
	config := DefaultLinkCheckQueueConfig()
	config.Workers = 1
	config.PollInterval = time.Hour
	queue := NewLinkCheckQueue(repository, checker, config)
	ctx := context.Background()
	if err := queue.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(ctx, LinkCheckCandidate{ResourceID: 61, URL: "https://example.test", Platform: "test"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("checker did not start")
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := queue.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if len(repository.results) != 0 {
		t.Fatalf("shutdown cancellation persisted results=%+v", repository.results)
	}
}

func TestLinkCheckQueueBacklogETAStates(t *testing.T) {
	clock := &linkCheckTestClock{now: time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)}
	repository := &fakeLinkCheckRepository{done: make(chan struct{}, 1)}
	config := DefaultLinkCheckQueueConfig()
	config.Workers = 1
	config.PollInterval = time.Hour
	config.Now = clock.Now
	queue := NewLinkCheckQueue(repository, LinkCheckerFunc(func(context.Context, LinkCheckCandidate) (DetectionStatus, error) {
		return DetectionValid, nil
	}), config)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := queue.Start(ctx); err != nil {
		t.Fatal(err)
	}
	queue.ObserveBacklog(120, clock.Now(), "policy-v1")
	if snapshot := queue.Snapshot(); snapshot.ETAState != LinkCheckETACalculating || snapshot.ETASeconds != nil {
		t.Fatalf("calculating snapshot = %+v", snapshot)
	}
	clock.Advance(time.Minute)
	queue.ObserveBacklog(100, clock.Now(), "policy-v1")
	snapshot := queue.Snapshot()
	if snapshot.ETAState != LinkCheckETAAvailable || snapshot.NetDrainPerMinute == nil || *snapshot.NetDrainPerMinute != 20 || snapshot.ETASeconds == nil || *snapshot.ETASeconds != 300 {
		t.Fatalf("available ETA snapshot = %+v", snapshot)
	}
	clock.Advance(time.Minute)
	queue.ObserveBacklog(10, clock.Now(), "policy-v2")
	if snapshot := queue.Snapshot(); snapshot.ETAState != LinkCheckETACalculating || snapshot.BacklogSampleWindow != 0 {
		t.Fatalf("policy reset snapshot = %+v", snapshot)
	}
	clock.Advance(time.Minute)
	queue.ObserveBacklog(130, clock.Now(), "policy-v2")
	if snapshot := queue.Snapshot(); snapshot.ETAState != LinkCheckETABacklogNotDecreasing || snapshot.ETASeconds != nil {
		t.Fatalf("non-decreasing snapshot = %+v", snapshot)
	}
	clock.Advance(time.Minute)
	queue.ObserveBacklog(0, clock.Now(), "policy-v2")
	if snapshot := queue.Snapshot(); snapshot.ETAState != LinkCheckETAIdle || snapshot.ETASeconds == nil || *snapshot.ETASeconds != 0 {
		t.Fatalf("idle snapshot = %+v", snapshot)
	}
	if err := queue.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestLinkCheckQueueLimitsConcurrencyPerPlatformWithoutBlockingOtherPlatforms(t *testing.T) {
	repository := &fakeLinkCheckRepository{done: make(chan struct{}, 8)}
	release := make(chan struct{})
	started := make(chan string, 8)
	var mu sync.Mutex
	inUse := make(map[string]int)
	maxInUse := make(map[string]int)
	checker := LinkCheckerFunc(func(ctx context.Context, candidate LinkCheckCandidate) (DetectionStatus, error) {
		mu.Lock()
		inUse[candidate.Platform]++
		if inUse[candidate.Platform] > maxInUse[candidate.Platform] {
			maxInUse[candidate.Platform] = inUse[candidate.Platform]
		}
		mu.Unlock()
		started <- candidate.Platform
		select {
		case <-release:
		case <-ctx.Done():
			return DetectionUnknown, ctx.Err()
		}
		mu.Lock()
		inUse[candidate.Platform]--
		mu.Unlock()
		return DetectionValid, nil
	})
	config := DefaultLinkCheckQueueConfig()
	config.Workers = 4
	config.PlatformLimit = 2
	config.PollInterval = time.Hour
	queue := NewLinkCheckQueue(repository, checker, config)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := queue.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 4; index++ {
		if err := queue.Enqueue(ctx, LinkCheckCandidate{ResourceID: int64(100 + index), URL: "https://xunlei.test", Platform: "xunlei"}); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < 2; index++ {
		if err := queue.Enqueue(ctx, LinkCheckCandidate{ResourceID: int64(200 + index), URL: "https://baidu.test", Platform: "baidu"}); err != nil {
			t.Fatal(err)
		}
	}
	seen := make(map[string]int)
	for range 4 {
		select {
		case platform := <-started:
			seen[platform]++
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if seen["xunlei"] != 2 || seen["baidu"] != 2 {
		t.Fatalf("first active platforms=%v, want two per platform", seen)
	}
	close(release)
	for range 6 {
		select {
		case <-repository.done:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if err := queue.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if maxInUse["xunlei"] > 2 || maxInUse["baidu"] > 2 {
		t.Fatalf("max platform concurrency=%v", maxInUse)
	}
}

func TestLinkCheckQueueCircuitBreakerCoolsDownAndHalfOpens(t *testing.T) {
	repository := &fakeLinkCheckRepository{done: make(chan struct{}, 3)}
	var mu sync.Mutex
	var calls []time.Time
	checker := LinkCheckerFunc(func(context.Context, LinkCheckCandidate) (DetectionStatus, error) {
		mu.Lock()
		calls = append(calls, time.Now())
		call := len(calls)
		mu.Unlock()
		if call <= 2 {
			return DetectionUnknown, errors.New("upstream timeout")
		}
		return DetectionValid, nil
	})
	config := DefaultLinkCheckQueueConfig()
	config.Workers = 2
	config.PlatformLimit = 1
	config.CircuitFailures = 2
	config.CircuitCooldown = 120 * time.Millisecond
	config.PollInterval = time.Hour
	queue := NewLinkCheckQueue(repository, checker, config)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := queue.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		if err := queue.Enqueue(ctx, LinkCheckCandidate{ResourceID: int64(300 + index), URL: "https://xunlei.test", Platform: "xunlei"}); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		select {
		case <-repository.done:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if err := queue.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("checker calls=%d", len(calls))
	}
	if cooldown := calls[2].Sub(calls[1]); cooldown < 100*time.Millisecond {
		t.Fatalf("half-open probe started after %s, want cooldown", cooldown)
	}
}

func TestLinkCheckQueueBatchesPersistenceWhenRepositorySupportsIt(t *testing.T) {
	repository := &fakeBatchLinkCheckRepository{fakeLinkCheckRepository: &fakeLinkCheckRepository{done: make(chan struct{}, 3)}}
	config := DefaultLinkCheckQueueConfig()
	config.Workers = 3
	config.PlatformLimit = 3
	config.WriteBatchSize = 3
	config.WriteFlushInterval = time.Second
	config.PollInterval = time.Hour
	queue := NewLinkCheckQueue(repository, LinkCheckerFunc(func(context.Context, LinkCheckCandidate) (DetectionStatus, error) {
		return DetectionValid, nil
	}), config)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := queue.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		if err := queue.Enqueue(ctx, LinkCheckCandidate{ResourceID: int64(400 + index), URL: "https://example.test", Platform: "same"}); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		select {
		case <-repository.done:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if err := queue.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	repository.batchMu.Lock()
	defer repository.batchMu.Unlock()
	if repository.batchCalls != 1 || len(repository.batches) != 1 || len(repository.batches[0]) != 3 {
		t.Fatalf("batch calls=%d batches=%v", repository.batchCalls, repository.batches)
	}
}

func TestLinkCheckQueueSamplesBacklogLessOftenThanDispatch(t *testing.T) {
	repository := &fakeLinkCheckBacklogRepository{
		fakeLinkCheckRepository: &fakeLinkCheckRepository{done: make(chan struct{}, 1)},
		count:                   10,
		done:                    make(chan struct{}, 4),
	}
	config := DefaultLinkCheckQueueConfig()
	config.Workers = 1
	config.PollInterval = 10 * time.Millisecond
	config.BacklogInterval = 200 * time.Millisecond
	queue := NewLinkCheckQueue(repository, LinkCheckerFunc(func(context.Context, LinkCheckCandidate) (DetectionStatus, error) {
		return DetectionValid, nil
	}), config)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := queue.Start(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(70 * time.Millisecond)
	if err := queue.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	repository.fakeLinkCheckRepository.mu.Lock()
	dueCalls := repository.fakeLinkCheckRepository.dueCalls
	repository.fakeLinkCheckRepository.mu.Unlock()
	repository.mu.Lock()
	countCalls := repository.countCalls
	repository.mu.Unlock()
	if dueCalls < 3 || countCalls != 1 {
		t.Fatalf("due calls=%d count calls=%d", dueCalls, countCalls)
	}
}

func TestIsFinalDetectionStatusIncludesDetailedFailures(t *testing.T) {
	for _, status := range []DetectionStatus{
		DetectionValid,
		DetectionInvalid,
		DetectionExpired,
		DetectionCancelled,
		DetectionViolation,
		DetectionLocked,
		DetectionUnknown,
		DetectionUnsupported,
	} {
		if !isFinalDetectionStatus(status) {
			t.Fatalf("isFinalDetectionStatus(%q) = false", status)
		}
	}
}
