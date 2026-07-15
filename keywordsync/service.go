package keywordsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"sync"
	"time"

	"pansou/keywordsource"
	"pansou/storage"
)

const defaultPollInterval = time.Minute

type Config struct {
	PollInterval time.Duration
	OnError      func(error)
}

type delaySampler func(minSeconds, maxSeconds int) int
type delayWaiter func(context.Context, time.Duration) error

type keywordExistenceStore interface {
	ExistingNormalizedKeywords(context.Context, []string) (map[string]struct{}, error)
}

// Service serializes scheduled and manual API-source synchronization. The
// store claim is the cross-goroutine guard; the mutex keeps this process from
// issuing multiple outbound keyword-source requests at once.
type Service struct {
	store        keywordSourceStore
	pollInterval time.Duration
	onError      func(error)
	sampleDelay  delaySampler
	waitDelay    delayWaiter
	wake         chan struct{}
	owner        string

	mu     sync.Mutex
	runMu  sync.Mutex
	cancel context.CancelFunc
	ctx    context.Context
	done   chan struct{}
}

type keywordSourceStore interface {
	keywordExistenceStore
	ClaimDueKeywordAPISource(context.Context, time.Time) (*storage.KeywordAPISource, error)
	ClaimKeywordAPISourceForSync(context.Context, int64, time.Time) (storage.KeywordAPISource, error)
	CompleteKeywordAPISourceSync(context.Context, storage.KeywordAPISourceSyncInput) (storage.KeywordAPISourceSyncResult, error)
	FailKeywordAPISourceSync(context.Context, int64, string, time.Time) (storage.KeywordAPISource, error)
	FailKeywordAPISourceSyncWithStats(context.Context, int64, string, time.Time, int, int) (storage.KeywordAPISource, error)
}

func New(store *storage.Store, config Config) *Service {
	if store == nil {
		return newService(nil, config)
	}
	return newService(store, config)
}

func newService(store keywordSourceStore, config Config) *Service {
	interval := config.PollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	onError := config.OnError
	if onError == nil {
		onError = func(err error) { log.Printf("API keyword source: %v", err) }
	}
	return &Service{
		store: store, pollInterval: interval, onError: onError,
		sampleDelay: sampleIterationDelay, waitDelay: waitIterationDelay,
		wake: make(chan struct{}, 1), owner: newWorkerID(),
	}
}

func (s *Service) Start(parent context.Context) error {
	if s == nil || s.store == nil {
		return errors.New("keyword API source storage is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.ctx = ctx
	s.done = make(chan struct{})
	done := s.done
	go s.loop(ctx, done)
	return nil
}

func (s *Service) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel = nil
	s.ctx = nil
	s.done = nil
	s.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TriggerNow persists a queued run and wakes the background worker. The
// variadic trigger keeps older callers source-compatible while allowing the
// admin UI to distinguish a normal manual sync from "save and sync".
func (s *Service) TriggerNow(ctx context.Context, id int64, triggers ...string) (storage.KeywordAPISyncRun, bool, error) {
	if s == nil || s.store == nil {
		return storage.KeywordAPISyncRun{}, false, errors.New("keyword API source service is unavailable")
	}
	s.mu.Lock()
	runCtx := s.ctx
	s.mu.Unlock()
	if runCtx == nil {
		return storage.KeywordAPISyncRun{}, false, errors.New("keyword API source service is not running")
	}
	tracked, ok := s.store.(trackedKeywordSourceStore)
	if !ok {
		return storage.KeywordAPISyncRun{}, false, errors.New("keyword API source history storage is unavailable")
	}
	trigger := storage.KeywordAPISyncTriggerManual
	if len(triggers) > 0 && triggers[0] == storage.KeywordAPISyncTriggerSave {
		trigger = storage.KeywordAPISyncTriggerSave
	}
	run, alreadyActive, err := tracked.EnqueueKeywordAPISourceSync(ctx, id, trigger, time.Now())
	if err != nil {
		return storage.KeywordAPISyncRun{}, false, err
	}
	s.wakeWorker()
	return run, alreadyActive, nil
}

func (s *Service) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	if tracked, ok := s.store.(trackedKeywordSourceStore); ok {
		s.trackedLoop(ctx, tracked)
		return
	}
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	s.runDue(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runDue(ctx)
		}
	}
}

