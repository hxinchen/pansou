package keywordsync

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"pansou/keywordsource"
	"pansou/storage"
)

const (
	keywordSyncHeartbeatInterval     = 10 * time.Second
	keywordSyncLeaseDuration         = 45 * time.Second
	keywordSyncHeartbeatStoreTimeout = 5 * time.Second
	keywordSyncStoreTimeout          = 10 * time.Second
	keywordSyncFinalizeStoreTimeout  = 30 * time.Second
	keywordSyncSampleLimit           = 5
	keywordSyncSampleMaxRunes        = 120
)

type trackedKeywordSourceStore interface {
	EnqueueKeywordAPISourceSync(context.Context, int64, string, time.Time) (storage.KeywordAPISyncRun, bool, error)
	EnqueueDueKeywordAPISourceSync(context.Context, time.Time) (*storage.KeywordAPISyncRun, error)
	ClaimNextKeywordAPISyncRun(context.Context, string, string, time.Time, time.Time) (*storage.KeywordAPISyncClaim, error)
	RenewKeywordAPISyncRunLease(context.Context, int64, string, string, time.Time, time.Time) error
	BeginKeywordAPISyncIteration(context.Context, storage.KeywordAPISyncIterationInput) (storage.KeywordAPISyncIteration, error)
	CompleteKeywordAPISyncIteration(context.Context, storage.KeywordAPISyncIterationInput) (storage.KeywordAPISyncIteration, error)
	FinalizeKeywordAPISyncRun(context.Context, storage.KeywordAPISyncFinalizeInput) (storage.KeywordAPISyncRun, storage.KeywordAPISourceSyncResult, error)
	FailKeywordAPISyncRun(context.Context, int64, string, string, string, time.Time, int, int) (storage.KeywordAPISyncRun, error)
	InterruptKeywordAPISyncRun(context.Context, int64, string, string, string, time.Time) (storage.KeywordAPISyncRun, error)
	RecoverExpiredKeywordAPISyncRuns(context.Context, time.Time) (int, error)
}

func newWorkerID() string {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return hex.EncodeToString(bytes[:])
	}
	return fmt.Sprintf("worker-%d", time.Now().UnixNano())
}

func keywordSyncStoreContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), timeout)
}

func (s *Service) wakeWorker() {
	if s == nil || s.wake == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) trackedLoop(ctx context.Context, store trackedKeywordSourceStore) {
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()

	s.runTrackedCycle(ctx, store)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
			s.runTrackedCycle(ctx, store)
		case <-ticker.C:
			s.runTrackedCycle(ctx, store)
		}
	}
}

func (s *Service) runTrackedCycle(ctx context.Context, store trackedKeywordSourceStore) {
	if ctx.Err() != nil {
		return
	}
	if _, err := store.RecoverExpiredKeywordAPISyncRuns(ctx, time.Now()); err != nil {
		s.onError(fmt.Errorf("recover expired API keyword sync runs: %w", err))
	}
	for ctx.Err() == nil {
		run, err := store.EnqueueDueKeywordAPISourceSync(ctx, time.Now())
		if err != nil {
			s.onError(fmt.Errorf("enqueue due API keyword source: %w", err))
			break
		}
		if run == nil {
			break
		}
	}
	for ctx.Err() == nil {
		token := newWorkerID()
		now := time.Now()
		claim, err := store.ClaimNextKeywordAPISyncRun(ctx, s.owner, token, now, now.Add(keywordSyncLeaseDuration))
		if err != nil {
			s.onError(fmt.Errorf("claim API keyword sync run: %w", err))
			return
		}
		if claim == nil {
			return
		}
		if err := s.executeTrackedRun(ctx, store, *claim, token); err != nil && ctx.Err() == nil {
			s.onError(fmt.Errorf("sync source %d run %d: %w", claim.Source.ID, claim.Run.ID, err))
		}
	}
}

