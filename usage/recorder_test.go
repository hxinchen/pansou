package usage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeUsageRepository struct {
	mu      sync.Mutex
	batches [][]UsageEvent
	write   func(context.Context, []UsageEvent) error
}

func (r *fakeUsageRepository) WriteUsage(ctx context.Context, events []UsageEvent) error {
	copyEvents := append([]UsageEvent(nil), events...)
	if r.write != nil {
		if err := r.write(ctx, copyEvents); err != nil {
			return err
		}
	}
	r.mu.Lock()
	r.batches = append(r.batches, copyEvents)
	r.mu.Unlock()
	return nil
}

func recorderTestConfig() RecorderConfig {
	config := DefaultRecorderConfig()
	config.QueueSize = 8
	config.BatchSize = 2
	config.FlushInterval = time.Hour
	config.WriteTimeout = time.Second
	return config
}

func TestRecorderBatchesAsynchronouslyAndSetsTimestamp(t *testing.T) {
	written := make(chan struct{}, 1)
	repository := &fakeUsageRepository{write: func(context.Context, []UsageEvent) error {
		written <- struct{}{}
		return nil
	}}
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	config := recorderTestConfig()
	config.Now = func() time.Time { return now }
	recorder := NewRecorder(repository, config)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := recorder.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if !recorder.Record(UsageEvent{UserID: "user", Route: "/search"}) || !recorder.Record(UsageEvent{UserID: "user", Route: "/check"}) {
		t.Fatal("events were unexpectedly dropped")
	}
	select {
	case <-written:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := recorder.Close(ctx); err != nil {
		t.Fatal(err)
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if len(repository.batches) != 1 || len(repository.batches[0]) != 2 {
		t.Fatalf("written batches = %+v", repository.batches)
	}
	for _, event := range repository.batches[0] {
		if !event.OccurredAt.Equal(now) {
			t.Fatalf("event timestamp = %v", event.OccurredAt)
		}
	}
}

func TestRecorderGracefulCloseDrainsFinalBatch(t *testing.T) {
	repository := &fakeUsageRepository{}
	config := recorderTestConfig()
	config.BatchSize = 10
	recorder := NewRecorder(repository, config)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := recorder.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if !recorder.Record(UsageEvent{UserID: "user"}) {
			t.Fatal("event was unexpectedly dropped")
		}
	}
	if err := recorder.Close(ctx); err != nil {
		t.Fatal(err)
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if len(repository.batches) != 1 || len(repository.batches[0]) != 3 {
		t.Fatalf("final batches = %+v", repository.batches)
	}
}

func TestRecorderFlushesOnConfiguredInterval(t *testing.T) {
	written := make(chan struct{}, 1)
	repository := &fakeUsageRepository{write: func(context.Context, []UsageEvent) error {
		written <- struct{}{}
		return nil
	}}
	config := recorderTestConfig()
	config.BatchSize = 10
	config.FlushInterval = 5 * time.Millisecond
	recorder := NewRecorder(repository, config)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := recorder.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if !recorder.Record(UsageEvent{UserID: "user"}) {
		t.Fatal("event was unexpectedly dropped")
	}
	select {
	case <-written:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := recorder.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRecorderBoundedQueueCountsDrops(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	repository := &fakeUsageRepository{write: func(ctx context.Context, _ []UsageEvent) error {
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	config := recorderTestConfig()
	config.QueueSize = 1
	config.BatchSize = 1
	recorder := NewRecorder(repository, config)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := recorder.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if !recorder.Record(UsageEvent{UserID: "first"}) {
		t.Fatal("first event was dropped")
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if !recorder.Record(UsageEvent{UserID: "second"}) {
		t.Fatal("second event should fill the queue")
	}
	if recorder.Record(UsageEvent{UserID: "third"}) {
		t.Fatal("third event should be dropped")
	}
	if recorder.Dropped() != 1 {
		t.Fatalf("dropped = %d, want 1", recorder.Dropped())
	}
	close(release)
	if err := recorder.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRecorderFinalWriteFailureIsReturnedAndCounted(t *testing.T) {
	writeErr := errors.New("database unavailable")
	repository := &fakeUsageRepository{write: func(context.Context, []UsageEvent) error { return writeErr }}
	config := recorderTestConfig()
	config.BatchSize = 10
	errorsSeen := make(chan error, 1)
	config.OnError = func(err error) { errorsSeen <- err }
	recorder := NewRecorder(repository, config)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := recorder.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if !recorder.Record(UsageEvent{UserID: "user"}) {
		t.Fatal("event was dropped before final flush")
	}
	if err := recorder.Close(ctx); !errors.Is(err, writeErr) {
		t.Fatalf("Close error = %v", err)
	}
	if recorder.Dropped() != 1 {
		t.Fatalf("dropped = %d, want 1", recorder.Dropped())
	}
	select {
	case <-errorsSeen:
	default:
		t.Fatal("write error was not reported")
	}
}

func TestRecorderRunsConfiguredCleanup(t *testing.T) {
	cleaned := make(chan struct{}, 1)
	config := recorderTestConfig()
	config.CleanupInterval = 5 * time.Millisecond
	config.CleanupTimeout = time.Second
	config.Cleanup = func(context.Context) error {
		select {
		case cleaned <- struct{}{}:
		default:
		}
		return nil
	}
	recorder := NewRecorder(&fakeUsageRepository{}, config)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := recorder.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cleaned:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := recorder.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRecorderRejectsBeforeStartAndAfterClose(t *testing.T) {
	recorder := NewRecorder(&fakeUsageRepository{}, recorderTestConfig())
	if recorder.Record(UsageEvent{}) || recorder.Dropped() != 1 {
		t.Fatal("record before Start should be dropped")
	}
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Start(context.Background()); !errors.Is(err, ErrRecorderClosed) {
		t.Fatalf("Start after Close error = %v", err)
	}
	if recorder.Record(UsageEvent{}) || recorder.Dropped() != 2 {
		t.Fatal("record after Close should be dropped")
	}
}