func (s *Service) runDue(ctx context.Context) {
	for ctx.Err() == nil {
		source, err := s.store.ClaimDueKeywordAPISource(ctx, time.Now())
		if err != nil {
			s.onError(err)
			return
		}
		if source == nil {
			return
		}
		if _, err := s.syncClaimed(ctx, *source); err != nil {
			s.onError(fmt.Errorf("sync source %d: %w", source.ID, err))
		}
	}
}

// SyncNow claims and synchronizes one source through the same code path used
// by the scheduler.
func (s *Service) SyncNow(ctx context.Context, id int64) (storage.KeywordAPISourceSyncResult, error) {
	if s == nil || s.store == nil {
		return storage.KeywordAPISourceSyncResult{}, errors.New("keyword API source service is unavailable")
	}
	source, err := s.store.ClaimKeywordAPISourceForSync(ctx, id, time.Now())
	if err != nil {
		return storage.KeywordAPISourceSyncResult{}, err
	}
	return s.syncClaimed(ctx, source)
}

func (s *Service) syncClaimed(ctx context.Context, source storage.KeywordAPISource) (storage.KeywordAPISourceSyncResult, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}

	baseConfig, err := RequestConfig(source)
	iteration := IterationConfig(source)
	if err == nil {
		_, err = keywordsource.ParsePath(source.ResponsePath)
	}
	if err == nil {
		err = keywordsource.ValidateIterationConfig(baseConfig, iteration)
	}
	if err != nil {
		redacted := keywordsource.RedactError(err, baseConfig)
		_, recordErr := s.store.FailKeywordAPISourceSync(context.WithoutCancel(ctx), source.ID, redacted.Error(), time.Now())
		if recordErr != nil {
			return storage.KeywordAPISourceSyncResult{}, fmt.Errorf("%v; record failure: %w", redacted, recordErr)
		}
		return storage.KeywordAPISourceSyncResult{}, redacted
	}

	values := make([]string, 0)
	seenValues := make(map[string]struct{})
	errorsByIteration := make([]string, 0)
	requestCount, successCount, failureCount := 0, 0, 0
	noKeywordStreak := 0
	sampleDelay := s.sampleDelay
	if sampleDelay == nil {
		sampleDelay = sampleIterationDelay
	}
	waitDelay := s.waitDelay
	if waitDelay == nil {
		waitDelay = waitIterationDelay
	}

