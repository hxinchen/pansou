package collection

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"pansou/model"
)

type fakeRunRepository struct {
	mu               sync.Mutex
	keywords         []Keyword
	found            *Keyword
	nextBatchID      int64
	nextItemID       int64
	created          []NewBatch
	marked           []RunItem
	itemCompletions  []ItemCompletion
	batchCompletions []BatchCompletion
	ingestResult     IngestResult
	ingestErr        error
	ingestRequests   []IngestRequest
	recoverCalls     int
	recoverErr       error
	pendingClaims    []*ClaimedRunItem
	claimCalls       int
	claimErr         error
	completionSignal chan struct{}
}

func (f *fakeRunRepository) SelectKeywords(_ context.Context, selection KeywordSelection) ([]Keyword, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]Keyword, 0, len(f.keywords))
	wanted := make(map[int64]struct{}, len(selection.IDs))
	for _, id := range selection.IDs {
		wanted[id] = struct{}{}
	}
	for _, keyword := range f.keywords {
		if len(wanted) > 0 {
			if _, ok := wanted[keyword.ID]; !ok {
				continue
			}
		}
		if selection.EnabledOnly && !keyword.Enabled {
			continue
		}
		result = append(result, keyword)
	}
	return result, nil
}

func (f *fakeRunRepository) FindKeyword(context.Context, string) (*Keyword, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.found == nil {
		return nil, nil
	}
	copy := *f.found
	return &copy, nil
}

func (f *fakeRunRepository) CreateBatch(_ context.Context, request NewBatch) (Batch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextBatchID++
	f.created = append(f.created, request)
	batch := Batch{
		ID:        f.nextBatchID,
		Trigger:   request.Trigger,
		Forced:    request.Forced,
		Status:    StatusPending,
		CreatedAt: request.CreatedAt,
	}
	for _, keyword := range request.Keywords {
		f.nextItemID++
		batch.Items = append(batch.Items, RunItem{
			ID:      f.nextItemID,
			BatchID: batch.ID,
			Keyword: keyword,
			Status:  StatusPending,
		})
	}
	return batch, nil
}

func (f *fakeRunRepository) MarkItemRunning(_ context.Context, batchID, itemID int64, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marked = append(f.marked, RunItem{ID: itemID, BatchID: batchID, Status: StatusRunning})
	return nil
}

func (f *fakeRunRepository) Ingest(_ context.Context, request IngestRequest) (IngestResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ingestRequests = append(f.ingestRequests, request)
	return f.ingestResult, f.ingestErr
}

func (f *fakeRunRepository) CompleteItem(_ context.Context, _, _ int64, completion ItemCompletion) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.itemCompletions = append(f.itemCompletions, completion)
	if f.completionSignal != nil {
		select {
		case f.completionSignal <- struct{}{}:
		default:
		}
	}
	return nil
}

func (f *fakeRunRepository) CompleteBatch(_ context.Context, _ int64, completion BatchCompletion) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batchCompletions = append(f.batchCompletions, completion)
	return nil
}

func (f *fakeRunRepository) RecoverRunning(context.Context, time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recoverCalls++
	return 1, f.recoverErr
}

func (f *fakeRunRepository) ClaimPending(context.Context) (*ClaimedRunItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimCalls++
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	if len(f.pendingClaims) == 0 {
		return nil, nil
	}
	claimed := f.pendingClaims[0]
	f.pendingClaims = f.pendingClaims[1:]
	if claimed == nil {
		return nil, nil
	}
	copy := *claimed
	return &copy, nil
}

type fakeEnqueuer struct {
	mu         sync.Mutex
	candidates []LinkCheckCandidate
	calls      int
	fullAt     int
}

func (f *fakeEnqueuer) Enqueue(_ context.Context, candidate LinkCheckCandidate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fullAt > 0 && f.calls >= f.fullAt {
		return ErrQueueFull
	}
	f.candidates = append(f.candidates, candidate)
	return nil
}

func runnerTestConfig(now time.Time) Config {
	config := DefaultConfig()
	config.DisableScheduler = true
	config.Now = func() time.Time { return now }
	config.RetryDelay = func(int) time.Duration { return 0 }
	return config
}

func TestEnqueueChecksStopsWhenQueueIsFull(t *testing.T) {
	enqueuer := &fakeEnqueuer{fullAt: 2}
	runner := &Runner{checks: enqueuer}
	runner.enqueueChecks(context.Background(), []LinkCheckCandidate{
		{ResourceID: 1, URL: "https://example.test/1", Status: DetectionPending},
		{ResourceID: 2, URL: "https://example.test/2", Status: DetectionPending},
		{ResourceID: 3, URL: "https://example.test/3", Status: DetectionPending},
	})
	enqueuer.mu.Lock()
	defer enqueuer.mu.Unlock()
	if enqueuer.calls != 2 || len(enqueuer.candidates) != 1 || enqueuer.candidates[0].ResourceID != 1 {
		t.Fatalf("enqueue calls=%d candidates=%+v", enqueuer.calls, enqueuer.candidates)
	}
}

