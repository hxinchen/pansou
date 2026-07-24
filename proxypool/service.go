package proxypool

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"pansou/storage"
	"pansou/util"
)

var (
	ErrDisabled = errors.New("proxy pool routing is disabled")
	ErrNoProxy  = errors.New("no healthy proxy is available")
)

const (
	ModeBaselineOnly  = "baseline_only"
	ModeBaselineFirst = "baseline_first"
	ModeProxyFirst    = "proxy_first"
	ModeProxyOnly     = "proxy_only"
	ModeStickyProxy   = "sticky_proxy"
)

type Config struct {
	Enabled           bool
	HealthEnabled     bool
	HealthWorkers     int
	ProbeTimeout      time.Duration
	ProbeInterval     time.Duration
	NodeRefresh       time.Duration
	FailureThreshold  int
	Cooldown          time.Duration
	CooldownMax       time.Duration
	CooldownJitter    time.Duration
	MaxHotNodes       int
	MaxPerNode        int
	MaxAttempts       int
	StickyTTL         time.Duration
	StickyMaxEntries  int
	SelectionStrategy string
	ProbeURLs         []string
}

func DefaultConfig() Config {
	return Config{HealthEnabled: true, HealthWorkers: 16, ProbeTimeout: 10 * time.Second, ProbeInterval: 30 * time.Second,
		NodeRefresh: 30 * time.Second, FailureThreshold: 3, Cooldown: 5 * time.Minute, MaxHotNodes: 1000, MaxPerNode: 2,
		CooldownMax: 30 * time.Minute, CooldownJitter: 30 * time.Second, MaxAttempts: 3, StickyTTL: time.Hour, StickyMaxEntries: 100000,
		SelectionStrategy: SelectionLeastScore,
		ProbeURLs:         []string{"https://www.baidu.com/robots.txt", "https://www.google.com/generate_204"}}
}

type Repository interface {
	ImportProxyNodes(context.Context, storage.ProxyImportInput, []storage.ProxyNodeInput) (storage.ProxyImportResult, error)
	ListProxyNodes(context.Context, storage.ProxyNodeFilter) (storage.ProxyNodePage, error)
	GetProxyNode(context.Context, int64) (storage.ProxyNode, error)
	ListRuntimeProxyNodes(context.Context, time.Time, int) ([]storage.ProxyNode, error)
	ListRuntimeProxyTargetStats(context.Context, []int64) ([]storage.ProxyTargetStat, error)
	ListProxyProbeCandidates(context.Context, time.Time, time.Time, int) ([]storage.ProxyNode, error)
	RecordProxyProbe(context.Context, int64, bool, time.Duration, int, time.Duration) error
	RecordProxyOutcome(context.Context, storage.ProxyOutcomeRecord) error
	SetProxyNodeEnabled(context.Context, int64, bool) error
	DeleteProxyNode(context.Context, int64) error
	SetProxyBatchEnabled(context.Context, int64, bool) error
	ListProxyBatches(context.Context, int, int) ([]storage.ProxyImportBatch, int64, error)
	ProxyPoolSummary(context.Context, time.Time) (storage.ProxyPoolSummary, error)
	ListProxyPolicies(context.Context) ([]storage.ProxyPolicy, error)
	ReplaceProxyPolicies(context.Context, []storage.ProxyPolicy) error
}

type ImportRequest struct {
	Name           string
	SourceFilename string
	ExpiresAt      time.Time
}

type ProxyRequest struct {
	TargetType string
	TargetKey  string
	StickyKey  string
	ExcludeIDs map[int64]struct{}
}

const (
	FailureScopeNone   = "none"
	FailureScopeNode   = "node"
	FailureScopeTarget = "target"
)

type ProxyOutcome struct {
	Success      bool
	FailureScope string
	RetryAfter   time.Duration
	Latency      time.Duration
}

type Lease struct {
	service  *Service
	nodeID   int64
	target   string
	url      string
	started  time.Time
	released sync.Once
}

func (l *Lease) URL() string {
	if l == nil {
		return ""
	}
	return l.url
}

func (l *Lease) ID() int64 {
	if l == nil {
		return 0
	}
	return l.nodeID
}

func (l *Lease) Release(outcome ProxyOutcome) {
	if l == nil || l.service == nil {
		return
	}
	l.released.Do(func() {
		latency := time.Since(l.started)
		if outcome.Latency > 0 {
			latency = outcome.Latency
		}
		l.service.completeLease(l.nodeID, l.target, outcome, latency)
	})
}

