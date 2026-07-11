package collection

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeLinkCheckRepository struct {
	mu      sync.Mutex
	results []LinkCheckResult
	done    chan struct{}
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
