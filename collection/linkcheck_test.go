package collection

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeLinkCheckRepository struct {
	mu       sync.Mutex
	results  []LinkCheckResult
	due      []LinkCheckCandidate
	dueCalls int
	dueAfter int
	dueOnce  bool
	dueErr   error
	done     chan struct{}
}

func (f *fakeLinkCheckRepository) CompleteLinkCheck(_ context.Context, result LinkCheckResult) error {
	f.mu.Lock()
	f.results = append(f.results, result)
	f.mu.Unlock()
	select {
	case f.done <- struct{}{}:
	default:
	}
	return nil
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