func (s *Service) executeTrackedRun(parent context.Context, store trackedKeywordSourceStore, claim storage.KeywordAPISyncClaim, token string) error {
	s.runMu.Lock()
	defer s.runMu.Unlock()

	runCtx, cancel := context.WithCancel(parent)
	heartbeatDone := make(chan error, 1)
	go s.renewTrackedLease(runCtx, cancel, store, claim.Run.ID, token, heartbeatDone)
	defer func() {
		cancel()
		<-heartbeatDone
	}()

	source := claim.Source
	baseConfig, err := RequestConfig(source)
	iteration := IterationConfig(source)
	if err == nil {
		_, err = keywordsource.ParsePath(source.ResponsePath)
	}
	if err == nil {
		err = keywordsource.ValidateIterationConfig(baseConfig, iteration)
	}
	if err != nil {
		return s.failTrackedRun(store, claim.Run.ID, token, keywordsource.RedactError(err, baseConfig), 0, 0)
	}

	values := make([]string, 0)
	valueSequences := make([]int, 0)
	seenValues := make(map[string]struct{})
	errorsByIteration := make([]string, 0)
	requestCount, successCount, failureCount, rawItemCount := 0, 0, 0, 0
	noKeywordStreak := 0

	for index := 0; iterationAllowsIndex(iteration, index); index++ {
		if err := runCtx.Err(); err != nil {
			return s.interruptTrackedRun(store, claim.Run.ID, token, err)
		}
		if index > 0 {
			randomSeconds := s.sampleDelay(iteration.RandomDelayMinSeconds, iteration.RandomDelayMaxSeconds)
			if err := s.waitDelay(runCtx, combinedIterationDelay(iteration.DelaySeconds, randomSeconds)); err != nil {
				return s.interruptTrackedRun(store, claim.Run.ID, token, err)
			}
		}

		iterationValue, valueErr := keywordsource.IterationValue(iteration, index)
		startedAt := time.Now()
		input := storage.KeywordAPISyncIterationInput{
			RunID: claim.Run.ID, LeaseOwner: s.owner, LeaseToken: token,
			Sequence: index + 1, IterationValue: iterationValue,
			Status: storage.KeywordAPISyncIterationStatusRunning, StartedAt: startedAt,
		}
		storeCtx, storeCancel := keywordSyncStoreContext(runCtx, keywordSyncStoreTimeout)
		_, beginErr := store.BeginKeywordAPISyncIteration(storeCtx, input)
		storeCancel()
		if beginErr != nil {
			return beginErr
		}
		requestCount++

		config, _, deriveErr := keywordsource.DeriveRequest(baseConfig, iteration, index)
		if valueErr != nil && deriveErr == nil {
			deriveErr = valueErr
		}
		if deriveErr != nil {
			failureCount++
			noKeywordStreak++
			message := iterationError(index, iterationValue, deriveErr, baseConfig)
			errorsByIteration = append(errorsByIteration, message)
			input.Status = storage.KeywordAPISyncIterationStatusFailed
			input.ErrorMessage = message
			input.CompletedAt = time.Now()
			storeCtx, storeCancel := keywordSyncStoreContext(runCtx, keywordSyncStoreTimeout)
			_, completeErr := store.CompleteKeywordAPISyncIteration(storeCtx, input)
			storeCancel()
			if completeErr != nil {
				return completeErr
			}
			if iterationReachedNoKeywordLimit(iteration, noKeywordStreak) {
				break
			}
			continue
		}

		response, executeErr := keywordsource.Execute(runCtx, config)
		input.HTTPStatus = response.StatusCode
		input.DurationMS = response.Duration.Milliseconds()
		input.ResponseBytes = int64(response.SizeBytes)
		if executeErr != nil {
			if runCtx.Err() != nil {
				input.Status = storage.KeywordAPISyncIterationStatusFailed
				input.ErrorMessage = "iteration interrupted: " + runCtx.Err().Error()
				input.CompletedAt = time.Now()
				storeCtx, storeCancel := keywordSyncStoreContext(runCtx, keywordSyncStoreTimeout)
				_, _ = store.CompleteKeywordAPISyncIteration(storeCtx, input)
				storeCancel()
				return s.interruptTrackedRun(store, claim.Run.ID, token, runCtx.Err())
			}
			failureCount++
			noKeywordStreak++
			message := iterationError(index, iterationValue, executeErr, config)
			errorsByIteration = append(errorsByIteration, message)
			input.Status = storage.KeywordAPISyncIterationStatusFailed
			input.ErrorMessage = message
			input.CompletedAt = time.Now()
			storeCtx, storeCancel := keywordSyncStoreContext(runCtx, keywordSyncStoreTimeout)
			_, completeErr := store.CompleteKeywordAPISyncIteration(storeCtx, input)
			storeCancel()
			if completeErr != nil {
				return completeErr
			}
			if iterationReachedNoKeywordLimit(iteration, noKeywordStreak) {
				break
			}
			continue
		}

		extraction, extractErr := keywordsource.ExtractKeywords(response.JSON, source.ResponsePath)
		if extractErr != nil {
			failureCount++
			noKeywordStreak++
			message := iterationError(index, iterationValue, extractErr, config)
			errorsByIteration = append(errorsByIteration, message)
			input.Status = storage.KeywordAPISyncIterationStatusFailed
			input.ErrorMessage = message
			input.CompletedAt = time.Now()
			storeCtx, storeCancel := keywordSyncStoreContext(runCtx, keywordSyncStoreTimeout)
			_, completeErr := store.CompleteKeywordAPISyncIteration(storeCtx, input)
			storeCancel()
			if completeErr != nil {
				return completeErr
			}
			if iterationReachedNoKeywordLimit(iteration, noKeywordStreak) {
				break
			}
			continue
		}

		successCount++
		rawItemCount += extraction.RawCount
		input.Status = storage.KeywordAPISyncIterationStatusSuccess
		input.RawItemCount = extraction.RawCount
		input.UniqueItemCount = extraction.UniqueCount
		input.Samples = keywordSyncSamples(extraction.Values)
		if len(extraction.Values) == 0 {
			noKeywordStreak++
		} else {
			noKeywordStreak = 0
		}
		for _, value := range extraction.Values {
			if _, exists := seenValues[value.Normalized]; exists {
				continue
			}
			seenValues[value.Normalized] = struct{}{}
			values = append(values, value.Value)
			valueSequences = append(valueSequences, index+1)
			input.CrossIterationNew++
		}
		input.CompletedAt = time.Now()
		storeCtx, storeCancel = keywordSyncStoreContext(runCtx, keywordSyncStoreTimeout)
		_, completeErr := store.CompleteKeywordAPISyncIteration(storeCtx, input)
		storeCancel()
		if completeErr != nil {
			return completeErr
		}
		if iterationReachedNoKeywordLimit(iteration, noKeywordStreak) {
			break
		}
	}

	if err := runCtx.Err(); err != nil {
		return s.interruptTrackedRun(store, claim.Run.ID, token, err)
	}
	errorSummary := strings.Join(errorsByIteration, "; ")
	if successCount == 0 {
		if strings.TrimSpace(errorSummary) == "" {
			errorSummary = "all keyword API source iterations failed"
		}
		return s.failTrackedRun(store, claim.Run.ID, token, errors.New(errorSummary), requestCount, failureCount)
	}
	status := storage.KeywordAPISyncRunStatusSuccess
	if failureCount > 0 {
		status = storage.KeywordAPISyncRunStatusPartial
	}
	storeCtx, storeCancel := keywordSyncStoreContext(runCtx, keywordSyncFinalizeStoreTimeout)
	_, _, err = store.FinalizeKeywordAPISyncRun(storeCtx, storage.KeywordAPISyncFinalizeInput{
		RunID: claim.Run.ID, LeaseOwner: s.owner, LeaseToken: token,
		Values: values, ValueSequences: valueSequences, SyncedAt: time.Now(), Status: status, ErrorMessage: errorSummary,
		RawItemCount: rawItemCount, RequestCount: requestCount,
		SuccessCount: successCount, FailureCount: failureCount,
	})
	storeCancel()
	if err != nil {
		_ = s.failTrackedRun(store, claim.Run.ID, token, errors.New("save synchronized keywords failed"), requestCount, failureCount)
	}
	return err
}

