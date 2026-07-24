package scheduler

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{Enabled: true, ActiveSearches: 2, SearchQueue: 2, TGWorkers: 2, PluginWorkers: 2, CredentialWorkers: 1, PerSource: 1, CircuitFailures: 2, CircuitCooldown: time.Minute}
}

func TestAdmissionRejectsWhenQueueIsFull(t *testing.T) {
	config := testConfig()
	config.ActiveSearches = 1
	config.SearchQueue = 1
	s := New(config)
	first, err := s.Acquire(context.Background(), PriorityInteractive)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()

	acquired := make(chan *Admission, 1)
	go func() { admission, _ := s.Acquire(context.Background(), PriorityInteractive); acquired <- admission }()
	deadline := time.Now().Add(time.Second)
	for s.Snapshot().Waiting != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if _, err := s.Acquire(context.Background(), PriorityInteractive); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("expected overload, got %v", err)
	}
	first.Release()
	second := <-acquired
	if second == nil {
		t.Fatal("queued search was not admitted")
	}
	second.Release()
}

func TestPerSourceBulkheadIsSharedAcrossTasks(t *testing.T) {
	s := New(testConfig())
	var current atomic.Int64
	var peak atomic.Int64
	tasks := make([]Task, 4)
	for i := range tasks {
		tasks[i] = Task{Source: "plugin:slow", Run: func(context.Context) Result {
			now := current.Add(1)
			for old := peak.Load(); now > old && !peak.CompareAndSwap(old, now); old = peak.Load() {
			}
			time.Sleep(10 * time.Millisecond)
			current.Add(-1)
			return Result{}
		}}
	}
	results := s.Execute(context.Background(), ClassPlugin, 4, time.Second, tasks)
	if len(results) != len(tasks) {
		t.Fatalf("results=%d", len(results))
	}
	if peak.Load() != 1 {
		t.Fatalf("peak source concurrency=%d", peak.Load())
	}
	snapshot := s.Snapshot()
	if len(snapshot.Sources) != 1 || snapshot.Sources[0].Runs != 4 || snapshot.Sources[0].P50MS <= 0 || snapshot.Sources[0].P95MS <= 0 {
		t.Fatalf("source metrics=%#v", snapshot.Sources)
	}
	if snapshot.Sources[0].TotalDurationMS == 0 {
		t.Fatalf("total duration was not exposed: %#v", snapshot.Sources[0])
	}
}

func TestCircuitBreakerSkipsAfterFailureThreshold(t *testing.T) {
	s := New(testConfig())
	runs := 0
	task := Task{Source: "plugin:broken", Run: func(context.Context) Result { runs++; return Result{Err: errors.New("boom")} }}
	for i := 0; i < 2; i++ {
		results := s.Execute(context.Background(), ClassPlugin, 1, time.Second, []Task{task})
		if len(results) != 1 || results[0].Skipped {
			t.Fatalf("failure %d was unexpectedly skipped", i)
		}
	}
	results := s.Execute(context.Background(), ClassPlugin, 1, time.Second, []Task{task})
	if len(results) != 1 || !results[0].Skipped || !errors.Is(results[0].Err, ErrCircuitOpen) {
		t.Fatalf("expected circuit-open result: %#v", results)
	}
	if runs != 2 {
		t.Fatalf("runs=%d", runs)
	}
}

func TestBackgroundAdmissionLeavesInteractiveCapacity(t *testing.T) {
	config := testConfig()
	config.ActiveSearches = 3
	s := New(config)
	var admissions []*Admission
	for i := 0; i < 1; i++ {
		admission, err := s.Acquire(context.Background(), PriorityCollection)
		if err != nil {
			t.Fatal(err)
		}
		admissions = append(admissions, admission)
	}
	if _, err := s.Acquire(context.Background(), PriorityCollection); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("expected background reservation overload, got %v", err)
	}
	interactive, err := s.Acquire(context.Background(), PriorityInteractive)
	if err != nil {
		t.Fatalf("interactive admission: %v", err)
	}
	interactive.Release()
	for _, admission := range admissions {
		admission.Release()
	}
}

func TestConfiguredSourceLimit(t *testing.T) {
	s := New(testConfig())
	s.ConfigureSource("plugin:wide", SourcePolicy{MaxConcurrency: 2})
	var current atomic.Int64
	var peak atomic.Int64
	var wg sync.WaitGroup
	gate := make(chan struct{})
	tasks := make([]Task, 2)
	for i := range tasks {
		tasks[i] = Task{Source: "plugin:wide", Run: func(context.Context) Result {
			now := current.Add(1)
			for old := peak.Load(); now > old && !peak.CompareAndSwap(old, now); old = peak.Load() {
			}
			wg.Done()
			<-gate
			current.Add(-1)
			return Result{}
		}}
		wg.Add(1)
	}
	done := make(chan []Result, 1)
	go func() { done <- s.Execute(context.Background(), ClassPlugin, 2, time.Second, tasks) }()
	wg.Wait()
	close(gate)
	<-done
	if peak.Load() != 2 {
		t.Fatalf("peak=%d", peak.Load())
	}
}

func TestConfiguredSourceLimitCanReturnToGlobalDefault(t *testing.T) {
	s := New(testConfig())
	s.ConfigureSource("plugin:reset", SourcePolicy{MaxConcurrency: 2})
	s.ConfigureSource("plugin:reset", SourcePolicy{MaxConcurrency: 0})
	snapshot := s.Snapshot()
	if len(snapshot.Sources) != 1 || snapshot.Sources[0].Limit != testConfig().PerSource {
		t.Fatalf("source snapshot=%#v", snapshot.Sources)
	}
}
