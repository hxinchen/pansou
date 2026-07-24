package integration

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"pansou/collection"
	"pansou/model"
	"pansou/plugin"
	"pansou/proxypool"
	"pansou/service"
	"pansou/storage"
)

type CollectionRepository struct {
	Store *storage.Store
}

func (r *CollectionRepository) SelectKeywords(ctx context.Context, selection collection.KeywordSelection) ([]collection.Keyword, error) {
	if r == nil || r.Store == nil {
		return nil, errors.New("resource library is disabled")
	}
	keywords := make([]storage.Keyword, 0)
	if len(selection.IDs) > 0 {
		for _, id := range selection.IDs {
			keyword, err := r.Store.GetKeyword(ctx, id)
			if errors.Is(err, storage.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			keywords = append(keywords, keyword)
		}
	} else {
		eligible, err := r.Store.ListEligibleKeywords(ctx, time.Now(), 10000)
		if err != nil {
			return nil, err
		}
		keywords = eligible
	}
	result := make([]collection.Keyword, 0, len(keywords))
	for _, keyword := range keywords {
		if selection.EnabledOnly && !keyword.Enabled {
			continue
		}
		result = append(result, toCollectionKeyword(keyword))
	}
	return result, nil
}

func (r *CollectionRepository) FindKeyword(ctx context.Context, normalized string) (*collection.Keyword, error) {
	keyword, err := r.Store.GetKeywordByNormalized(ctx, normalized)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	converted := toCollectionKeyword(keyword)
	return &converted, nil
}

func (r *CollectionRepository) CreateBatch(ctx context.Context, input collection.NewBatch) (collection.Batch, error) {
	keywords := make([]storage.RunKeywordInput, 0, len(input.Keywords))
	for _, keyword := range input.Keywords {
		var id *int64
		if keyword.ID > 0 {
			value := keyword.ID
			id = &value
		}
		keywords = append(keywords, storage.RunKeywordInput{
			KeywordID: id, Keyword: keyword.Value, KeywordType: keyword.KeywordType, Priority: keyword.Priority,
			CooldownSeconds: durationSecondsPointer(keyword.Cooldown),
		})
	}
	run, err := r.Store.CreateRun(ctx, storage.CreateRunInput{
		Trigger: string(input.Trigger), Force: input.Forced, Keywords: keywords,
	})
	if err != nil {
		return collection.Batch{}, err
	}
	batch := toCollectionBatch(run)
	snapshots := make(map[string]collection.Keyword, len(input.Keywords))
	for _, keyword := range input.Keywords {
		snapshots[keyword.Normalized] = keyword
	}
	for index := range batch.Items {
		if snapshot, exists := snapshots[batch.Items[index].Keyword.Normalized]; exists {
			batch.Items[index].Keyword = snapshot
		}
	}
	return batch, nil
}

func (r *CollectionRepository) MarkItemRunning(ctx context.Context, runID, itemID int64, startedAt time.Time) error {
	return r.Store.MarkRunItemRunning(ctx, runID, itemID, startedAt)
}

func (r *CollectionRepository) Ingest(ctx context.Context, request collection.IngestRequest) (collection.IngestResult, error) {
	sourceType, sourceKey := collectionSourceIdentity(request.Source)
	summary, err := r.Store.UpsertSearchResponseFromSource(
		ctx,
		request.Keyword.Value,
		request.Keyword.KeywordType,
		string(request.Trigger),
		sourceType,
		sourceKey,
		request.Response,
	)
	if err != nil {
		return collection.IngestResult{}, err
	}
	return collection.IngestResult{
		New: summary.Inserted, Duplicate: summary.Duplicates,
		CheckCandidates: toLinkCheckCandidates(summary.CheckCandidates),
	}, nil
}

func collectionSourceIdentity(source collection.Source) (string, string) {
	sourceType := strings.ToLower(strings.TrimSpace(source.Type))
	sourceKey := strings.TrimSpace(source.Key)
	if sourceKey == "" {
		switch sourceType {
		case "tg":
			if len(source.Channels) == 1 {
				sourceKey = strings.TrimSpace(source.Channels[0])
			}
		case "plugin":
			if len(source.Plugins) == 1 {
				sourceKey = strings.TrimSpace(source.Plugins[0])
			}
		}
	}
	return sourceType, sourceKey
}

func (r *CollectionRepository) CompleteItem(ctx context.Context, _ int64, itemID int64, completion collection.ItemCompletion) error {
	resultCount := 0
	summary := make(map[string]any, len(completion.SourceSummary))
	for key, entry := range completion.SourceSummary {
		resultCount += entry.ResultCount
		summary[key] = entry
	}
	_, err := r.Store.CompleteRunItem(ctx, itemID, storage.CompleteRunItemInput{
		Status: string(completion.Status), FoundCount: resultCount, NewCount: completion.NewCount,
		DuplicateCount: completion.DuplicateCount, SourceSummary: summary, ErrorMessage: completion.Error,
		CompletedAt: completion.FinishedAt, NextEligibleAt: completion.NextEligibleAt,
	})
	return err
}

func (r *CollectionRepository) CompleteBatch(ctx context.Context, batchID int64, completion collection.BatchCompletion) error {
	return r.Store.CompleteRun(ctx, batchID, string(completion.Status), completion.FinishedAt)
}

func (r *CollectionRepository) RecoverRunning(ctx context.Context, _ time.Time) (int, error) {
	count, err := r.Store.RecoverRunningItems(ctx)
	return int(count), err
}

func (r *CollectionRepository) ClaimPending(ctx context.Context) (*collection.ClaimedRunItem, error) {
	if r == nil || r.Store == nil {
		return nil, errors.New("resource library is disabled")
	}
	item, err := r.Store.ClaimNextRunItem(ctx)
	if err != nil || item == nil {
		return nil, err
	}
	run, err := r.Store.GetRunExecutionContext(ctx, item.RunID)
	if err != nil {
		return nil, err
	}
	batch := toCollectionBatch(run)
	keywordID := int64(0)
	if item.KeywordID != nil {
		keywordID = *item.KeywordID
	}
	claimed := collection.RunItem{
		ID: item.ID, BatchID: item.RunID, Status: collection.RunStatus(item.Status),
		Keyword: collection.Keyword{
			ID: keywordID, Value: item.Keyword, Normalized: item.NormalizedKeyword,
			KeywordType: item.KeywordType, Priority: item.Priority,
			Cooldown: secondsDuration(item.CooldownSeconds),
		},
	}
	startedAt := time.Now().UTC()
	if item.StartedAt != nil {
		startedAt = item.StartedAt.UTC()
	}
	return &collection.ClaimedRunItem{Batch: batch, Item: claimed, StartedAt: startedAt}, nil
}

func (r *CollectionRepository) CompleteLinkCheck(ctx context.Context, result collection.LinkCheckResult) error {
	return r.Store.CompleteResourceCheck(ctx, result.ResourceID, string(result.Status), result.CheckedAt)
}

func (r *CollectionRepository) CompleteLinkChecks(ctx context.Context, results []collection.LinkCheckResult) error {
	if r == nil || r.Store == nil {
		return errors.New("resource library is disabled")
	}
	completions := make([]storage.ResourceCheckCompletion, len(results))
	for index, result := range results {
		completions[index] = storage.ResourceCheckCompletion{
			ResourceID: result.ResourceID,
			Status:     string(result.Status),
			CheckedAt:  result.CheckedAt,
		}
	}
	return r.Store.CompleteResourceChecks(ctx, completions)
}

func (r *CollectionRepository) DueLinkChecks(ctx context.Context, limit int, at time.Time) ([]collection.LinkCheckCandidate, error) {
	if r == nil || r.Store == nil {
		return nil, errors.New("resource library is disabled")
	}
	policy, err := r.Store.GetLinkCheckPolicy(ctx)
	if err != nil {
		return nil, err
	}
	resources, err := r.Store.ListResourcesDueForCheck(ctx, policy, limit, at)
	if err != nil {
		return nil, err
	}
	return toLinkCheckCandidates(resources), nil
}

func (r *CollectionRepository) CountDueLinkChecks(ctx context.Context, at time.Time) (collection.LinkCheckBacklogObservation, error) {
	if r == nil || r.Store == nil {
		return collection.LinkCheckBacklogObservation{}, errors.New("resource library is disabled")
	}
	policy, err := r.Store.GetLinkCheckPolicy(ctx)
	if err != nil {
		return collection.LinkCheckBacklogObservation{}, err
	}
	count, err := r.Store.CountResourcesDueForCheck(ctx, policy, at)
	if err != nil {
		return collection.LinkCheckBacklogObservation{}, err
	}
	return collection.LinkCheckBacklogObservation{DueCount: count, PolicyRevision: policy.Revision()}, nil
}

func toLinkCheckCandidates(resources []storage.Resource) []collection.LinkCheckCandidate {
	result := make([]collection.LinkCheckCandidate, 0, len(resources))
	for _, resource := range resources {
		result = append(result, collection.LinkCheckCandidate{
			ResourceID: resource.ID, URL: resource.URL, Password: resource.Password, Platform: resource.Platform,
			Status: collection.DetectionStatus(resource.CheckStatus), IsNew: resource.LastCheckedAt == nil,
			LastCheckedAt: resource.LastCheckedAt,
		})
	}
	return result
}

func toCollectionKeyword(keyword storage.Keyword) collection.Keyword {
	cooldown := time.Duration(0)
	if keyword.CooldownSeconds != nil {
		cooldown = time.Duration(*keyword.CooldownSeconds) * time.Second
	}
	return collection.Keyword{
		ID: keyword.ID, Value: keyword.Keyword, Normalized: keyword.NormalizedKeyword,
		KeywordType: keyword.KeywordType, SourceType: keyword.SourceType, SourceKey: keyword.SourceKey,
		Enabled: keyword.Enabled, Priority: keyword.Priority, Cooldown: cooldown,
		NextEligibleAt: keyword.NextEligibleAt,
	}
}

func toCollectionBatch(run storage.CollectionRun) collection.Batch {
	batch := collection.Batch{
		ID: run.ID, Trigger: collection.Trigger(run.Trigger), Forced: run.Forced,
		Status: collection.RunStatus(run.Status), CreatedAt: run.CreatedAt,
		StartedAt: run.StartedAt, FinishedAt: run.CompletedAt,
		Items: make([]collection.RunItem, 0, len(run.Items)),
	}
	for _, item := range run.Items {
		keywordID := int64(0)
		if item.KeywordID != nil {
			keywordID = *item.KeywordID
		}
		batch.Items = append(batch.Items, collection.RunItem{
			ID: item.ID, BatchID: item.RunID, Status: collection.RunStatus(item.Status),
			Keyword: collection.Keyword{
				ID: keywordID, Value: item.Keyword, Normalized: item.NormalizedKeyword,
				KeywordType: item.KeywordType, Priority: item.Priority,
				Cooldown: secondsDuration(item.CooldownSeconds),
			},
		})
	}
	return batch
}

func durationSecondsPointer(value time.Duration) *int64 {
	if value <= 0 {
		return nil
	}
	seconds := int64(value / time.Second)
	return &seconds
}

func secondsDuration(value *int64) time.Duration {
	if value == nil {
		return 0
	}
	return time.Duration(*value) * time.Second
}

func NewLiveSearcher(live *service.LiveSearchService) collection.LiveSearcher {
	return collection.LiveSearchFunc(func(ctx context.Context, request collection.SearchRequest) (model.SearchResponse, error) {
		if err := ctx.Err(); err != nil {
			return model.SearchResponse{}, err
		}
		resultType := request.Source.ResultType
		if resultType == "" {
			resultType = "all"
		}
		response, err := live.SearchContext(ctx, service.ContextSearchRequest{
			Keyword: request.Keyword.Value, Channels: request.Source.Channels, Concurrency: request.Source.Concurrency,
			ForceRefresh: request.ForceRefresh, ResultType: resultType, SourceType: request.Source.Type,
			Plugins: request.Source.Plugins, CloudTypes: request.Source.CloudTypes, Ext: cloneMap(request.Source.Extra),
			Identity: service.SearchIdentity{Actor: service.SearchActorCollector},
		})
		if err == nil {
			err = ctx.Err()
		}
		return response, err
	})
}

func ConfiguredSources(channels []string, manager *plugin.PluginManager) collection.SourceProvider {
	return collection.SourceProviderFunc(func(context.Context, collection.Keyword) ([]collection.Source, error) {
		result := make([]collection.Source, 0, len(channels)+16)
		for _, channel := range channels {
			channel = strings.TrimSpace(channel)
			if channel == "" {
				continue
			}
			result = append(result, collection.Source{
				Key: "tg:" + channel, Type: "tg", Channels: []string{channel}, Concurrency: 1, ResultType: "all",
			})
		}
		if manager != nil {
			for _, item := range manager.GetPlugins() {
				name := item.Name()
				result = append(result, collection.Source{
					Key: "plugin:" + name, Type: "plugin", Plugins: []string{name}, Concurrency: 1, ResultType: "all",
				})
			}
		}
		return result, nil
	})
}

type LinkChecker struct {
	Service *service.CheckService
	Pool    *proxypool.Service
}

func (c LinkChecker) Check(ctx context.Context, candidate collection.LinkCheckCandidate) (collection.DetectionStatus, error) {
	if c.Service == nil {
		return collection.DetectionUnknown, errors.New("link check service is unavailable")
	}
	item := model.CheckItem{DiskType: candidate.Platform, URL: candidate.URL, Password: candidate.Password}
	mode := proxypool.ModeBaselineFirst
	if c.Pool != nil {
		mode = c.Pool.RouteMode("platform", candidate.Platform)
	}
	check := func(proxyURL string) (model.CheckResult, error) {
		return c.Service.CheckItemContext(ctx, item, proxyURL)
	}
	if mode == proxypool.ModeProxyOnly || mode == proxypool.ModeStickyProxy {
		result, err := c.checkWithProxyPolicy(ctx, candidate, mode, check, model.CheckResult{}, nil)
		return mapLinkCheckResult(result, err)
	}
	if mode == proxypool.ModeProxyFirst {
		result, err := c.checkWithProxyPolicy(ctx, candidate, mode, check, model.CheckResult{}, nil)
		if err == nil && result.State != "uncertain" {
			return mapLinkCheckResult(result, nil)
		}
		baseline, baselineErr := check("")
		return mapLinkCheckResult(baseline, baselineErr)
	}
	result, err := check("")
	if mode == proxypool.ModeBaselineFirst && (err != nil || result.State == "uncertain") {
		result, err = c.checkWithProxyPolicy(ctx, candidate, mode, check, result, err)
	}
	return mapLinkCheckResult(result, err)
}

func (c LinkChecker) checkWithProxyPolicy(ctx context.Context, candidate collection.LinkCheckCandidate, mode string, check func(string) (model.CheckResult, error), baseline model.CheckResult, baselineErr error) (model.CheckResult, error) {
	if c.Pool == nil || !c.Pool.Enabled() {
		return baseline, baselineErr
	}
	request := proxypool.ProxyRequest{TargetType: "platform", TargetKey: candidate.Platform}
	if mode == proxypool.ModeStickyProxy {
		request.StickyKey = candidate.URL
	}
	tried := make(map[int64]struct{})
	var lastResult model.CheckResult
	var lastErr error
	attempted := false
	for attempt := 0; attempt < c.Pool.MaxAttempts(); attempt++ {
		request.ExcludeIDs = tried
		lease, acquireErr := c.Pool.Acquire(ctx, request)
		if acquireErr != nil {
			if !attempted {
				lastErr = acquireErr
			}
			break
		}
		attempted = true
		tried[lease.ID()] = struct{}{}
		started := time.Now()
		result, err := check(lease.URL())
		decision := classifyProxyAttempt(ctx, result, err)
		decision.outcome.Latency = time.Since(started)
		lease.Release(decision.outcome)
		lastResult, lastErr = result, err
		if decision.outcome.Success {
			return result, nil
		}
		if !decision.retryable {
			break
		}
	}
	if mode == proxypool.ModeProxyOnly || mode == proxypool.ModeStickyProxy {
		if attempted {
			return lastResult, lastErr
		}
		return baseline, lastErr
	}
	return baseline, baselineErr
}

type proxyAttemptDecision struct {
	outcome   proxypool.ProxyOutcome
	retryable bool
}

var retryAfterPattern = regexp.MustCompile(`(?i)retry[- ]after[^0-9]{0,8}([0-9]+)`)

func classifyProxyAttempt(ctx context.Context, result model.CheckResult, err error) proxyAttemptDecision {
	if ctx != nil && ctx.Err() != nil {
		return proxyAttemptDecision{outcome: proxypool.ProxyOutcome{FailureScope: proxypool.FailureScopeNone}}
	}
	if err == nil && result.State != "uncertain" {
		return proxyAttemptDecision{outcome: proxypool.ProxyOutcome{Success: true}}
	}
	if err == nil && result.State == "uncertain" && !linkCheckSummaryIsCircuitFailure(result.Summary) {
		return proxyAttemptDecision{outcome: proxypool.ProxyOutcome{FailureScope: proxypool.FailureScopeNone}}
	}
	message := strings.ToLower(strings.TrimSpace(result.Summary))
	if err != nil {
		message = strings.TrimSpace(message + " " + strings.ToLower(err.Error()))
	}
	retryAfter := parseRetryAfter(message)
	for _, marker := range []string{
		"too many requests", "rate limit", "http状态码: 429", "http status 429", "captcha",
		"http状态码: 5", "http status 5", "响应解析失败", "接口响应格式异常",
	} {
		if strings.Contains(message, marker) {
			return proxyAttemptDecision{outcome: proxypool.ProxyOutcome{FailureScope: proxypool.FailureScopeTarget, RetryAfter: retryAfter}, retryable: true}
		}
	}
	for _, marker := range []string{
		"proxyconnect", "proxy connection", "socks", "dial tcp", "connection refused", "connection reset",
		"no such host", "tls handshake timeout", "i/o timeout", "timeout awaiting response", "unexpected eof", "eof",
	} {
		if strings.Contains(message, marker) {
			return proxyAttemptDecision{outcome: proxypool.ProxyOutcome{FailureScope: proxypool.FailureScopeNode, RetryAfter: retryAfter}, retryable: true}
		}
	}
	if err != nil {
		return proxyAttemptDecision{outcome: proxypool.ProxyOutcome{FailureScope: proxypool.FailureScopeNode, RetryAfter: retryAfter}, retryable: true}
	}
	return proxyAttemptDecision{outcome: proxypool.ProxyOutcome{FailureScope: proxypool.FailureScopeTarget, RetryAfter: retryAfter}, retryable: true}
}

func parseRetryAfter(message string) time.Duration {
	matches := retryAfterPattern.FindStringSubmatch(message)
	if len(matches) != 2 {
		return 0
	}
	seconds, err := strconv.Atoi(matches[1])
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func mapLinkCheckResult(result model.CheckResult, err error) (collection.DetectionStatus, error) {
	if err != nil {
		return collection.DetectionUnknown, err
	}
	switch result.State {
	case "ok":
		return collection.DetectionValid, nil
	case "bad", "invalid":
		return collection.DetectionInvalid, nil
	case "expired":
		return collection.DetectionExpired, nil
	case "cancelled":
		return collection.DetectionCancelled, nil
	case "violation":
		return collection.DetectionViolation, nil
	case "locked":
		return collection.DetectionLocked, nil
	case "unsupported":
		return collection.DetectionUnsupported, nil
	case "uncertain":
		if !result.CacheHit && linkCheckSummaryIsCircuitFailure(result.Summary) {
			return collection.DetectionUnknown, fmt.Errorf("upstream link check failed: %s", result.Summary)
		}
		return collection.DetectionUnknown, nil
	default:
		return collection.DetectionUnknown, fmt.Errorf("unknown link-check state %q", result.State)
	}
}

func linkCheckSummaryIsCircuitFailure(summary string) bool {
	summary = strings.ToLower(strings.TrimSpace(summary))
	if summary == "" {
		return false
	}
	for _, marker := range []string{
		"请求失败", "详情请求失败", "响应解析失败", "接口响应格式异常", "无法确认链接状态",
		"captcha", "too many requests", "rate limit", "http状态码: 429", "http状态码: 5",
	} {
		if strings.Contains(summary, marker) {
			return true
		}
	}
	return false
}

func cloneMap(input map[string]interface{}) map[string]interface{} {
	if input == nil {
		return make(map[string]interface{})
	}
	result := make(map[string]interface{}, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