type runtimeTarget struct {
	successCount       int64
	failureCount       int64
	latencyMS          int64
	consecutiveFailure int
	cooldownUntil      time.Time
}

type runtimeProxy struct {
	node     storage.ProxyNode
	url      string
	inflight int
	targets  map[string]*runtimeTarget
}

type stickyEntry struct {
	nodeID    int64
	expiresAt time.Time
	lastUsed  time.Time
}

type Service struct {
	repo              Repository
	cipher            *Cipher
	cfg               Config
	mu                sync.Mutex
	nodes             map[int64]*runtimeProxy
	sticky            map[string]stickyEntry
	policies          map[string]string
	selector          proxySelector
	inUse             int
	probeJobs         int
	started           bool
	cancel            context.CancelFunc
	probeQueue        chan storage.ProxyNode
	queued            map[int64]struct{}
	outcomes          chan storage.ProxyOutcomeRecord
	now               func() time.Time
	jitter            func(time.Duration, time.Duration) time.Duration
	lastStickyCleanup time.Time
}

func NewService(repo Repository, cipher *Cipher, cfg Config) *Service {
	defaults := DefaultConfig()
	if cfg.HealthWorkers <= 0 {
		cfg.HealthWorkers = defaults.HealthWorkers
	}
	if cfg.ProbeTimeout <= 0 {
		cfg.ProbeTimeout = defaults.ProbeTimeout
	}
	if cfg.ProbeInterval <= 0 {
		cfg.ProbeInterval = defaults.ProbeInterval
	}
	if cfg.NodeRefresh <= 0 {
		cfg.NodeRefresh = defaults.NodeRefresh
	}
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = defaults.FailureThreshold
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = defaults.Cooldown
	}
	if cfg.CooldownMax <= 0 {
		cfg.CooldownMax = defaults.CooldownMax
	}
	if cfg.CooldownMax < cfg.Cooldown {
		cfg.CooldownMax = cfg.Cooldown
	}
	if cfg.CooldownJitter < 0 {
		cfg.CooldownJitter = defaults.CooldownJitter
	}
	if cfg.MaxHotNodes <= 0 {
		cfg.MaxHotNodes = defaults.MaxHotNodes
	}
	if cfg.MaxPerNode <= 0 {
		cfg.MaxPerNode = defaults.MaxPerNode
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = defaults.MaxAttempts
	}
	if cfg.StickyTTL <= 0 {
		cfg.StickyTTL = defaults.StickyTTL
	}
	if cfg.StickyMaxEntries <= 0 {
		cfg.StickyMaxEntries = defaults.StickyMaxEntries
	}
	if strings.TrimSpace(cfg.SelectionStrategy) == "" {
		cfg.SelectionStrategy = defaults.SelectionStrategy
	}
	if len(cfg.ProbeURLs) == 0 {
		cfg.ProbeURLs = defaults.ProbeURLs
	}
	return &Service{
		repo: repo, cipher: cipher, cfg: cfg, nodes: make(map[int64]*runtimeProxy), sticky: make(map[string]stickyEntry),
		policies: make(map[string]string), queued: make(map[int64]struct{}), outcomes: make(chan storage.ProxyOutcomeRecord, 4096),
		selector: newProxySelector(cfg.SelectionStrategy), now: time.Now,
		jitter: func(wait, maxJitter time.Duration) time.Duration {
			if wait <= 0 || maxJitter <= 0 {
				return wait
			}
			bound := maxJitter
			if tenPercent := wait / 10; tenPercent > 0 && bound > tenPercent {
				bound = tenPercent
			}
			if bound <= 0 {
				return wait
			}
			return wait + time.Duration(rand.Int63n(int64(bound)+1))
		},
	}
}

func (s *Service) Enabled() bool { return s != nil && s.cfg.Enabled }