func TestRunnerManualCooldownRetryAndPartialSourceFailure(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	repository := &fakeRunRepository{
		keywords: []Keyword{
			{ID: 1, Value: "Alpha", Enabled: true, Priority: 1},
			{ID: 2, Value: "Beta", Enabled: true, Priority: 2, NextEligibleAt: &future},
		},
		ingestResult: IngestResult{
			New:       1,
			Duplicate: 2,
			CheckCandidates: []LinkCheckCandidate{{
				ResourceID: 7,
				URL:        "https://example.test/share/7",
				Status:     DetectionPending,
				IsNew:      true,
			}},
		},
	}
	attempts := make(map[string]int)
	var attemptsMu sync.Mutex
	searcher := LiveSearchFunc(func(_ context.Context, request SearchRequest) (model.SearchResponse, error) {
		attemptsMu.Lock()
		attempts[request.Source.Key]++
		attempt := attempts[request.Source.Key]
		attemptsMu.Unlock()
		if request.Source.Key == "tg" && attempt < 3 {
			return model.SearchResponse{}, errors.New("temporary tg error")
		}
		if request.Source.Key == "plugin" {
			return model.SearchResponse{}, errors.New("plugin unavailable")
		}
		return model.SearchResponse{Total: 1, Results: []model.SearchResult{{Title: "result"}}}, nil
	})
	checks := &fakeEnqueuer{}
	runner := NewRunner(repository, searcher, StaticSources{
		{Key: "tg", Type: "tg", Channels: []string{"channel-a"}},
		{Key: "plugin", Type: "plugin", Plugins: []string{"plugin-a"}},
	}, checks, runnerTestConfig(now))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := runner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	batch, err := runner.StartManual(ctx, []int64{1, 2}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Items) != 1 || batch.Items[0].Keyword.ID != 1 {
		t.Fatalf("cooldown snapshot = %+v", batch.Items)
	}
	if err := runner.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runner.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	repository.mu.Lock()
	if repository.recoverCalls != 1 {
		t.Fatalf("recover calls = %d, want 1", repository.recoverCalls)
	}
	if len(repository.itemCompletions) != 1 {
		t.Fatalf("item completions = %d", len(repository.itemCompletions))
	}
	completion := repository.itemCompletions[0]
	repository.mu.Unlock()
	if completion.Status != StatusSuccess {
		t.Fatalf("item status = %q", completion.Status)
	}
	if completion.NewCount != 1 || completion.DuplicateCount != 2 {
		t.Fatalf("counts = %d/%d", completion.NewCount, completion.DuplicateCount)
	}
	if completion.NextEligibleAt == nil || !completion.NextEligibleAt.Equal(now.Add(DefaultCooldown)) {
		t.Fatalf("next eligible = %v", completion.NextEligibleAt)
	}
	if completion.SourceSummary["tg"].Attempts != 3 || completion.SourceSummary["plugin"].Attempts != 3 {
		t.Fatalf("source summary = %+v", completion.SourceSummary)
	}
	if completion.SourceSummary["plugin"].Status != string(StatusFailed) {
		t.Fatalf("partial source status = %+v", completion.SourceSummary["plugin"])
	}
	if _, err := json.Marshal(completion.SourceSummary); err != nil {
		t.Fatalf("source summary is not JSON-compatible: %v", err)
	}
	checks.mu.Lock()
	defer checks.mu.Unlock()
	if len(checks.candidates) != 1 || checks.candidates[0].ResourceID != 7 {
		t.Fatalf("check candidates = %+v", checks.candidates)
	}
}

func TestRunnerStartFailsWhenRecoveryFails(t *testing.T) {
	repository := &fakeRunRepository{recoverErr: errors.New("database unavailable")}
	runner := NewRunner(repository, nil, nil, nil, runnerTestConfig(time.Now()))
	if err := runner.Start(context.Background()); err == nil {
		t.Fatal("Start() succeeded despite recovery failure")
	}
	if _, err := runner.StartManual(context.Background(), []int64{1}, false); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("StartManual() error = %v", err)
	}
}

