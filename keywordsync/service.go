package keywordsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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

// Service serializes scheduled and manual API-source synchronization. The
// store claim is the cross-goroutine guard; the mutex keeps this process from
// issuing multiple outbound keyword-source requests at once.
type Service struct {
	store        keywordSourceStore
	pollInterval time.Duration
	onError      func(error)

	mu     sync.Mutex
	runMu  sync.Mutex
	cancel context.CancelFunc
	ctx    context.Context
	done   chan struct{}
}

type keywordSourceStore interface {
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
	return &Service{store: store, pollInterval: interval, onError: onError}
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

// TriggerNow claims a source synchronously and performs the potentially long
// iterative run in the service background context. This keeps the admin HTTP
// request below the server write timeout even when iterations include delays.
func (s *Service) TriggerNow(ctx context.Context, id int64) (storage.KeywordAPISource, error) {
	if s == nil || s.store == nil {
		return storage.KeywordAPISource{}, errors.New("keyword API source service is unavailable")
	}
	s.mu.Lock()
	runCtx := s.ctx
	s.mu.Unlock()
	if runCtx == nil {
		return storage.KeywordAPISource{}, errors.New("keyword API source service is not running")
	}
	source, err := s.store.ClaimKeywordAPISourceForSync(ctx, id, time.Now())
	if err != nil {
		return storage.KeywordAPISource{}, err
	}
	go func() {
		if _, syncErr := s.syncClaimed(runCtx, source); syncErr != nil {
			s.onError(fmt.Errorf("sync source %d: %w", source.ID, syncErr))
		}
	}()
	return source, nil
}

func (s *Service) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
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

	baseConfig, err := RequestConfig(source)
	if err == nil {
		_, err = keywordsource.ParsePath(source.ResponsePath)
	}
	if err == nil {
		err = keywordsource.ValidateIterationConfig(baseConfig, IterationConfig(source))
	}
	if err == nil {
		_, err = keywordsource.IterationValues(IterationConfig(source))
	}
	if err != nil {
		redacted := keywordsource.RedactError(err, baseConfig)
		_, recordErr := s.store.FailKeywordAPISourceSync(context.WithoutCancel(ctx), source.ID, redacted.Error(), time.Now())
		if recordErr != nil {
			return storage.KeywordAPISourceSyncResult{}, fmt.Errorf("%v; record failure: %w", redacted, recordErr)
		}
		return storage.KeywordAPISourceSyncResult{}, redacted
	}

	iteration := IterationConfig(source)
	sequence, _ := keywordsource.IterationValues(iteration)
	values := make([]string, 0)
	seenValues := make(map[string]struct{})
	errorsByIteration := make([]string, 0)
	requestCount, successCount, failureCount := 0, 0, 0
iterationLoop:
	for index := range sequence {
		if index > 0 && iteration.Enabled && iteration.DelaySeconds > 0 {
			timer := time.NewTimer(time.Duration(iteration.DelaySeconds) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				errorsByIteration = append(errorsByIteration, "iteration cancelled: "+ctx.Err().Error())
				failureCount++
				break iterationLoop
			case <-timer.C:
			}
		}
		config, iterationValue, deriveErr := keywordsource.DeriveRequest(baseConfig, iteration, index)
		requestCount++
		if deriveErr != nil {
			failureCount++
			errorsByIteration = append(errorsByIteration, iterationError(index, iterationValue, deriveErr, baseConfig))
			continue
		}
		response, executeErr := keywordsource.Execute(ctx, config)
		if executeErr != nil {
			failureCount++
			errorsByIteration = append(errorsByIteration, iterationError(index, iterationValue, executeErr, config))
			continue
		}
		extraction, extractErr := keywordsource.ExtractKeywords(response.JSON, source.ResponsePath)
		if extractErr != nil {
			failureCount++
			errorsByIteration = append(errorsByIteration, iterationError(index, iterationValue, extractErr, config))
			continue
		}
		successCount++
		for _, value := range extraction.Values {
			if _, exists := seenValues[value.Normalized]; exists {
				continue
			}
			seenValues[value.Normalized] = struct{}{}
			values = append(values, value.Value)
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
	result, err := s.store.CompleteKeywordAPISourceSync(ctx, storage.KeywordAPISourceSyncInput{
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