func (s *Service) Start(parent context.Context) error {
	if s == nil || s.repo == nil || s.cipher == nil {
		return nil
	}
	if parent == nil {
		parent = context.Background()
	}
	if err := s.Refresh(parent); err != nil {
		return err
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.started = true
	s.probeQueue = make(chan storage.ProxyNode, s.cfg.HealthWorkers*4)
	s.mu.Unlock()
	go s.outcomeLoop(ctx)
	go s.refreshLoop(ctx)
	if s.cfg.HealthEnabled {
		for i := 0; i < s.cfg.HealthWorkers; i++ {
			go s.probeWorker(ctx)
		}
		go s.probeScheduler(ctx)
	}
	return nil
}

func (s *Service) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel := s.cancel
	s.started = false
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Service) Refresh(ctx context.Context) error {
	if s == nil || s.repo == nil || s.cipher == nil {
		return nil
	}
	now := s.now()
	nodes, err := s.repo.ListRuntimeProxyNodes(ctx, now, s.cfg.MaxHotNodes)
	if err != nil {
		return err
	}
	proxyIDs := make([]int64, 0, len(nodes))
	for _, node := range nodes {
		proxyIDs = append(proxyIDs, node.ID)
	}
	targetStats, err := s.repo.ListRuntimeProxyTargetStats(ctx, proxyIDs)
	if err != nil {
		return err
	}
	policies, err := s.repo.ListProxyPolicies(ctx)
	if err != nil {
		return err
	}
	next := make(map[int64]*runtimeProxy, len(nodes))
	for _, node := range nodes {
		raw, decryptErr := s.cipher.Decrypt(node.Ciphertext, node.Nonce, node.Fingerprint)
		if decryptErr != nil {
			continue
		}
		next[node.ID] = &runtimeProxy{node: node, url: raw, targets: make(map[string]*runtimeTarget)}
	}
	for _, stat := range targetStats {
		item := next[stat.ProxyID]
		if item == nil {
			continue
		}
		target := &runtimeTarget{
			successCount: stat.SuccessCount, failureCount: stat.FailureCount, latencyMS: stat.LatencyMS,
			consecutiveFailure: stat.ConsecutiveFailure,
		}
		if stat.CooldownUntil != nil {
			target.cooldownUntil = *stat.CooldownUntil
		}
		item.targets[stat.TargetKey] = target
	}
	s.mu.Lock()
	for id, item := range next {
		old := s.nodes[id]
		if old != nil {
			item.inflight = old.inflight
			if old.node.CooldownUntil != nil && old.node.CooldownUntil.After(now) {
				item.node.Status = old.node.Status
				item.node.ConsecutiveFailure = old.node.ConsecutiveFailure
				cooldownUntil := *old.node.CooldownUntil
				item.node.CooldownUntil = &cooldownUntil
			}
			for targetKey, oldTarget := range old.targets {
				if oldTarget == nil || !oldTarget.cooldownUntil.After(now) {
					continue
				}
				current := item.targets[targetKey]
				if current == nil || current.cooldownUntil.Before(oldTarget.cooldownUntil) {
					copyTarget := *oldTarget
					item.targets[targetKey] = &copyTarget
				}
			}
		}
	}
	for id, old := range s.nodes {
		if _, ok := next[id]; !ok && old != nil && old.inflight > 0 {
			old.node.Status = storage.ProxyStatusCooling
			next[id] = old
		}
	}
	s.nodes = next
	s.policies = make(map[string]string, len(policies))
	for _, policy := range policies {
		s.policies[policy.TargetType+"\x00"+policy.TargetKey] = policy.Mode
	}
	s.cleanupStickyLocked(now, true)
	s.mu.Unlock()
	return nil
}

func (s *Service) refreshLoop(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.NodeRefresh)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Refresh(ctx); err != nil && ctx.Err() == nil {
				log.Printf("proxy pool refresh failed: %v", err)
			}
		}
	}
}

