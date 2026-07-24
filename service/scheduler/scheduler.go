package scheduler

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrOverloaded  = errors.New("search scheduler overloaded")
	ErrCircuitOpen = errors.New("search source circuit is open")
)

type Class string

const (
	ClassTG         Class = "tg"
	ClassPlugin     Class = "plugin"
	ClassCredential Class = "credential_plugin"
)

type Priority int

const (
	PriorityBackground Priority = iota
	PriorityCollection
	PriorityInteractive
)

type Config struct {
	Enabled           bool
	ActiveSearches    int
	SearchQueue       int
	TGWorkers         int
	PluginWorkers     int
	CredentialWorkers int
	PerSource         int
	CircuitFailures   int
	CircuitCooldown   time.Duration
}

type Task struct {
	Source string
	Run    func(context.Context) Result
}

type SourcePolicy struct {
	MaxConcurrency  int
	Timeout         time.Duration
	CircuitFailures int
	CircuitCooldown time.Duration
}

type Result struct {
	Source      string
	Value       any
	Err         error
	Skipped     bool
	Duration    time.Duration
	ResultCount int
	UniqueCount int
}

type Scheduler struct {
	config           Config
	active           chan struct{}
	background       chan struct{}
	waiting          atomic.Int64
	classes          map[Class]chan struct{}
	classRuns        map[Class]*atomic.Uint64
	gatesMu          sync.Mutex
	gates            map[string]*sourceGate
	totalSearches    atomic.Uint64
	rejectedSearches atomic.Uint64
	peakActive       atomic.Int64
}

type sourceGate struct {
	limiter         *resizableSemaphore
	mu              sync.Mutex
	failStreak      int
	breakUntil      time.Time
	halfOpen        bool
	runs            uint64
	failures        uint64
	skipped         uint64
	totalDuration   time.Duration
	maxDuration     time.Duration
	durations       []time.Duration
	timeouts        uint64
	rateLimited     uint64
	resultCount     uint64
	uniqueCount     uint64
	waiting         atomic.Int64
	timeout         time.Duration
	circuitFailures int
	circuitCooldown time.Duration
}

type resizableSemaphore struct {
	mu      sync.Mutex
	limit   int
	inUse   int
	changed chan struct{}
}

func newResizableSemaphore(limit int) *resizableSemaphore {
	if limit <= 0 {
		limit = 1
	}
	return &resizableSemaphore{limit: limit, changed: make(chan struct{})}
}