func TestRunnerStartResumesRecoveredPendingItemsBeforeNewSchedule(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	first := ClaimedRunItem{
		Batch: Batch{ID: 40, Trigger: TriggerManual, Status: StatusRunning},
		Item: RunItem{ID: 401, BatchID: 40, Status: StatusRunning, Keyword: Keyword{
			ID: 1, Value: "Recovered A", Normalized: "recovered a", KeywordType: "general", Enabled: true,
		}},
		StartedAt: now.Add(-time.Minute),
	}
	second := ClaimedRunItem{
		Batch: Batch{ID: 40, Trigger: TriggerManual, Status: StatusRunning},
		Item: RunItem{ID: 402, BatchID: 40, Status: StatusRunning, Keyword: Keyword{
			ID: 2, Value: "Recovered B", Normalized: "recovered b", KeywordType: "general", Enabled: true,
		}},
		StartedAt: now,
	}
	repository := &fakeRunRepository{
		pendingClaims:    []*ClaimedRunItem{&first, &second},
		ingestResult:     IngestResult{New: 1},
		completionSignal: make(chan struct{}, 2),
	}
	searched := make([]string, 0, 2)
	var searchedMu sync.Mutex
	searcher := LiveSearchFunc(func(_ context.Context, request SearchRequest) (model.SearchResponse, error) {
		searchedMu.Lock()
		searched = append(searched, request.Keyword.Value)
		searchedMu.Unlock()
		return model.SearchResponse{Total: 1, Results: []model.SearchResult{{Title: request.Keyword.Value}}}, nil
	})
	config := runnerTestConfig(now)
	config.DisableScheduler = false
	config.ScheduleInterval = time.Hour
	runner := NewRunner(repository, searcher, StaticSources{{Key: "all", Type: "all"}}, nil, config)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-repository.completionSignal:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if err := runner.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	repository.mu.Lock()
	completions := append([]ItemCompletion(nil), repository.itemCompletions...)
	claimCalls := repository.claimCalls
	created := len(repository.created)
	marked := len(repository.marked)
	repository.mu.Unlock()
	searchedMu.Lock()
	gotSearched := append([]string(nil), searched...)
	searchedMu.Unlock()
	if len(completions) != 2 || completions[0].Status != StatusSuccess || completions[1].Status != StatusSuccess {
		t.Fatalf("resumed completions = %+v", completions)
	}
	if len(gotSearched) != 2 || gotSearched[0] != "Recovered A" || gotSearched[1] != "Recovered B" {
		t.Fatalf("resumed search order = %v", gotSearched)
	}
	if claimCalls != 3 || created != 0 || marked != 0 {
		t.Fatalf("claims/created/marked = %d/%d/%d", claimCalls, created, marked)
	}
	if err := runner.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerForcedManualBypassesCooldown(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	future := now.Add(24 * time.Hour)
	repository := &fakeRunRepository{keywords: []Keyword{{ID: 2, Value: "Beta", Enabled: true, NextEligibleAt: &future}}}
	runner := NewRunner(repository, LiveSearchFunc(func(context.Context, SearchRequest) (model.SearchResponse, error) {
		return model.SearchResponse{}, nil
	}), StaticSources{{Key: "all", Type: "all"}}, nil, runnerTestConfig(now))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	batch, err := runner.StartManual(ctx, []int64{2}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !batch.Forced || len(batch.Items) != 1 {
		t.Fatalf("forced batch = %+v", batch)
	}
	if err := runner.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	repository.mu.Lock()
	completion := repository.itemCompletions[0]
	repository.mu.Unlock()
	if completion.Status != StatusSuccessEmpty || completion.NextEligibleAt == nil {
		t.Fatalf("forced completion = %+v", completion)
	}
	if err := runner.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerForcedManualStillRejectsDisabledKeyword(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	repository := &fakeRunRepository{keywords: []Keyword{{ID: 2, Value: "disabled", Enabled: false}}}
	runner := NewRunner(repository, nil, nil, nil, runnerTestConfig(now))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.StartManual(ctx, []int64{2}, true); !errors.Is(err, ErrNoEligibleKeyword) {
		t.Fatalf("forced disabled keyword error = %v", err)
	}
	if err := runner.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestStartScheduledSnapshotsEnabledEligibleKeywordsByPriority(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	repository := &fakeRunRepository{keywords: []Keyword{
		{ID: 1, Value: "low", Enabled: true, Priority: 1},
		{ID: 2, Value: "disabled", Enabled: false, Priority: 100},
		{ID: 3, Value: "cooling", Enabled: true, Priority: 50, NextEligibleAt: &future},
		{ID: 4, Value: "high", Enabled: true, Priority: 10},
	}}
	runner := NewRunner(repository, LiveSearchFunc(func(context.Context, SearchRequest) (model.SearchResponse, error) {
		return model.SearchResponse{}, nil
	}), StaticSources{{Key: "all", Type: "all"}}, nil, runnerTestConfig(now))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	batch, err := runner.StartScheduled(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Items) != 2 || batch.Items[0].Keyword.ID != 4 || batch.Items[1].Keyword.ID != 1 {
		t.Fatalf("scheduled snapshot = %+v", batch.Items)
	}
	if err := runner.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runner.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestStartScheduledReturnsNoEligibleKeyword(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	repository := &fakeRunRepository{keywords: []Keyword{{ID: 1, Value: "disabled", Enabled: false}}}
	runner := NewRunner(repository, nil, nil, nil, runnerTestConfig(now))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.StartScheduled(ctx); !errors.Is(err, ErrNoEligibleKeyword) {
		t.Fatalf("scheduled empty error = %v", err)
	}
	if running, _ := runner.IsRunning(); running {
		t.Fatal("runner remained active after empty snapshot")
	}
	if err := runner.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerFailedRunDoesNotAdvanceCooldown(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	repository := &fakeRunRepository{keywords: []Keyword{{ID: 1, Value: "Alpha", Enabled: true}}}
	attempts := 0
	runner := NewRunner(repository, LiveSearchFunc(func(context.Context, SearchRequest) (model.SearchResponse, error) {
		attempts++
		return model.SearchResponse{}, errors.New("network down")
	}), StaticSources{{Key: "tg", Type: "tg"}}, nil, runnerTestConfig(now))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.StartManual(ctx, []int64{1}, false); err != nil {
		t.Fatal(err)
	}
	if err := runner.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	repository.mu.Lock()
	completion := repository.itemCompletions[0]
	batchCompletion := repository.batchCompletions[0]
	repository.mu.Unlock()
	if completion.Status != StatusFailed || completion.NextEligibleAt != nil {
		t.Fatalf("failed completion = %+v", completion)
	}
	if batchCompletion.Status != StatusFailed || attempts != 3 {
		t.Fatalf("batch status/attempts = %q/%d", batchCompletion.Status, attempts)
	}
	if err := runner.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerKeepsPartialDataReturnedWithError(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	repository := &fakeRunRepository{
		keywords:     []Keyword{{ID: 1, Value: "Alpha", Enabled: true}},
		ingestResult: IngestResult{New: 1},
	}
	runner := NewRunner(repository, LiveSearchFunc(func(context.Context, SearchRequest) (model.SearchResponse, error) {
		return model.SearchResponse{Total: 1, Results: []model.SearchResult{{Title: "partial"}}}, errors.New("one subchannel failed")
	}), StaticSources{{Key: "tg", Type: "tg"}}, nil, runnerTestConfig(now))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.StartManual(ctx, []int64{1}, false); err != nil {
		t.Fatal(err)
	}
	if err := runner.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	repository.mu.Lock()
	completion := repository.itemCompletions[0]
	ingestCount := len(repository.ingestRequests)
	repository.mu.Unlock()
	if completion.Status != StatusSuccess || ingestCount != 1 {
		t.Fatalf("partial-data completion/ingests = %+v/%d", completion, ingestCount)
	}
	if completion.SourceSummary["tg"].Error == "" || completion.SourceSummary["tg"].Attempts != 1 {
		t.Fatalf("partial source summary = %+v", completion.SourceSummary["tg"])
	}
	if err := runner.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerAllowsOnlyOneActiveBatch(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	repository := &fakeRunRepository{keywords: []Keyword{{ID: 1, Value: "Alpha", Enabled: true}, {ID: 2, Value: "Beta", Enabled: true}}}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	runner := NewRunner(repository, LiveSearchFunc(func(ctx context.Context, _ SearchRequest) (model.SearchResponse, error) {
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return model.SearchResponse{}, nil
		case <-ctx.Done():
			return model.SearchResponse{}, ctx.Err()
		}
	}), StaticSources{{Key: "all", Type: "all"}}, nil, runnerTestConfig(now))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.StartManual(ctx, []int64{1}, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := runner.StartManual(ctx, []int64{2}, false); !errors.Is(err, ErrBatchRunning) {
		t.Fatalf("second batch error = %v", err)
	}
	external, err := runner.RecordExternal(ctx, "external", model.SearchResponse{})
	if err != nil {
		t.Fatalf("overlapping external record error = %v", err)
	}
	if external == nil || external.Trigger != TriggerExternal || external.Status != StatusSuccessEmpty {
		t.Fatalf("overlapping external batch = %+v", external)
	}
	repository.mu.Lock()
	createdWhileBusy := len(repository.created)
	repository.mu.Unlock()
	if createdWhileBusy != 2 {
		t.Fatalf("created batches while busy = %d, want 2", createdWhileBusy)
	}
	close(release)
	if err := runner.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runner.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestExternalRecordDuringActiveBatchDoesNotChangeActiveBatch(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	repository := &fakeRunRepository{keywords: []Keyword{{ID: 1, Value: "Alpha", Enabled: true}}}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	runner := NewRunner(repository, LiveSearchFunc(func(ctx context.Context, _ SearchRequest) (model.SearchResponse, error) {
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return model.SearchResponse{}, nil
		case <-ctx.Done():
			return model.SearchResponse{}, ctx.Err()
		}
	}), StaticSources{{Key: "all", Type: "all"}}, nil, runnerTestConfig(now))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	internal, err := runner.StartManual(ctx, []int64{1}, false)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	external, err := runner.RecordExternal(ctx, "External", model.SearchResponse{})
	if err != nil || external == nil || external.Trigger != TriggerExternal {
		t.Fatalf("RecordExternal() = %+v, %v", external, err)
	}
	if running, activeID := runner.IsRunning(); !running || activeID != internal.ID {
		t.Fatalf("active collection after external record = %v/%d, want true/%d", running, activeID, internal.ID)
	}
	close(release)
	if err := runner.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runner.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerRestartInvokesRecoveryForCanceledRunningItem(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	repository := &fakeRunRepository{keywords: []Keyword{{ID: 1, Value: "Alpha", Enabled: true}}}
	entered := make(chan struct{})
	var once sync.Once
	runner := NewRunner(repository, LiveSearchFunc(func(ctx context.Context, _ SearchRequest) (model.SearchResponse, error) {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		return model.SearchResponse{}, ctx.Err()
	}), StaticSources{{Key: "all", Type: "all"}}, nil, runnerTestConfig(now))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.StartManual(ctx, []int64{1}, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := runner.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	repository.mu.Lock()
	if len(repository.itemCompletions) != 0 {
		t.Fatalf("canceled item was completed instead of left for recovery: %+v", repository.itemCompletions)
	}
	firstRecoveryCalls := repository.recoverCalls
	repository.mu.Unlock()
	if firstRecoveryCalls != 1 {
		t.Fatalf("initial recovery calls = %d", firstRecoveryCalls)
	}
	if err := runner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	repository.mu.Lock()
	recoveryCalls := repository.recoverCalls
	repository.mu.Unlock()
	if recoveryCalls != 2 {
		t.Fatalf("restart recovery calls = %d, want 2", recoveryCalls)
	}
	if err := runner.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRecordExternalUpdatesManagedKeywordCooldown(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	repository := &fakeRunRepository{
		found:        &Keyword{ID: 9, Value: "Alpha", Normalized: "alpha", Cooldown: 24 * time.Hour},
		ingestResult: IngestResult{New: 1},
	}
	runner := NewRunner(repository, nil, nil, nil, runnerTestConfig(now))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	batch, err := runner.RecordExternal(ctx, "  ALPHA ", model.SearchResponse{Total: 1, Results: []model.SearchResult{{Title: "result"}}})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Trigger != TriggerExternal || batch.Status != StatusSuccess {
		t.Fatalf("external batch = %+v", batch)
	}
	repository.mu.Lock()
	completion := repository.itemCompletions[0]
	request := repository.ingestRequests[0]
	repository.mu.Unlock()
	if request.Trigger != TriggerExternal || request.Keyword.ID != 9 {
		t.Fatalf("external ingest request = %+v", request)
	}
	if completion.NextEligibleAt == nil || !completion.NextEligibleAt.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("external next eligible = %v", completion.NextEligibleAt)
	}
	if err := runner.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRecordExternalWithDataFailsWhenIngestReportsError(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	repository := &fakeRunRepository{ingestErr: errors.New("partial write report")}
	runner := NewRunner(repository, nil, nil, nil, runnerTestConfig(now))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	batch, err := runner.RecordExternal(ctx, "Alpha", model.SearchResponse{Total: 1, Results: []model.SearchResult{{Title: "result"}}})
	if err == nil {
		t.Fatal("RecordExternal() did not report ingest error")
	}
	if batch == nil || batch.Status != StatusFailed {
		t.Fatalf("external batch with returned data = %+v", batch)
	}
	repository.mu.Lock()
	completion := repository.itemCompletions[0]
	repository.mu.Unlock()
	if completion.Status != StatusFailed || completion.NextEligibleAt != nil {
		t.Fatalf("unmanaged external completion = %+v", completion)
	}
	if err := runner.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
