package collection

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"pansou/model"
)

type Config struct {
	ScheduleInterval time.Duration
	DefaultCooldown  time.Duration
	LinkCheckStale   time.Duration
	MaxSourceRetries int
	DisableScheduler bool
	Now              func() time.Time
	RetryDelay       func(retry int) time.Duration
	OnError          func(error)
}

func DefaultConfig() Config {
	return Config{
		ScheduleInterval: DefaultScheduleInterval,
		DefaultCooldown:  DefaultCooldown,
		LinkCheckStale:   DefaultLinkCheckStale,
		MaxSourceRetries: 2,
		Now:              time.Now,
		RetryDelay: func(retry int) time.Duration {
			return time.Duration(1<<uint(retry-1)) * 200 * time.Millisecond
		},
	}
}

type Runner struct {
	repository RunRepository
	searcher   LiveSearcher
	sources    SourceProvider
	checks     CheckEnqueuer
	config     Config

	stateMu sync.Mutex
	started bool
	runCtx  context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	recoveryMu sync.Mutex
	recovered  bool
	persistMu  sync.Mutex

	activeMu sync.Mutex
	active   bool
	activeID int64
	idle     chan struct{}
}

func NewRunner(repository RunRepository, searcher LiveSearcher, sources SourceProvider, checks CheckEnqueuer, config Config) *Runner {
	defaults := DefaultConfig()
	if config.ScheduleInterval <= 0 {
		config.ScheduleInterval = defaults.ScheduleInterval
	}
	if config.DefaultCooldown <= 0 {
		config.DefaultCooldown = defaults.DefaultCooldown
	}
	if config.LinkCheckStale <= 0 {
		config.LinkCheckStale = defaults.LinkCheckStale
	}
	if config.MaxSourceRetries <= 0 {
		config.MaxSourceRetries = defaults.MaxSourceRetries
	}
	if config.Now == nil {
		config.Now = defaults.Now
	}
	if config.RetryDelay == nil {
		config.RetryDelay = defaults.RetryDelay
	}

	idle := make(chan struct{})
	close(idle)
	return &Runner{
		repository: repository,
		searcher:   searcher,
		sources:    sources,
		checks:     checks,
		config:     config,
		idle:       idle,
	}
}

func (r *Runner) Start(parent context.Context) error {
	if r.repository == nil {
		return errors.New("collection repository is nil")
	}
	if parent == nil {
		parent = context.Background()
	}

	r.stateMu.Lock()
	if r.started {
		r.stateMu.Unlock()
		return nil
	}
	r.runCtx, r.cancel = context.WithCancel(parent)
	r.started = true
	r.recovered = false
	r.wg.Add(1)
	r.stateMu.Unlock()

	if lifecycle, ok := r.checks.(interface{ Start(context.Context) error }); ok {
		if err := lifecycle.Start(r.runCtx); err != nil {
			r.stateMu.Lock()
			r.started = false
			r.cancel()
			r.stateMu.Unlock()
			r.wg.Done()
			return fmt.Errorf("start link check queue: %w", err)
		}
	}

	if err := r.ensureRecovered(r.runCtx); err != nil {
		r.stateMu.Lock()
		r.started = false
		r.cancel()
		r.stateMu.Unlock()
		if lifecycle, ok := r.checks.(interface{ Stop(context.Context) error }); ok {
			_ = lifecycle.Stop(context.Background())
		}
		r.wg.Done()
		return fmt.Errorf("recover collection runs: %w", err)
	}
	_, err := r.startPending(r.runCtx)
	if err != nil {
		r.stateMu.Lock()
		r.started = false
		r.cancel()
		r.stateMu.Unlock()
		if lifecycle, ok := r.checks.(interface{ Stop(context.Context) error }); ok {
			_ = lifecycle.Stop(context.Background())
		}
		r.wg.Done()
		return fmt.Errorf("resume pending collection runs: %w", err)
	}
	go r.scheduleLoop(r.runCtx)
	return nil
}