func (s *resizableSemaphore) Acquire(ctx context.Context) error {
	for {
		s.mu.Lock()
		if s.inUse < s.limit {
			s.inUse++
			s.mu.Unlock()
			return nil
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *resizableSemaphore) Release() {
	s.mu.Lock()
	if s.inUse > 0 {
		s.inUse--
	}
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
}

func (s *resizableSemaphore) SetLimit(limit int) {
	if limit <= 0 {
		return
	}
	s.mu.Lock()
	s.limit = limit
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
}

func (s *resizableSemaphore) Snapshot() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inUse, s.limit
}

type Admission struct {
	s          *Scheduler
	background bool
	once       sync.Once
}

type Snapshot struct {
	Enabled          bool                    `json:"enabled"`
	Active           int                     `json:"active"`
	ActiveLimit      int                     `json:"active_limit"`
	Waiting          int64                   `json:"waiting"`
	QueueLimit       int                     `json:"queue_limit"`
	TotalSearches    uint64                  `json:"total_searches"`
	RejectedSearches uint64                  `json:"rejected_searches"`
	PeakActive       int64                   `json:"peak_active"`
	Classes          map[string]PoolSnapshot `json:"classes"`
	Sources          []SourceSnapshot        `json:"sources"`
}

type PoolSnapshot struct {
	InUse int    `json:"in_use"`
	Limit int    `json:"limit"`
	Runs  uint64 `json:"runs"`
}

type SourceSnapshot struct {
	Source          string     `json:"source"`
	InUse           int        `json:"in_use"`
	Limit           int        `json:"limit"`
	Runs            uint64     `json:"runs"`
	Failures        uint64     `json:"failures"`
	Skipped         uint64     `json:"skipped"`
	Timeouts        uint64     `json:"timeouts"`
	RateLimited     uint64     `json:"rate_limited"`
	ResultCount     uint64     `json:"result_count"`
	UniqueCount     uint64     `json:"unique_count"`
	FailStreak      int        `json:"fail_streak"`
	CircuitOpen     bool       `json:"circuit_open"`
	BreakUntil      *time.Time `json:"break_until,omitempty"`
	AverageMS       int64      `json:"average_ms"`
	TotalDurationMS uint64     `json:"total_duration_ms"`
	P50MS           int64      `json:"p50_ms"`
	P95MS           int64      `json:"p95_ms"`
	MaxMS           int64      `json:"max_ms"`
	Waiting         int64      `json:"waiting"`
}

func New(config Config) *Scheduler {
	config = normalize(config)
	return &Scheduler{
		config:     config,
		active:     make(chan struct{}, config.ActiveSearches),
		background: make(chan struct{}, max(1, config.ActiveSearches-2)),
		classes: map[Class]chan struct{}{
			ClassTG:         make(chan struct{}, config.TGWorkers),
			ClassPlugin:     make(chan struct{}, config.PluginWorkers),
			ClassCredential: make(chan struct{}, config.CredentialWorkers),
		},
		classRuns: map[Class]*atomic.Uint64{ClassTG: {}, ClassPlugin: {}, ClassCredential: {}},
		gates:     make(map[string]*sourceGate),
	}
}

func normalize(config Config) Config {
	if config.ActiveSearches <= 0 {
		config.ActiveSearches = 8
	}
	if config.SearchQueue <= 0 {
		config.SearchQueue = 100
	}
	if config.TGWorkers <= 0 {
		config.TGWorkers = 32
	}
	if config.PluginWorkers <= 0 {
		config.PluginWorkers = 32
	}
	if config.CredentialWorkers <= 0 {
		config.CredentialWorkers = 16
	}
	if config.PerSource <= 0 {
		config.PerSource = 2
	}
	if config.CircuitFailures <= 0 {
		config.CircuitFailures = 5
	}
	if config.CircuitCooldown <= 0 {
		config.CircuitCooldown = 5 * time.Minute
	}
	return config
}

func (s *Scheduler) Acquire(ctx context.Context, priority Priority) (*Admission, error) {
	if s == nil || !s.config.Enabled {
		return &Admission{}, nil
	}
	isBackground := priority != PriorityInteractive
	if isBackground {
		select {
		case s.background <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			s.rejectedSearches.Add(1)
			return nil, ErrOverloaded
		}
	}
	releaseBackground := true
	defer func() {
		if releaseBackground && isBackground {
			release(s.background)
		}
	}()
	select {
	case s.active <- struct{}{}:
		s.totalSearches.Add(1)
		recordPeak(&s.peakActive, int64(len(s.active)))
		releaseBackground = false
		return &Admission{s: s, background: isBackground}, nil
	default:
	}
	waiting := s.waiting.Add(1)
	if waiting > int64(s.config.SearchQueue) {
		s.waiting.Add(-1)
		s.rejectedSearches.Add(1)
		return nil, ErrOverloaded
	}
	defer s.waiting.Add(-1)
	select {
	case s.active <- struct{}{}:
		s.totalSearches.Add(1)
		recordPeak(&s.peakActive, int64(len(s.active)))
		releaseBackground = false
		return &Admission{s: s, background: isBackground}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (a *Admission) Release() {
	if a == nil || a.s == nil {
		return
	}
	a.once.Do(func() {
		<-a.s.active
		if a.background {
			release(a.s.background)
		}
	})
}

func (s *Scheduler) Execute(ctx context.Context, class Class, perRequest int, timeout time.Duration, tasks []Task) []Result {
	if len(tasks) == 0 {
		return []Result{}
	}
	if s == nil || !s.config.Enabled {
		return executeUnscheduled(ctx, perRequest, timeout, tasks)
	}
	if perRequest <= 0 || perRequest > len(tasks) {
		perRequest = len(tasks)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	local := make(chan struct{}, perRequest)
	results := make(chan Result, len(tasks))
	for _, task := range tasks {
		task := task
		go func() {
			if err := acquire(ctx, local); err != nil {
				return
			}
			defer release(local)
			classPool := s.classes[class]
			if err := acquire(ctx, classPool); err != nil {
				return
			}
			defer release(classPool)
			gate := s.gate(task.Source)
			if !gate.allow(time.Now()) {
				gate.recordSkip()
				results <- Result{Source: task.Source, Err: ErrCircuitOpen, Skipped: true}
				return
			}
			gate.waiting.Add(1)
			if err := gate.limiter.Acquire(ctx); err != nil {
				gate.waiting.Add(-1)
				return
			}
			gate.waiting.Add(-1)
			defer gate.limiter.Release()
			taskCtx := ctx
			cancelTask := func() {}
			gate.mu.Lock()
			taskTimeout := gate.timeout
			gate.mu.Unlock()
			if taskTimeout > 0 {
				taskCtx, cancelTask = context.WithTimeout(ctx, taskTimeout)
			}
			start := time.Now()
			if counter := s.classRuns[class]; counter != nil {
				counter.Add(1)
			}
			outcome := task.Run(taskCtx)
			cancelTask()
			outcome.Source = task.Source
			outcome.Duration = time.Since(start)
			gate.record(outcome, s.config.CircuitFailures, s.config.CircuitCooldown)
			select {
			case results <- outcome:
			case <-ctx.Done():
			}
		}()
	}
	collected := make([]Result, 0, len(tasks))
	for len(collected) < len(tasks) {
		select {
		case result := <-results:
			collected = append(collected, result)
		case <-ctx.Done():
			return collected
		}
	}
	return collected
}

func executeUnscheduled(parent context.Context, limit int, timeout time.Duration, tasks []Task) []Result {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	if limit <= 0 || limit > len(tasks) {
		limit = len(tasks)
	}
	sem := make(chan struct{}, limit)
	results := make(chan Result, len(tasks))
	for _, task := range tasks {
		task := task
		go func() {
			if err := acquire(ctx, sem); err != nil {
				return
			}
			defer release(sem)
			start := time.Now()
			outcome := task.Run(ctx)
			outcome.Source, outcome.Duration = task.Source, time.Since(start)
			select {
			case results <- outcome:
			case <-ctx.Done():
			}
		}()
	}
	collected := make([]Result, 0, len(tasks))
	for len(collected) < len(tasks) {
		select {
		case result := <-results:
			collected = append(collected, result)
		case <-ctx.Done():
			return collected
		}
	}
	return collected
}

func acquire(ctx context.Context, semaphore chan struct{}) error {
	if semaphore == nil {
		return nil
	}
	select {
	case semaphore <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func release(semaphore chan struct{}) {
	if semaphore != nil {
		<-semaphore
	}
}

func (s *Scheduler) gate(source string) *sourceGate {
	s.gatesMu.Lock()
	defer s.gatesMu.Unlock()
	gate := s.gates[source]
	if gate == nil {
		gate = &sourceGate{limiter: newResizableSemaphore(s.config.PerSource)}
		s.gates[source] = gate
	}
	return gate
}

func (s *Scheduler) ConfigureSource(source string, policy SourcePolicy) {
	if s == nil || source == "" {
		return
	}
	gate := s.gate(source)
	gate.mu.Lock()
	defer gate.mu.Unlock()
	limit := policy.MaxConcurrency
	if limit <= 0 {
		limit = s.config.PerSource
	}
	gate.limiter.SetLimit(limit)
	gate.timeout = policy.Timeout
	gate.circuitFailures = policy.CircuitFailures
	gate.circuitCooldown = policy.CircuitCooldown
}

func (g *sourceGate) allow(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.breakUntil.IsZero() {
		return true
	}
	if now.Before(g.breakUntil) {
		return false
	}
	if g.halfOpen {
		return false
	}
	g.halfOpen = true
	return true
}

func (g *sourceGate) record(outcome Result, threshold int, cooldown time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.runs++
	duration, err := outcome.Duration, outcome.Err
	if g.circuitFailures > 0 {
		threshold = g.circuitFailures
	}
	if g.circuitCooldown > 0 {
		cooldown = g.circuitCooldown
	}
	g.totalDuration += duration
	g.resultCount += uint64(max(0, outcome.ResultCount))
	g.uniqueCount += uint64(max(0, outcome.UniqueCount))
	g.durations = append(g.durations, duration)
	if len(g.durations) > 256 {
		copy(g.durations, g.durations[len(g.durations)-256:])
		g.durations = g.durations[:256]
	}
	if duration > g.maxDuration {
		g.maxDuration = duration
	}
	if err == nil {
		g.failStreak = 0
		g.breakUntil = time.Time{}
		g.halfOpen = false
		return
	}
	g.failures++
	message := strings.ToLower(err.Error())
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(message, "timeout") || strings.Contains(message, "超时") {
		g.timeouts++
	}
	if strings.Contains(message, "429") || strings.Contains(message, "rate limit") || strings.Contains(message, "限流") {
		g.rateLimited++
	}
	g.failStreak++
	g.halfOpen = false
	if g.failStreak >= threshold {
		multiplier := g.failStreak - threshold
		if multiplier > 2 {
			multiplier = 2
		}
		g.breakUntil = time.Now().Add(cooldown * time.Duration(1<<multiplier))
	}
}

func (g *sourceGate) recordSkip() { g.mu.Lock(); g.skipped++; g.mu.Unlock() }

func (s *Scheduler) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{}
	}
	snapshot := Snapshot{Enabled: s.config.Enabled, Active: len(s.active), ActiveLimit: cap(s.active), Waiting: s.waiting.Load(), QueueLimit: s.config.SearchQueue, TotalSearches: s.totalSearches.Load(), RejectedSearches: s.rejectedSearches.Load(), PeakActive: s.peakActive.Load(), Classes: make(map[string]PoolSnapshot), Sources: make([]SourceSnapshot, 0)}
	for class, pool := range s.classes {
		runs := uint64(0)
		if counter := s.classRuns[class]; counter != nil {
			runs = counter.Load()
		}
		snapshot.Classes[string(class)] = PoolSnapshot{InUse: len(pool), Limit: cap(pool), Runs: runs}
	}
	s.gatesMu.Lock()
	for source, gate := range s.gates {
		gate.mu.Lock()
		inUse, limit := gate.limiter.Snapshot()
		item := SourceSnapshot{Source: source, InUse: inUse, Limit: limit, Runs: gate.runs, Failures: gate.failures, Skipped: gate.skipped, Timeouts: gate.timeouts, RateLimited: gate.rateLimited, ResultCount: gate.resultCount, UniqueCount: gate.uniqueCount, FailStreak: gate.failStreak, TotalDurationMS: uint64(max(int64(0), gate.totalDuration.Milliseconds())), MaxMS: gate.maxDuration.Milliseconds(), Waiting: gate.waiting.Load()}
		if gate.runs > 0 {
			item.AverageMS = (gate.totalDuration / time.Duration(gate.runs)).Milliseconds()
		}
		if len(gate.durations) > 0 {
			samples := append([]time.Duration(nil), gate.durations...)
			sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
			item.P50MS = percentileDuration(samples, 0.50).Milliseconds()
			item.P95MS = percentileDuration(samples, 0.95).Milliseconds()
		}
		if !gate.breakUntil.IsZero() && time.Now().Before(gate.breakUntil) {
			until := gate.breakUntil
			item.CircuitOpen, item.BreakUntil = true, &until
		}
		gate.mu.Unlock()
		snapshot.Sources = append(snapshot.Sources, item)
	}
	s.gatesMu.Unlock()
	sort.Slice(snapshot.Sources, func(i, j int) bool { return snapshot.Sources[i].Source < snapshot.Sources[j].Source })
	return snapshot
}

func recordPeak(peak *atomic.Int64, value int64) {
	for current := peak.Load(); value > current; current = peak.Load() {
		if peak.CompareAndSwap(current, value) {
			return
		}
	}
}

func percentileDuration(sorted []time.Duration, percentile float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	index := int(float64(len(sorted)-1) * percentile)
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}