func (s *Service) RouteMode(targetType, targetKey string) string {
	if s == nil {
		return ModeBaselineFirst
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if mode := s.policies[targetType+"\x00"+targetKey]; mode != "" {
		return mode
	}
	if mode := s.policies[targetType+"\x00*"]; mode != "" {
		return mode
	}
	if mode := s.policies["global\x00*"]; mode != "" {
		return mode
	}
	return ModeBaselineFirst
}

func (s *Service) Acquire(ctx context.Context, request ProxyRequest) (*Lease, error) {
	if s == nil || !s.cfg.Enabled {
		return nil, ErrDisabled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target := request.TargetType + ":" + request.TargetKey
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupStickyLocked(now, false)
	var selected *runtimeProxy
	stickyKey := ""
	if request.StickyKey != "" {
		stickyKey = target + "\x00" + request.StickyKey
		if entry, ok := s.sticky[stickyKey]; ok {
			candidate := s.nodes[entry.nodeID]
			if entry.expiresAt.After(now) && s.proxyAvailableLocked(candidate, target, request.ExcludeIDs, now) {
				selected = candidate
				entry.lastUsed = now
				entry.expiresAt = now.Add(s.cfg.StickyTTL)
				s.sticky[stickyKey] = entry
			} else {
				delete(s.sticky, stickyKey)
			}
		}
	}
	if selected == nil {
		candidates := make([]*runtimeProxy, 0, len(s.nodes))
		for _, candidate := range s.nodes {
			if s.proxyAvailableLocked(candidate, target, request.ExcludeIDs, now) {
				candidates = append(candidates, candidate)
			}
		}
		selected = s.selector.Pick(target, candidates)
	}
	if selected == nil {
		return nil, ErrNoProxy
	}
	selected.inflight++
	s.inUse++
	if stickyKey != "" {
		s.sticky[stickyKey] = stickyEntry{nodeID: selected.node.ID, expiresAt: now.Add(s.cfg.StickyTTL), lastUsed: now}
	}
	return &Lease{service: s, nodeID: selected.node.ID, target: target, url: selected.url, started: now}, nil
}

func (s *Service) MaxAttempts() int {
	if s == nil || s.cfg.MaxAttempts <= 0 {
		return 1
	}
	return s.cfg.MaxAttempts
}

func (s *Service) proxyAvailableLocked(node *runtimeProxy, target string, excluded map[int64]struct{}, now time.Time) bool {
	if node == nil || !node.node.Enabled || !node.node.ExpiresAt.After(now) || node.inflight >= s.cfg.MaxPerNode {
		return false
	}
	if node.node.Status != storage.ProxyStatusHealthy && node.node.Status != storage.ProxyStatusCooling {
		return false
	}
	if node.node.CooldownUntil != nil && node.node.CooldownUntil.After(now) {
		return false
	}
	if node.node.Status == storage.ProxyStatusCooling && node.node.CooldownUntil == nil {
		return false
	}
	if _, ok := excluded[node.node.ID]; ok {
		return false
	}
	if state := node.targets[target]; state != nil && state.cooldownUntil.After(now) {
		return false
	}
	return true
}

func (s *Service) completeLease(id int64, target string, outcome ProxyOutcome, latency time.Duration) {
	now := s.now()
	scope := strings.ToLower(strings.TrimSpace(outcome.FailureScope))
	if !outcome.Success && scope == "" {
		scope = FailureScopeNode
	}
	record := storage.ProxyOutcomeRecord{ProxyID: id, TargetKey: target, Success: outcome.Success, FailureScope: scope, Latency: latency}
	s.mu.Lock()
	node := s.nodes[id]
	if node != nil && node.inflight > 0 {
		node.inflight--
	}
	if s.inUse > 0 {
		s.inUse--
	}
	if node != nil {
		if node.targets == nil {
			node.targets = make(map[string]*runtimeTarget)
		}
		targetState := node.targets[target]
		if targetState == nil {
			targetState = &runtimeTarget{}
			node.targets[target] = targetState
		}
		switch {
		case outcome.Success:
			node.node.Status = storage.ProxyStatusHealthy
			node.node.SuccessCount++
			node.node.ConsecutiveFailure = 0
			node.node.CooldownUntil = nil
			if latency > 0 {
				node.node.LatencyMS = latency.Milliseconds()
				targetState.latencyMS = latency.Milliseconds()
			}
			targetState.successCount++
			targetState.consecutiveFailure = 0
			targetState.cooldownUntil = time.Time{}
		case scope == FailureScopeNode:
			node.node.FailureCount++
			node.node.ConsecutiveFailure, node.node.CooldownUntil = s.nextFailureState(node.node.ConsecutiveFailure, node.node.CooldownUntil, outcome.RetryAfter, now)
			record.ConsecutiveFailures = node.node.ConsecutiveFailure
			record.CooldownUntil = copyTime(node.node.CooldownUntil)
			targetState.failureCount++
			targetState.consecutiveFailure, targetState.cooldownUntil = s.nextTargetFailureState(targetState, outcome.RetryAfter, now)
			if latency > 0 {
				node.node.LatencyMS = latency.Milliseconds()
				targetState.latencyMS = latency.Milliseconds()
			}
			if node.node.CooldownUntil != nil {
				node.node.Status = storage.ProxyStatusCooling
				s.invalidateStickyLocked(id, "")
			}
		case scope == FailureScopeTarget:
			targetState.failureCount++
			targetState.consecutiveFailure, targetState.cooldownUntil = s.nextTargetFailureState(targetState, outcome.RetryAfter, now)
			if latency > 0 {
				targetState.latencyMS = latency.Milliseconds()
			}
			if targetState.cooldownUntil.After(now) {
				s.invalidateStickyLocked(id, target)
			}
		}
		record.TargetConsecutiveFailure = targetState.consecutiveFailure
		record.TargetCooldownUntil = timePointer(targetState.cooldownUntil)
	}
	s.mu.Unlock()
	if outcome.Success || scope == FailureScopeNode || scope == FailureScopeTarget {
		s.enqueueOutcome(record)
	}
}

func (s *Service) enqueueOutcome(record storage.ProxyOutcomeRecord) {
	s.mu.Lock()
	started := s.started
	outcomes := s.outcomes
	s.mu.Unlock()
	if !started || outcomes == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.persistOutcome(ctx, record); err != nil {
			log.Printf("proxy pool outcome persistence failed: %v", err)
		}
		return
	}
	select {
	case outcomes <- record:
	default:
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.persistOutcome(ctx, record); err != nil {
			log.Printf("proxy pool outcome overflow persistence failed: %v", err)
		}
	}
}

func (s *Service) outcomeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case record := <-s.outcomes:
					flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					_ = s.persistOutcome(flushCtx, record)
					cancel()
				default:
					return
				}
			}
		case record := <-s.outcomes:
			if err := s.persistOutcome(ctx, record); err != nil && ctx.Err() == nil {
				log.Printf("proxy pool outcome persistence failed: %v", err)
			}
		}
	}
}

