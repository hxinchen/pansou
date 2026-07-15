package collection

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

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

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	queued  map[int64]struct{}
	wg      sync.WaitGroup
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
	ctx, cancel := context.WithCancel(parent)
	q.cancel = cancel
	q.started = true
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
	if !q.started {
		q.mu.Unlock()
		return nil
	}
	q.started = false
	q.cancel()
	q.mu.Unlock()

	done := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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
	candidates, err := q.repository.DueLinkChecks(ctx, q.config.BatchSize, q.config.Now().UTC())
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

	result := LinkCheckResult{
		ResourceID: candidate.ResourceID,
		Status:     status,
		CheckedAt:  q.config.Now().UTC(),
	}
	if checkErr != nil {
		result.Error = checkErr.Error()
	}
	if err := q.repository.CompleteLinkCheck(parent, result); err != nil {
		q.report(fmt.Errorf("complete link check for resource %d: %w", candidate.ResourceID, err))
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