func (s *Service) renewTrackedLease(ctx context.Context, cancel context.CancelFunc, store trackedKeywordSourceStore, runID int64, token string, done chan<- error) {
	ticker := time.NewTicker(keywordSyncHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			done <- nil
			return
		case at := <-ticker.C:
			storeCtx, storeCancel := keywordSyncStoreContext(ctx, keywordSyncHeartbeatStoreTimeout)
			err := store.RenewKeywordAPISyncRunLease(storeCtx, runID, s.owner, token, at.Add(keywordSyncLeaseDuration), at)
			storeCancel()
			if err != nil {
				cancel()
				done <- err
				return
			}
		}
	}
}

func (s *Service) failTrackedRun(store trackedKeywordSourceStore, runID int64, token string, err error, requestCount, failureCount int) error {
	message := strings.TrimSpace(err.Error())
	storeCtx, storeCancel := keywordSyncStoreContext(context.Background(), keywordSyncStoreTimeout)
	_, recordErr := store.FailKeywordAPISyncRun(storeCtx, runID, s.owner, token, message, time.Now(), requestCount, failureCount)
	storeCancel()
	if recordErr != nil {
		return fmt.Errorf("%s; record failure: %w", message, recordErr)
	}
	return err
}

func (s *Service) interruptTrackedRun(store trackedKeywordSourceStore, runID int64, token string, cause error) error {
	message := "service interrupted"
	if cause != nil {
		message += ": " + cause.Error()
	}
	storeCtx, storeCancel := keywordSyncStoreContext(context.Background(), keywordSyncStoreTimeout)
	_, err := store.InterruptKeywordAPISyncRun(storeCtx, runID, s.owner, token, message, time.Now())
	storeCancel()
	if err != nil {
		return fmt.Errorf("%s; record interruption: %w", message, err)
	}
	return cause
}

func keywordSyncSamples(values []keywordsource.KeywordValue) []string {
	limit := len(values)
	if limit > keywordSyncSampleLimit {
		limit = keywordSyncSampleLimit
	}
	samples := make([]string, 0, limit)
	for _, value := range values[:limit] {
		sample := strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, strings.TrimSpace(value.Value))
		runes := []rune(sample)
		if len(runes) > keywordSyncSampleMaxRunes {
			sample = string(runes[:keywordSyncSampleMaxRunes])
		}
		if sample != "" {
			samples = append(samples, sample)
		}
	}
	return samples
}