func (s *Service) persistOutcome(ctx context.Context, record storage.ProxyOutcomeRecord) error {
	delays := [...]time.Duration{0, 100 * time.Millisecond, 500 * time.Millisecond}
	var lastErr error
	for _, delay := range delays {
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		if err := s.repo.RecordProxyOutcome(ctx, record); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}

func (s *Service) Summary(ctx context.Context) (storage.ProxyPoolSummary, error) {
	summary, err := s.repo.ProxyPoolSummary(ctx, s.now())
	if err != nil {
		return summary, err
	}
	s.mu.Lock()
	summary.InUse = s.inUse
	summary.RoutingEnabled = s.cfg.Enabled
	summary.ProbeJobs = s.probeJobs
	summary.Probing = s.probeJobs > 0
	s.mu.Unlock()
	return summary, nil
}

func (s *Service) Import(ctx context.Context, request ImportRequest, reader io.Reader) (storage.ProxyImportResult, []string, error) {
	if s == nil || s.repo == nil || s.cipher == nil {
		return storage.ProxyImportResult{}, nil, ErrDisabled
	}
	if request.ExpiresAt.IsZero() {
		request.ExpiresAt = time.Now().Add(6 * 24 * time.Hour)
	}
	if !request.ExpiresAt.After(time.Now()) {
		return storage.ProxyImportResult{}, nil, storage.ErrInvalid
	}
	if strings.TrimSpace(request.Name) == "" {
		request.Name = "代理批次 " + time.Now().Format("2006-01-02 15:04")
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 32*1024), 4*1024*1024)
	nodes := make([]storage.ProxyNodeInput, 0)
	seen := make(map[string]struct{})
	errorsList := make([]string, 0, 100)
	totalLines, invalid, duplicates := 0, 0, 0
	for scanner.Scan() {
		totalLines++
		if totalLines > 200000 {
			return storage.ProxyImportResult{}, errorsList, fmt.Errorf("proxy import exceeds 200000 lines")
		}
		parsed, err := ParseLine(scanner.Text())
		if err != nil {
			invalid++
			if len(errorsList) < 100 {
				errorsList = append(errorsList, fmt.Sprintf("第 %d 行: %v", totalLines, err))
			}
			continue
		}
		if parsed.Canonical == "" {
			continue
		}
		fingerprint := s.cipher.Fingerprint([]byte(parsed.Canonical))
		key := string(fingerprint)
		if _, ok := seen[key]; ok {
			duplicates++
			continue
		}
		seen[key] = struct{}{}
		ciphertext, nonce, _, err := s.cipher.Encrypt(parsed.Canonical)
		if err != nil {
			return storage.ProxyImportResult{}, errorsList, err
		}
		nodes = append(nodes, storage.ProxyNodeInput{Scheme: parsed.Scheme, Host: parsed.Host, Port: parsed.Port, DisplayURL: parsed.DisplayURL, HasAuth: parsed.HasAuth, Ciphertext: ciphertext, Nonce: nonce, KeyVersion: 1, Fingerprint: fingerprint, ExpiresAt: request.ExpiresAt})
	}
	if err := scanner.Err(); err != nil {
		return storage.ProxyImportResult{}, errorsList, err
	}
	if len(nodes) == 0 {
		return storage.ProxyImportResult{}, errorsList, fmt.Errorf("no valid proxy nodes found")
	}
	result, err := s.repo.ImportProxyNodes(ctx, storage.ProxyImportInput{Name: request.Name, SourceFilename: request.SourceFilename, ExpiresAt: request.ExpiresAt, TotalLines: totalLines, InvalidCount: invalid}, nodes)
	if err != nil {
		return result, errorsList, err
	}
	result.Duplicates += duplicates
	result.Batch.DuplicateCount += duplicates
	result.Invalid = invalid
	result.Errors = errorsList
	_ = s.Refresh(ctx)
	return result, errorsList, nil
}

