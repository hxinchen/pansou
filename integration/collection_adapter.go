package integration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"pansou/collection"
	"pansou/model"
	"pansou/plugin"
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
}

func (c LinkChecker) Check(ctx context.Context, candidate collection.LinkCheckCandidate) (collection.DetectionStatus, error) {
	if c.Service == nil {
		return collection.DetectionUnknown, errors.New("link check service is unavailable")
	}
	type outcome struct {
		response model.CheckResponse
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		response, err := c.Service.CheckWithProxy([]model.CheckItem{{
			DiskType: candidate.Platform, URL: candidate.URL, Password: candidate.Password,
		}}, "")
		done <- outcome{response: response, err: err}
	}()
	select {
	case <-ctx.Done():
		return collection.DetectionUnknown, ctx.Err()
	case result := <-done:
		if result.err != nil {
			return collection.DetectionUnknown, result.err
		}
		if len(result.response.Results) != 1 {
			return collection.DetectionUnknown, fmt.Errorf("link checker returned %d results", len(result.response.Results))
		}
		switch result.response.Results[0].State {
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
			return collection.DetectionUnknown, nil
		default:
			return collection.DetectionUnknown, fmt.Errorf("unknown link-check state %q", result.response.Results[0].State)
		}
	}
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