func (r *Runner) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	r.stateMu.Lock()
	if !r.started {
		r.stateMu.Unlock()
		return nil
	}
	r.started = false
	cancel := r.cancel
	r.stateMu.Unlock()
	cancel()
	var checkStopErr error
	if lifecycle, ok := r.checks.(interface{ Stop(context.Context) error }); ok {
		if err := lifecycle.Stop(ctx); err != nil {
			checkStopErr = fmt.Errorf("stop link check queue: %w", err)
		}
	}

	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return errors.Join(checkStopErr, ctx.Err())
	}

	return checkStopErr
}

func (r *Runner) StartScheduled(ctx context.Context) (*Batch, error) {
	return r.startBatch(ctx, TriggerScheduled, nil, false)
}

func (r *Runner) StartManual(ctx context.Context, keywordIDs []int64, force bool) (*Batch, error) {
	if len(keywordIDs) == 0 {
		return nil, ErrNoEligibleKeyword
	}
	return r.startBatch(ctx, TriggerManual, keywordIDs, force)
}

func (r *Runner) IsRunning() (bool, int64) {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	return r.active, r.activeID
}

func (r *Runner) WaitIdle(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	r.activeMu.Lock()
	idle := r.idle
	r.activeMu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runner) RecordExternal(ctx context.Context, keyword string, response model.SearchResponse) (*Batch, error) {
	return r.RecordExternalSource(ctx, keyword, Source{Key: "external", Type: "external"}, response)
}

func (r *Runner) RecordExternalSource(ctx context.Context, value string, source Source, response model.SearchResponse) (*Batch, error) {
	r.stateMu.Lock()
	if !r.started || r.runCtx == nil {
		r.stateMu.Unlock()
		return nil, ErrNotStarted
	}
	r.wg.Add(1)
	r.stateMu.Unlock()
	defer r.wg.Done()
	normalized := NormalizeKeyword(value)
	if normalized == "" {
		return nil, ErrEmptyKeyword
	}

	// External live search is already complete. Serialize only its persistence
	// window with recovery/claiming so it cannot be mistaken for resumable work.
	r.persistMu.Lock()
	defer r.persistMu.Unlock()

	keyword, err := r.repository.FindKeyword(ctx, normalized)
	if err != nil {
		return nil, fmt.Errorf("find external keyword: %w", err)
	}
	if keyword == nil {
		keyword = &Keyword{
			Value:       strings.TrimSpace(value),
			Normalized:  normalized,
			KeywordType: "general",
			SourceType:  "api",
		}
	}

	startedAt := r.now()
	snapshot := *keyword
	if snapshot.KeywordType == "" {
		snapshot.KeywordType = "general"
	}
	batch, err := r.repository.CreateBatch(ctx, NewBatch{
		Trigger:   TriggerExternal,
		Keywords:  []Keyword{snapshot},
		CreatedAt: startedAt,
	})
	if err != nil {
		return nil, fmt.Errorf("create external collection run: %w", err)
	}
	if len(batch.Items) != 1 {
		return &batch, fmt.Errorf("external collection batch returned %d items, want 1", len(batch.Items))
	}
	item := batch.Items[0]
	// The persistence mutex stays held until this external batch is terminal,
	// so ClaimPending can never pick up its short-lived pending item.
	if err := r.repository.MarkItemRunning(ctx, batch.ID, item.ID, startedAt); err != nil {
		r.requireRecovery()
		return &batch, fmt.Errorf("mark external collection item running: %w", err)
	}

	count := ResultCount(response)
	summaryKey := sourceSummaryKey(source, 0, nil)
	summary := SourceSummary{
		summaryKey: {
			Key:         source.Key,
			Type:        source.Type,
			Attempts:    1,
			ResultCount: count,
		},
	}
	completion := ItemCompletion{
		StartedAt:     startedAt,
		SourceSummary: summary,
	}
	var ingestErr error
	if count > 0 {
		ingested, err := r.repository.Ingest(ctx, IngestRequest{
			BatchID:      batch.ID,
			ItemID:       item.ID,
			Trigger:      TriggerExternal,
			Keyword:      item.Keyword,
			Source:       source,
			Response:     response,
			DiscoveredAt: startedAt,
		})
		entry := summary[summaryKey]
		if err != nil {
			ingestErr = fmt.Errorf("ingest external search: %w", err)
			entry.Status = string(StatusFailed)
			entry.Error = err.Error()
		} else {
			entry.Status = string(StatusSuccess)
			entry.NewCount = ingested.New
			entry.DuplicateCount = ingested.Duplicate
			completion.NewCount = ingested.New
			completion.DuplicateCount = ingested.Duplicate
			r.enqueueChecks(ctx, ingested.CheckCandidates)
		}
		summary[summaryKey] = entry
	} else {
		entry := summary[summaryKey]
		entry.Status = string(StatusSuccessEmpty)
		summary[summaryKey] = entry
	}

	completion.FinishedAt = r.now()
	if ingestErr != nil {
		completion.Status = StatusFailed
		completion.Error = ingestErr.Error()
	} else {
		completion.Status = DetermineRunStatus(count > 0, count == 0)
	}
	if keyword.ID != 0 && (completion.Status == StatusSuccess || completion.Status == StatusSuccessEmpty) {
		next := CalculateNextEligibleAt(*keyword, completion.FinishedAt, r.config.DefaultCooldown)
		completion.NextEligibleAt = &next
	}
	if err := r.repository.CompleteItem(ctx, batch.ID, item.ID, completion); err != nil {
		r.requireRecovery()
		return &batch, errors.Join(ingestErr, fmt.Errorf("complete external collection item: %w", err))
	}
	if err := r.repository.CompleteBatch(ctx, batch.ID, BatchCompletion{Status: completion.Status, FinishedAt: completion.FinishedAt}); err != nil {
		return &batch, errors.Join(ingestErr, fmt.Errorf("complete external collection batch: %w", err))
	}
	batch.Status = completion.Status
	batch.FinishedAt = &completion.FinishedAt
	return &batch, ingestErr
}