func (s *Service) Probe(ctx context.Context, ids []int64, limit int) (int, error) {
	s.mu.Lock()
	s.probeJobs++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.probeJobs > 0 {
			s.probeJobs--
		}
		s.mu.Unlock()
	}()
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	nodes := make([]storage.ProxyNode, 0)
	if len(ids) > 0 {
		if len(ids) > limit {
			ids = ids[:limit]
		}
		for _, id := range ids {
			node, err := s.repo.GetProxyNode(ctx, id)
			if err != nil {
				return 0, err
			}
			nodes = append(nodes, node)
		}
	} else {
		now := s.now()
		candidates, err := s.repo.ListProxyProbeCandidates(ctx, now, now.Add(-time.Minute), limit)
		if err != nil {
			return 0, err
		}
		nodes = candidates
	}
	workers := s.cfg.HealthWorkers
	if workers > len(nodes) {
		workers = len(nodes)
	}
	if workers < 1 {
		return 0, nil
	}
	queue := make(chan storage.ProxyNode)
	var wg sync.WaitGroup
	var count int
	var mu sync.Mutex
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for node := range queue {
				if s.probeNode(ctx, node) == nil {
					mu.Lock()
					count++
					mu.Unlock()
				}
			}
		}()
	}
	for _, node := range nodes {
		select {
		case queue <- node:
		case <-ctx.Done():
			close(queue)
			wg.Wait()
			return count, ctx.Err()
		}
	}
	close(queue)
	wg.Wait()
	_ = s.Refresh(ctx)
	return count, nil
}

func (s *Service) probeScheduler(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.ProbeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := s.now()
			candidates, err := s.repo.ListProxyProbeCandidates(ctx, now, now.Add(-time.Minute), s.cfg.HealthWorkers*8)
			if err != nil {
				continue
			}
			s.mu.Lock()
			queue := s.probeQueue
			for _, node := range candidates {
				if _, ok := s.queued[node.ID]; ok {
					continue
				}
				s.queued[node.ID] = struct{}{}
				select {
				case queue <- node:
				default:
					delete(s.queued, node.ID)
				}
			}
			s.mu.Unlock()
		}
	}
}

func (s *Service) probeWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case node := <-s.probeQueue:
			_ = s.probeNode(ctx, node)
			s.mu.Lock()
			delete(s.queued, node.ID)
			s.mu.Unlock()
		}
	}
}