iterationLoop:
	for index := 0; iterationAllowsIndex(iteration, index); index++ {
		if ctxErr := ctx.Err(); ctxErr != nil {
			errorsByIteration = append(errorsByIteration, "iteration cancelled: "+ctxErr.Error())
			requestCount++
			failureCount++
			break
		}
		if index > 0 {
			randomSeconds := sampleDelay(iteration.RandomDelayMinSeconds, iteration.RandomDelayMaxSeconds)
			delay := combinedIterationDelay(iteration.DelaySeconds, randomSeconds)
			if waitErr := waitDelay(ctx, delay); waitErr != nil {
				requestCount++
				failureCount++
				if ctxErr := ctx.Err(); ctxErr != nil {
					errorsByIteration = append(errorsByIteration, "iteration cancelled: "+ctxErr.Error())
				} else {
					errorsByIteration = append(errorsByIteration, "iteration delay failed: "+waitErr.Error())
				}
				break
			}
		}
		config, iterationValue, deriveErr := keywordsource.DeriveRequest(baseConfig, iteration, index)
		requestCount++
		if deriveErr != nil {
			failureCount++
			noKeywordStreak++
			errorsByIteration = append(errorsByIteration, iterationError(index, iterationValue, deriveErr, baseConfig))
			if iterationReachedNoKeywordLimit(iteration, noKeywordStreak) {
				break
			}
			continue
		}
		response, executeErr := keywordsource.Execute(ctx, config)
		if executeErr != nil {
			failureCount++
			noKeywordStreak++
			errorsByIteration = append(errorsByIteration, iterationError(index, iterationValue, executeErr, config))
			if ctx.Err() != nil || iterationReachedNoKeywordLimit(iteration, noKeywordStreak) {
				break
			}
			continue
		}
		extraction, extractErr := keywordsource.ExtractKeywords(response.JSON, source.ResponsePath)
		if extractErr != nil {
			failureCount++
			noKeywordStreak++
			errorsByIteration = append(errorsByIteration, iterationError(index, iterationValue, extractErr, config))
			if iterationReachedNoKeywordLimit(iteration, noKeywordStreak) {
				break
			}
			continue
		}
		progress, progressErr := evaluateIterationKeywordProgress(ctx, s.store, iteration, extraction.Values, seenValues)
		if progressErr != nil {
			failureCount++
			message := fmt.Sprintf("iteration %d strict stop check failed: %v", index+1, progressErr)
			_, recordErr := s.store.FailKeywordAPISourceSyncWithStats(context.WithoutCancel(ctx), source.ID, message, time.Now(), requestCount, failureCount)
			if recordErr != nil {
				return storage.KeywordAPISourceSyncResult{}, fmt.Errorf("%s; record failure: %w", message, recordErr)
			}
			return storage.KeywordAPISourceSyncResult{}, errors.New(message)
		}
		successCount++
		if progress.hasProgress {
			noKeywordStreak = 0
		} else {
			noKeywordStreak++
		}
		for _, value := range progress.newValues {
			values = append(values, value.Value)
		}
		if iterationReachedNoKeywordLimit(iteration, noKeywordStreak) {
			break iterationLoop
		}
	}
	errorSummary := strings.Join(errorsByIteration, "; ")
	if successCount == 0 {
		if strings.TrimSpace(errorSummary) == "" {
			errorSummary = "all keyword API source iterations failed"
		}
		_, recordErr := s.store.FailKeywordAPISourceSyncWithStats(context.WithoutCancel(ctx), source.ID, errorSummary, time.Now(), requestCount, failureCount)
		if recordErr != nil {
			return storage.KeywordAPISourceSyncResult{}, fmt.Errorf("%s; record failure: %w", errorSummary, recordErr)
		}
		return storage.KeywordAPISourceSyncResult{}, errors.New(errorSummary)
	}
	status := storage.KeywordAPISourceStatusSuccess
	if failureCount > 0 {
		status = storage.KeywordAPISourceStatusPartial
	}
	result, err := s.store.CompleteKeywordAPISourceSync(context.WithoutCancel(ctx), storage.KeywordAPISourceSyncInput{
		SourceID: source.ID, Values: values, SyncedAt: time.Now(), Status: status, ErrorMessage: errorSummary,
		RequestCount: requestCount, SuccessCount: successCount, FailureCount: failureCount,
	})
	if err != nil {
		_, _ = s.store.FailKeywordAPISourceSyncWithStats(context.WithoutCancel(ctx), source.ID, "save synchronized keywords failed", time.Now(), requestCount, failureCount)
		return storage.KeywordAPISourceSyncResult{}, err
	}
	return result, nil
}

func IterationConfig(source storage.KeywordAPISource) keywordsource.IterationConfig {
	return keywordsource.IterationConfig{
		Enabled: source.IterationEnabled, Location: keywordsource.IterationLocation(source.IterationLocation),
		Path: source.IterationPath, Start: source.IterationStart, Step: source.IterationStep,
		Count: source.IterationCount, DelaySeconds: source.IterationDelaySeconds,
		Unlimited: source.IterationUnlimited, NoKeywordStopCount: source.IterationNoKeywordStopCount,
		StopMode:              keywordsource.IterationStopMode(source.IterationStopMode),
		RandomDelayMinSeconds: source.IterationRandomDelayMinSeconds,
		RandomDelayMaxSeconds: source.IterationRandomDelayMaxSeconds,
	}
}