func (r *Runner) scheduleLoop(ctx context.Context) {
	defer r.wg.Done()
	ticker := time.NewTicker(r.config.ScheduleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

func (r *Runner) tick(ctx context.Context) {
	if running, _ := r.IsRunning(); running {
		return
	}
	if err := r.ensureRecovered(ctx); err != nil {
		r.report(fmt.Errorf("recover collection runs: %w", err))
		return
	}
	if r.config.DisableScheduler {
		return
	}
	resumed, err := r.startPending(ctx)
	if err != nil {
		if !errors.Is(err, ErrBatchRunning) && !errors.Is(err, context.Canceled) {
			r.report(fmt.Errorf("resume pending collection: %w", err))
		}
		return
	}
	if resumed {
		return
	}
	if _, err := r.StartScheduled(ctx); err != nil && !errors.Is(err, ErrBatchRunning) && !errors.Is(err, ErrNoEligibleKeyword) && !errors.Is(err, context.Canceled) {
		r.report(fmt.Errorf("start scheduled collection: %w", err))
	}
}

func (r *Runner) startPending(ctx context.Context) (bool, error) {
	runCtx, err := r.runtimeContext()
	if err != nil {
		return false, err
	}
	if !r.acquireActive() {
		return false, ErrBatchRunning
	}
	releaseActive := true
	defer func() {
		if releaseActive {
			r.releaseActive()
		}
	}()

	r.stateMu.Lock()
	if !r.started {
		r.stateMu.Unlock()
		return false, ErrNotStarted
	}
	r.wg.Add(1)
	r.stateMu.Unlock()
	releaseWaitGroup := true
	defer func() {
		if releaseWaitGroup {
			r.wg.Done()
		}
	}()

	r.persistMu.Lock()
	claimed, err := r.repository.ClaimPending(ctx)
	r.persistMu.Unlock()
	if err != nil {
		r.requireRecovery()
		return false, err
	}
	if claimed == nil {
		return false, nil
	}
	if claimed.Batch.ID == 0 || claimed.Item.ID == 0 {
		r.requireRecovery()
		return false, errors.New("claimed pending collection item is missing identifiers")
	}
	if claimed.Item.BatchID == 0 {
		claimed.Item.BatchID = claimed.Batch.ID
	}
	if claimed.StartedAt.IsZero() {
		claimed.StartedAt = r.now()
	}

	r.activeMu.Lock()
	r.activeID = claimed.Batch.ID
	r.activeMu.Unlock()
	releaseActive = false
	releaseWaitGroup = false
	go r.runClaimedPending(runCtx, *claimed)
	return true, nil
}

func (r *Runner) startBatch(ctx context.Context, trigger Trigger, keywordIDs []int64, force bool) (*Batch, error) {
	runCtx, err := r.runtimeContext()
	if err != nil {
		return nil, err
	}
	if !r.acquireActive() {
		return nil, ErrBatchRunning
	}
	releaseActive := true
	defer func() {
		if releaseActive {
			r.releaseActive()
		}
	}()
	r.stateMu.Lock()
	if !r.started {
		r.stateMu.Unlock()
		return nil, ErrNotStarted
	}
	r.wg.Add(1)
	r.stateMu.Unlock()
	releaseWaitGroup := true
	defer func() {
		if releaseWaitGroup {
			r.wg.Done()
		}
	}()
	if err := r.ensureRecovered(ctx); err != nil {
		return nil, fmt.Errorf("recover collection runs: %w", err)
	}

	selection := KeywordSelection{IDs: append([]int64(nil), keywordIDs...), EnabledOnly: true}
	keywords, err := r.repository.SelectKeywords(ctx, selection)
	if err != nil {
		return nil, fmt.Errorf("select collection keywords: %w", err)
	}
	enabled := keywords[:0]
	for _, keyword := range keywords {
		if keyword.Enabled {
			enabled = append(enabled, keyword)
		}
	}
	keywords = enabled
	if trigger != TriggerScheduled {
		keywords = filterRequestedKeywords(keywords, keywordIDs)
	}
	keywords = prepareKeywords(keywords, r.now(), force)
	if len(keywords) == 0 {
		return nil, ErrNoEligibleKeyword
	}

	batch, err := r.repository.CreateBatch(ctx, NewBatch{
		Trigger:   trigger,
		Forced:    force,
		Keywords:  keywords,
		CreatedAt: r.now(),
	})
	if err != nil {
		return nil, fmt.Errorf("create collection batch: %w", err)
	}
	if len(batch.Items) == 0 {
		return nil, errors.New("collection batch has no items")
	}
	if len(batch.Items) != len(keywords) {
		return nil, fmt.Errorf("collection batch returned %d items for %d keyword snapshots", len(batch.Items), len(keywords))
	}

	r.activeMu.Lock()
	r.activeID = batch.ID
	r.activeMu.Unlock()
	releaseActive = false
	releaseWaitGroup = false
	go r.runBatch(runCtx, batch)
	return &batch, nil
}

func (r *Runner) runBatch(ctx context.Context, batch Batch) {
	defer r.wg.Done()
	defer r.releaseActive()

	anyData := false
	anyCompleted := false
	for _, item := range batch.Items {
		if ctx.Err() != nil {
			return
		}
		startedAt := r.now()
		if err := r.repository.MarkItemRunning(ctx, batch.ID, item.ID, startedAt); err != nil {
			r.requireRecovery()
			r.report(fmt.Errorf("mark collection item %d running: %w", item.ID, err))
			break
		}
		completion := r.collectKeyword(ctx, batch, item, startedAt)
		if ctx.Err() != nil {
			return
		}
		if err := r.repository.CompleteItem(ctx, batch.ID, item.ID, completion); err != nil {
			r.requireRecovery()
			r.report(fmt.Errorf("complete collection item %d: %w", item.ID, err))
			break
		}
		switch completion.Status {
		case StatusSuccess:
			anyData = true
			anyCompleted = true
		case StatusSuccessEmpty:
			anyCompleted = true
		}
	}
	if ctx.Err() != nil {
		return
	}
	finishedAt := r.now()
	status := DetermineRunStatus(anyData, anyCompleted)
	if err := r.repository.CompleteBatch(ctx, batch.ID, BatchCompletion{Status: status, FinishedAt: finishedAt}); err != nil {
		r.report(fmt.Errorf("complete collection batch %d: %w", batch.ID, err))
	}
}

func (r *Runner) runClaimedPending(ctx context.Context, claimed ClaimedRunItem) {
	defer r.wg.Done()
	defer r.releaseActive()

	current := claimed
	for {
		if ctx.Err() != nil {
			return
		}
		startedAt := current.StartedAt
		if startedAt.IsZero() {
			startedAt = r.now()
		}
		completion := r.collectKeyword(ctx, current.Batch, current.Item, startedAt)
		if ctx.Err() != nil {
			return
		}
		if err := r.repository.CompleteItem(ctx, current.Batch.ID, current.Item.ID, completion); err != nil {
			r.requireRecovery()
			r.report(fmt.Errorf("complete resumed collection item %d: %w", current.Item.ID, err))
			return
		}

		r.persistMu.Lock()
		next, err := r.repository.ClaimPending(ctx)
		r.persistMu.Unlock()
		if err != nil {
			r.requireRecovery()
			r.report(fmt.Errorf("claim next pending collection item: %w", err))
			return
		}
		if next == nil {
			return
		}
		if next.Item.BatchID == 0 {
			next.Item.BatchID = next.Batch.ID
		}
		if next.StartedAt.IsZero() {
			next.StartedAt = r.now()
		}
		r.activeMu.Lock()
		r.activeID = next.Batch.ID
		r.activeMu.Unlock()
		current = *next
	}
}

func (r *Runner) collectKeyword(ctx context.Context, batch Batch, item RunItem, startedAt time.Time) ItemCompletion {
	completion := ItemCompletion{
		StartedAt:     startedAt,
		SourceSummary: make(SourceSummary),
	}
	if r.searcher == nil || r.sources == nil {
		completion.Status = StatusFailed
		completion.Error = ErrNoSources.Error()
		completion.FinishedAt = r.now()
		return completion
	}

	searcher := r.searcher
	var sourceLease SourceLease
	var sources []Source
	var err error
	if leased, ok := r.sources.(LeasedSourceProvider); ok {
		sourceLease, err = leased.AcquireSources(ctx, item.Keyword)
		if err == nil && sourceLease != nil {
			defer sourceLease.Release()
			sources = sourceLease.Sources()
			if sourceLease.Searcher() != nil {
				searcher = sourceLease.Searcher()
			}
		}
	} else {
		sources, err = r.sources.Sources(ctx, item.Keyword)
	}
	if err != nil {
		completion.Status = StatusFailed
		completion.Error = err.Error()
		completion.FinishedAt = r.now()
		return completion
	}
	if len(sources) == 0 {
		completion.Status = StatusFailed
		completion.Error = ErrNoSources.Error()
		completion.FinishedAt = r.now()
		return completion
	}

	anyData := false
	anyCompleted := false
	errorsBySource := make([]string, 0)
	for index, source := range sources {
		sourceStarted := r.now()
		response, attempts, searchErr := r.searchSource(ctx, searcher, item.Keyword, source)
		key := sourceSummaryKey(source, index, completion.SourceSummary)
		entry := SourceRunSummary{
			Key:         source.Key,
			Type:        source.Type,
			Attempts:    attempts,
			ResultCount: ResultCount(response),
		}
		if searchErr != nil && entry.ResultCount == 0 {
			entry.Status = string(StatusFailed)
			entry.Error = searchErr.Error()
			entry.DurationMS = r.now().Sub(sourceStarted).Milliseconds()
			completion.SourceSummary[key] = entry
			errorsBySource = append(errorsBySource, key+": "+searchErr.Error())
			continue
		}
		if searchErr != nil {
			entry.Error = searchErr.Error()
		}
		if entry.ResultCount == 0 {
			entry.Status = string(StatusSuccessEmpty)
			entry.DurationMS = r.now().Sub(sourceStarted).Milliseconds()
			completion.SourceSummary[key] = entry
			anyCompleted = true
			continue
		}

		ingested, ingestErr := r.repository.Ingest(ctx, IngestRequest{
			BatchID:      batch.ID,
			ItemID:       item.ID,
			Trigger:      batch.Trigger,
			Keyword:      item.Keyword,
			Source:       source,
			Response:     response,
			DiscoveredAt: r.now(),
		})
		if ingestErr != nil {
			entry.Status = string(StatusFailed)
			entry.Error = ingestErr.Error()
			errorsBySource = append(errorsBySource, key+": "+ingestErr.Error())
		} else {
			entry.Status = string(StatusSuccess)
			entry.NewCount = ingested.New
			entry.DuplicateCount = ingested.Duplicate
			completion.NewCount += ingested.New
			completion.DuplicateCount += ingested.Duplicate
			anyData = true
			anyCompleted = true
			r.enqueueChecks(ctx, ingested.CheckCandidates)
		}
		entry.DurationMS = r.now().Sub(sourceStarted).Milliseconds()
		completion.SourceSummary[key] = entry
	}

	completion.FinishedAt = r.now()
	completion.Status = DetermineRunStatus(anyData, anyCompleted)
	if completion.Status == StatusFailed {
		completion.Error = strings.Join(errorsBySource, "; ")
	}
	if completion.Status == StatusSuccess || completion.Status == StatusSuccessEmpty {
		next := CalculateNextEligibleAt(item.Keyword, completion.FinishedAt, r.config.DefaultCooldown)
		completion.NextEligibleAt = &next
	}
	return completion
}

func (r *Runner) searchSource(ctx context.Context, searcher LiveSearcher, keyword Keyword, source Source) (model.SearchResponse, int, error) {
	var response model.SearchResponse
	var err error
	attempts := r.config.MaxSourceRetries + 1
	for attempt := 1; attempt <= attempts; attempt++ {
		response, err = searcher.Search(ctx, SearchRequest{Keyword: keyword, Source: source, ForceRefresh: true})
		if err == nil {
			return response, attempt, nil
		}
		if ResultCount(response) > 0 {
			return response, attempt, err
		}
		if ctx.Err() != nil {
			return response, attempt, ctx.Err()
		}
		if attempt < attempts {
			if err := waitContext(ctx, r.config.RetryDelay(attempt)); err != nil {
				return response, attempt, err
			}
		}
	}
	return response, attempts, err
}

func (r *Runner) enqueueChecks(ctx context.Context, candidates []LinkCheckCandidate) {
	if r.checks == nil {
		return
	}
	now := r.now()
	for _, candidate := range candidates {
		if !ShouldQueueLinkCheck(candidate, now, r.config.LinkCheckStale) {
			continue
		}
		if err := r.checks.Enqueue(ctx, candidate); err != nil {
			r.report(fmt.Errorf("enqueue link check for resource %d: %w", candidate.ResourceID, err))
		}
	}
}

func (r *Runner) ensureRecovered(ctx context.Context) error {
	r.persistMu.Lock()
	defer r.persistMu.Unlock()
	r.recoveryMu.Lock()
	defer r.recoveryMu.Unlock()
	if r.recovered {
		return nil
	}
	if _, err := r.repository.RecoverRunning(ctx, r.now()); err != nil {
		return err
	}
	r.recovered = true
	return nil
}

func (r *Runner) requireRecovery() {
	r.recoveryMu.Lock()
	r.recovered = false
	r.recoveryMu.Unlock()
}

func (r *Runner) runtimeContext() (context.Context, error) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if !r.started || r.runCtx == nil {
		return nil, ErrNotStarted
	}
	return r.runCtx, nil
}

func (r *Runner) acquireActive() bool {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	if r.active {
		return false
	}
	r.active = true
	r.activeID = 0
	r.idle = make(chan struct{})
	return true
}

func (r *Runner) releaseActive() {
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	if !r.active {
		return
	}
	r.active = false
	r.activeID = 0
	close(r.idle)
}

func (r *Runner) now() time.Time {
	return r.config.Now().UTC()
}

func (r *Runner) report(err error) {
	if err != nil && r.config.OnError != nil {
		r.config.OnError(err)
	}
}

func filterRequestedKeywords(keywords []Keyword, ids []int64) []Keyword {
	wanted := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	result := keywords[:0]
	for _, keyword := range keywords {
		if _, ok := wanted[keyword.ID]; ok {
			result = append(result, keyword)
		}
	}
	return result
}

func sourceSummaryKey(source Source, index int, existing SourceSummary) string {
	key := strings.TrimSpace(source.Key)
	if key == "" {
		key = strings.TrimSpace(source.Type)
	}
	if key == "" {
		key = fmt.Sprintf("source_%d", index+1)
	}
	if existing == nil {
		return key
	}
	if _, found := existing[key]; !found {
		return key
	}
	base := key
	for suffix := 2; ; suffix++ {
		key = fmt.Sprintf("%s_%d", base, suffix)
		if _, found := existing[key]; !found {
			return key
		}
	}
}

func waitContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