func (s *Service) probeNode(parent context.Context, node storage.ProxyNode) error {
	raw, err := s.cipher.Decrypt(node.Ciphertext, node.Nonce, node.Fingerprint)
	if err != nil {
		_ = s.recordProbeResult(parent, node, "", false, 0)
		return err
	}
	ctx, cancel := context.WithTimeout(parent, s.cfg.ProbeTimeout)
	defer cancel()
	client, err := util.NewHTTPClient(raw)
	if err != nil {
		_ = s.recordProbeResult(parent, node, raw, false, 0)
		return err
	}
	defer client.CloseIdleConnections()
	started := s.now()
	var lastErr error
	success := false
	for _, probeURL := range s.cfg.ProbeURLs {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
		if reqErr != nil {
			lastErr = reqErr
			continue
		}
		resp, doErr := client.Do(req)
		if doErr != nil {
			lastErr = doErr
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32*1024))
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			success = true
			break
		}
		lastErr = fmt.Errorf("probe returned HTTP %d", resp.StatusCode)
	}
	if err := s.recordProbeResult(parent, node, raw, success, s.now().Sub(started)); err != nil {
		return err
	}
	if !success {
		return lastErr
	}
	return nil
}

func (s *Service) recordProbeResult(ctx context.Context, node storage.ProxyNode, raw string, success bool, latency time.Duration) error {
	now := s.now()
	cooldown := time.Duration(0)
	if !success {
		_, until := s.nextFailureState(node.ConsecutiveFailure, node.CooldownUntil, 0, now)
		if until != nil {
			cooldown = until.Sub(now)
		}
	}
	if err := s.repo.RecordProxyProbe(ctx, node.ID, success, latency, s.cfg.FailureThreshold, cooldown); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.nodes[node.ID]
	if success {
		if raw == "" {
			return nil
		}
		if current != nil {
			nodeTargets := current.targets
			nodeInflight := current.inflight
			if nodeTargets == nil {
				nodeTargets = make(map[string]*runtimeTarget)
			}
			current.node = node
			current.node.Status = storage.ProxyStatusHealthy
			current.node.SuccessCount++
			current.node.ConsecutiveFailure = 0
			current.node.CooldownUntil = nil
			if latency > 0 {
				current.node.LatencyMS = latency.Milliseconds()
			}
			current.inflight = nodeInflight
			current.targets = nodeTargets
			return nil
		}
		node.Status = storage.ProxyStatusHealthy
		node.SuccessCount++
		node.ConsecutiveFailure = 0
		node.CooldownUntil = nil
		if latency > 0 {
			node.LatencyMS = latency.Milliseconds()
		}
		s.nodes[node.ID] = &runtimeProxy{node: node, url: raw, targets: make(map[string]*runtimeTarget)}
		return nil
	}
	if current != nil {
		if current.inflight > 0 {
			current.node.Status = storage.ProxyStatusCooling
		} else {
			delete(s.nodes, node.ID)
		}
		s.invalidateStickyLocked(node.ID, "")
	}
	return nil
}

func (s *Service) Policies(ctx context.Context) ([]storage.ProxyPolicy, error) {
	return s.repo.ListProxyPolicies(ctx)
}

func (s *Service) Nodes(ctx context.Context, filter storage.ProxyNodeFilter) (storage.ProxyNodePage, error) {
	return s.repo.ListProxyNodes(ctx, filter)
}

func (s *Service) Batches(ctx context.Context, page, pageSize int) ([]storage.ProxyImportBatch, int64, error) {
	return s.repo.ListProxyBatches(ctx, page, pageSize)
}

func (s *Service) SetNodeEnabled(ctx context.Context, id int64, enabled bool) error {
	err := s.repo.SetProxyNodeEnabled(ctx, id, enabled)
	if err == nil {
		_ = s.Refresh(ctx)
	}
	return err
}

func (s *Service) DeleteNode(ctx context.Context, id int64) error {
	err := s.repo.DeleteProxyNode(ctx, id)
	if err == nil {
		_ = s.Refresh(ctx)
	}
	return err
}

func (s *Service) SetBatchEnabled(ctx context.Context, id int64, enabled bool) error {
	err := s.repo.SetProxyBatchEnabled(ctx, id, enabled)
	if err == nil {
		_ = s.Refresh(ctx)
	}
	return err
}

func (s *Service) ReplacePolicies(ctx context.Context, policies []storage.ProxyPolicy) error {
	for i := range policies {
		if !validMode(policies[i].Mode) {
			return storage.ErrInvalid
		}
	}
	err := s.repo.ReplaceProxyPolicies(ctx, policies)
	if err == nil {
		_ = s.Refresh(ctx)
	}
	return err
}
func validMode(mode string) bool {
	switch mode {
	case ModeBaselineOnly, ModeBaselineFirst, ModeProxyFirst, ModeProxyOnly, ModeStickyProxy:
		return true
	}
	return false
}