type iterationKeywordProgress struct {
	newValues   []keywordsource.KeywordValue
	hasProgress bool
}

func evaluateIterationKeywordProgress(
	ctx context.Context,
	store keywordExistenceStore,
	iteration keywordsource.IterationConfig,
	extracted []keywordsource.KeywordValue,
	seenValues map[string]struct{},
) (iterationKeywordProgress, error) {
	progress := iterationKeywordProgress{newValues: make([]keywordsource.KeywordValue, 0, len(extracted))}
	for _, value := range extracted {
		if _, exists := seenValues[value.Normalized]; exists {
			continue
		}
		seenValues[value.Normalized] = struct{}{}
		progress.newValues = append(progress.newValues, value)
	}
	if !iteration.Enabled || iteration.NoKeywordStopCount <= 0 || keywordsource.NormalizeIterationStopMode(iteration.StopMode) == keywordsource.IterationStopModeNormal {
		progress.hasProgress = len(extracted) > 0
		return progress, nil
	}
	if len(progress.newValues) == 0 {
		return progress, nil
	}
	normalized := make([]string, len(progress.newValues))
	for index, value := range progress.newValues {
		normalized[index] = value.Normalized
	}
	existing, err := store.ExistingNormalizedKeywords(ctx, normalized)
	if err != nil {
		return iterationKeywordProgress{}, err
	}
	for _, value := range progress.newValues {
		if _, exists := existing[value.Normalized]; !exists {
			progress.hasProgress = true
			break
		}
	}
	return progress, nil
}

func iterationAllowsIndex(iteration keywordsource.IterationConfig, index int) bool {
	if !iteration.Enabled {
		return index == 0
	}
	return iteration.Unlimited || index < iteration.Count
}

func iterationReachedNoKeywordLimit(iteration keywordsource.IterationConfig, streak int) bool {
	return iteration.NoKeywordStopCount > 0 && streak >= iteration.NoKeywordStopCount
}

func sampleIterationDelay(minSeconds, maxSeconds int) int {
	if minSeconds >= maxSeconds {
		return minSeconds
	}
	return minSeconds + rand.Intn(maxSeconds-minSeconds+1)
}

func combinedIterationDelay(fixedSeconds, randomSeconds int) time.Duration {
	totalSeconds := fixedSeconds + randomSeconds
	if totalSeconds < 0 {
		totalSeconds = 0
	}
	return time.Duration(totalSeconds) * time.Second
}

func waitIterationDelay(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func iterationError(index int, value int64, err error, config keywordsource.RequestConfig) string {
	redacted := keywordsource.RedactError(err, config)
	return fmt.Sprintf("iteration %d (value %d): %v", index+1, value, redacted)
}

// RequestConfig converts the persistence model to the HTTP executor model.
// Form bodies are stored as a JSON object in request_body.
func RequestConfig(source storage.KeywordAPISource) (keywordsource.RequestConfig, error) {
	config := keywordsource.RequestConfig{
		Executor:       keywordsource.RequestExecutor(source.RequestExecutor),
		Method:         source.RequestMethod,
		URL:            source.RequestURL,
		Headers:        source.RequestHeaders,
		Query:          source.QueryParams,
		BodyType:       keywordsource.BodyType(source.BodyType),
		Body:           source.RequestBody,
		ProxyURL:       source.ProxyURL,
		TimeoutSeconds: source.TimeoutSeconds,
	}
	if strings.EqualFold(source.BodyType, string(keywordsource.BodyForm)) && strings.TrimSpace(source.RequestBody) != "" {
		if err := json.Unmarshal([]byte(source.RequestBody), &config.Form); err != nil {
			return config, fmt.Errorf("invalid stored form body: %w", err)
		}
	}
	return config, keywordsource.ValidateRequestConfig(config)
}
