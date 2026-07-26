package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"pansou/model"
)

type searchFlightValue struct {
	response    model.SearchResponse
	cacheStatus SearchCacheStatus
}

const searchFlightReplayTTL = 5 * time.Second

type searchFlightReplayKey struct {
	group *singleflight.Group
	key   string
}

type searchFlightReplayValue struct {
	value     searchFlightValue
	expiresAt time.Time
}

var searchFlightReplays sync.Map

type searchExecutionDeadlineContextKey struct{}

func executeSearchFlight(
	ctx context.Context,
	group *singleflight.Group,
	prefix string,
	request ContextSearchRequest,
	search func(context.Context) (model.SearchResponse, error),
) (model.SearchResponse, error) {
	if group == nil {
		return search(ctx)
	}
	key, err := buildSearchFlightKey(prefix, request)
	if err != nil {
		return search(ctx)
	}
	replayKey := searchFlightReplayKey{group: group, key: key}
	if cached, ok := searchFlightReplays.Load(replayKey); ok {
		replay := cached.(*searchFlightReplayValue)
		if time.Now().Before(replay.expiresAt) {
			MarkSearchCacheStatus(ctx, replay.value.cacheStatus)
			return replay.value.response, nil
		}
		searchFlightReplays.CompareAndDelete(replayKey, replay)
	}

	result := group.DoChan(key, func() (interface{}, error) {
		trace := NewSearchTrace()
		sharedBase := context.WithoutCancel(ctx)
		cancelExecution := func() {}
		if request.ExecutionBudget > 0 {
			if _, inherited := ctx.Value(searchExecutionDeadlineContextKey{}).(struct{}); inherited {
				// Preserve an outer hybrid-search budget instead of starting a
				// second full budget for its nested live search.
				sharedBase = ctx
			} else {
				sharedBase, cancelExecution = context.WithTimeout(sharedBase, request.ExecutionBudget)
				sharedBase = context.WithValue(sharedBase, searchExecutionDeadlineContextKey{}, struct{}{})
			}
		}
		defer cancelExecution()
		started := time.Now()
		sharedCtx := ContextWithSearchTrace(sharedBase, trace)
		response, searchErr := search(sharedCtx)
		if request.ExecutionBudget > 0 && errors.Is(sharedCtx.Err(), context.DeadlineExceeded) {
			response = markSearchDeadline(response, time.Since(started))
			if errors.Is(searchErr, context.DeadlineExceeded) || errors.Is(searchErr, context.Canceled) {
				searchErr = nil
			}
		}
		value := searchFlightValue{response: response, cacheStatus: trace.Status()}
		if searchErr == nil && response.StopReason != model.SearchStopReasonDeadline {
			replay := &searchFlightReplayValue{value: value, expiresAt: time.Now().Add(searchFlightReplayTTL)}
			searchFlightReplays.Store(replayKey, replay)
			time.AfterFunc(searchFlightReplayTTL, func() {
				searchFlightReplays.CompareAndDelete(replayKey, replay)
			})
		}
		return value, searchErr
	})

	select {
	case <-ctx.Done():
		return model.SearchResponse{}, ctx.Err()
	case shared := <-result:
		value, ok := shared.Val.(searchFlightValue)
		if !ok {
			return model.SearchResponse{}, fmt.Errorf("invalid shared search result")
		}
		MarkSearchCacheStatus(ctx, value.cacheStatus)
		return value.response, shared.Err
	}
}

func buildSearchFlightKey(prefix string, request ContextSearchRequest) (string, error) {
	channels := sortedSearchValues(request.Channels)
	plugins := sortedSearchValues(request.Plugins)
	cloudTypes := sortedSearchValues(request.CloudTypes)
	payload := struct {
		Keyword         string                 `json:"keyword"`
		Channels        []string               `json:"channels"`
		Concurrency     int                    `json:"concurrency"`
		ForceRefresh    bool                   `json:"force_refresh"`
		ResultType      string                 `json:"result_type"`
		SourceType      string                 `json:"source_type"`
		Plugins         []string               `json:"plugins"`
		CloudTypes      []string               `json:"cloud_types"`
		Ext             map[string]interface{} `json:"ext"`
		Actor           string                 `json:"actor"`
		UserID          int64                  `json:"user_id"`
		Role            string                 `json:"role"`
		AuthType        string                 `json:"auth_type"`
		APIKeyID        int64                  `json:"api_key_id"`
		ExecutionBudget int64                  `json:"execution_budget_ns"`
	}{
		Keyword: request.Keyword, Channels: channels, Concurrency: request.Concurrency,
		ForceRefresh: request.ForceRefresh, ResultType: request.ResultType, SourceType: request.SourceType,
		Plugins: plugins, CloudTypes: cloudTypes, Ext: request.Ext,
		Actor: request.Identity.Actor, UserID: request.Identity.UserID, Role: request.Identity.Role,
		AuthType: request.Identity.AuthType, APIKeyID: request.Identity.APIKeyID,
		ExecutionBudget: int64(request.ExecutionBudget),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return prefix + ":" + hex.EncodeToString(digest[:]), nil
}

func markSearchDeadline(response model.SearchResponse, elapsed time.Duration) model.SearchResponse {
	response.Completion = model.SearchCompletionPartial
	response.StopReason = model.SearchStopReasonDeadline
	response.ElapsedMS = elapsed.Milliseconds()
	if response.ElapsedMS <= 0 {
		response.ElapsedMS = 1
	}
	if len(response.PartialSources) > 0 {
		seen := make(map[string]struct{}, len(response.PartialSources))
		unique := make([]string, 0, len(response.PartialSources))
		for _, source := range response.PartialSources {
			source = strings.TrimSpace(source)
			if source == "" {
				continue
			}
			if _, exists := seen[source]; exists {
				continue
			}
			seen[source] = struct{}{}
			unique = append(unique, source)
		}
		sort.Strings(unique)
		response.PartialSources = unique
		if len(unique) > 0 {
			if response.SourceStatuses == nil {
				response.SourceStatuses = make(map[string]model.SourceStatus, len(unique))
			}
			for _, source := range unique {
				if _, exists := response.SourceStatuses[source]; !exists {
					response.SourceStatuses[source] = model.SourceStatus{
						Completion: model.SearchCompletionPartial,
						Message:    model.SearchStopReasonDeadline,
					}
				}
			}
		}
	}
	return response
}

func sortedSearchValues(values []string) []string {
	if values == nil {
		return nil
	}
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
