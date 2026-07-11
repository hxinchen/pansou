package usage

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrRecorderClosed = errors.New("usage recorder is closed")
)

type UsageEvent struct {
	UserID     string                 `json:"user_id"`
	APIKeyID   string                 `json:"api_key_id,omitempty"`
	RequestID  string                 `json:"request_id,omitempty"`
	Method     string                 `json:"method,omitempty"`
	Route      string                 `json:"route,omitempty"`
	StatusCode int                    `json:"status_code,omitempty"`
	OccurredAt time.Time              `json:"occurred_at"`
	Duration   time.Duration          `json:"duration,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

type UsageRepository interface {
	WriteUsage(context.Context, []UsageEvent) error
}

type UsageRepositoryFunc func(context.Context, []UsageEvent) error

func (f UsageRepositoryFunc) WriteUsage(ctx context.Context, events []UsageEvent) error {
	return f(ctx, events)
}

type CleanupFunc func(context.Context) error

type RecorderConfig struct {
	QueueSize       int
	BatchSize       int
	FlushInterval   time.Duration
	WriteTimeout    time.Duration
	Cleanup         CleanupFunc
	CleanupInterval time.Duration
	CleanupTimeout  time.Duration
	Now             func() time.Time
	OnError         func(error)
}

func DefaultRecorderConfig() RecorderConfig {
	return RecorderConfig{
		QueueSize:       1024,
		BatchSize:       100,
		FlushInterval:   time.Second,
		WriteTimeout:    5 * time.Second,
		CleanupInterval: 24 * time.Hour,
		CleanupTimeout:  30 * time.Second,
		Now:             time.Now,
	}
}

// Recorder accepts usage events without blocking request handling. A full or
// stopped queue drops the event and increments Dropped.
type Recorder struct {
	repository UsageRepository
	config     RecorderConfig
	queue      chan UsageEvent

	mu        sync.RWMutex
	started   bool
	accepting bool
	closing   bool
	finished  bool
	stop      chan struct{}
	done      chan struct{}
	cancel    context.CancelFunc
	finishErr error
	doneOnce  sync.Once

	dropped atomic.Uint64
}

func NewRecorder(repository UsageRepository, config RecorderConfig) *Recorder {
	config = normalizeRecorderConfig(config)
	return &Recorder{
		repository: repository,
		config:     config,
		queue:      make(chan UsageEvent, config.QueueSize),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
}

func (r *Recorder) Start(parent context.Context) error {
	if r.repository == nil {
		return errors.New("usage repository is nil")
	}
	if parent == nil {
		parent = context.Background()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished || r.closing {
		return ErrRecorderClosed
	}
	if r.started {
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	r.cancel = cancel
	r.started = true
	r.accepting = true
	go r.run(ctx)
	return nil
}

// Record performs a non-blocking enqueue. It returns false when the event was
// dropped because the queue is full or the recorder is not accepting events.
func (r *Recorder) Record(event UsageEvent) bool {
	r.mu.RLock()
	if !r.accepting {
		r.mu.RUnlock()
		r.dropped.Add(1)
		return false
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = r.config.Now().UTC()
	}
	event.Metadata = cloneMetadata(event.Metadata)
	select {
	case r.queue <- event:
		r.mu.RUnlock()
		return true
	default:
		r.mu.RUnlock()
		r.dropped.Add(1)
		return false
	}
}

// Close stops new records, drains the bounded queue, flushes the final batch,
// and waits until the worker exits or ctx expires.
func (r *Recorder) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if !r.started {
		if !r.finished {
			r.accepting = false
			r.closing = true
			r.finished = true
			r.doneOnce.Do(func() { close(r.done) })
		}
		err := r.finishErr
		r.mu.Unlock()
		return err
	}
	if !r.closing {
		r.accepting = false
		r.closing = true
		close(r.stop)
	}
	done := r.done
	cancel := r.cancel
	r.mu.Unlock()

	select {
	case <-done:
		r.mu.RLock()
		err := r.finishErr
		r.mu.RUnlock()
		return err
	case <-ctx.Done():
		cancel()
		return ctx.Err()
	}
}

func (r *Recorder) Dropped() uint64 {
	return r.dropped.Load()
}

func (r *Recorder) QueueLen() int {
	return len(r.queue)
}

func (r *Recorder) run(ctx context.Context) {
	flushTicker := time.NewTicker(r.config.FlushInterval)
	defer flushTicker.Stop()

	var cleanupTicker *time.Ticker
	var cleanupC <-chan time.Time
	if r.config.Cleanup != nil {
		cleanupTicker = time.NewTicker(r.config.CleanupInterval)
		cleanupC = cleanupTicker.C
		defer cleanupTicker.Stop()
	}

	batch := make([]UsageEvent, 0, r.config.BatchSize)
	for {
		select {
		case event := <-r.queue:
			batch = append(batch, event)
			if len(batch) >= r.config.BatchSize {
				_ = r.flush(&batch)
			}
		case <-flushTicker.C:
			_ = r.flush(&batch)
		case <-cleanupC:
			r.cleanup()
		case <-r.stop:
			r.finish(r.drainAndFlush(&batch))
			return
		case <-ctx.Done():
			r.stopAccepting()
			r.finish(r.drainAndFlush(&batch))
			return
		}
	}
}

func (r *Recorder) drainAndFlush(batch *[]UsageEvent) error {
	var result error
	for {
		select {
		case event := <-r.queue:
			*batch = append(*batch, event)
			if len(*batch) >= r.config.BatchSize {
				if err := r.flush(batch); err != nil {
					result = errors.Join(result, err)
				}
			}
		default:
			return errors.Join(result, r.flush(batch))
		}
	}
}

func (r *Recorder) flush(batch *[]UsageEvent) error {
	if len(*batch) == 0 {
		return nil
	}
	events := append([]UsageEvent(nil), (*batch)...)
	*batch = (*batch)[:0]
	ctx, cancel := context.WithTimeout(context.Background(), r.config.WriteTimeout)
	err := r.repository.WriteUsage(ctx, events)
	cancel()
	if err != nil {
		r.dropped.Add(uint64(len(events)))
		r.report(fmt.Errorf("write usage batch of %d events: %w", len(events), err))
	}
	return err
}

func (r *Recorder) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), r.config.CleanupTimeout)
	err := r.config.Cleanup(ctx)
	cancel()
	if err != nil {
		r.report(fmt.Errorf("cleanup usage events: %w", err))
	}
}

func (r *Recorder) stopAccepting() {
	r.mu.Lock()
	r.accepting = false
	r.closing = true
	r.mu.Unlock()
}

func (r *Recorder) finish(err error) {
	r.mu.Lock()
	r.accepting = false
	r.closing = true
	r.finished = true
	r.finishErr = err
	r.mu.Unlock()
	r.doneOnce.Do(func() { close(r.done) })
}

func (r *Recorder) report(err error) {
	if err != nil && r.config.OnError != nil {
		r.config.OnError(err)
	}
}

func normalizeRecorderConfig(config RecorderConfig) RecorderConfig {
	defaults := DefaultRecorderConfig()
	if config.QueueSize <= 0 {
		config.QueueSize = defaults.QueueSize
	}
	if config.BatchSize <= 0 {
		config.BatchSize = defaults.BatchSize
	}
	if config.FlushInterval <= 0 {
		config.FlushInterval = defaults.FlushInterval
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = defaults.WriteTimeout
	}
	if config.CleanupInterval <= 0 {
		config.CleanupInterval = defaults.CleanupInterval
	}
	if config.CleanupTimeout <= 0 {
		config.CleanupTimeout = defaults.CleanupTimeout
	}
	if config.Now == nil {
		config.Now = defaults.Now
	}
	return config
}

func cloneMetadata(metadata map[string]interface{}) map[string]interface{} {
	if metadata == nil {
		return nil
	}
	cloned := make(map[string]interface{}, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}
