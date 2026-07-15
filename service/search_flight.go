package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"golang.org/x/sync/singleflight"

	"pansou/model"
)

type searchFlightValue struct {
	response    model.SearchResponse
	cacheStatus SearchCacheStatus
}

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

	result := group.DoChan(key, func() (interface{}, error) {
		trace := NewSearchTrace()
		sharedCtx := ContextWithSearchTrace(context.WithoutCancel(ctx), trace)
		response, searchErr := search(sharedCtx)
		return searchFlightValue{response: response, cacheStatus: trace.Status()}, searchErr
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
		Keyword      string                 `json:"keyword"`
		Channels     []string               `json:"channels"`
		Concurrency  int                    `json:"concurrency"`
		ForceRefresh bool                   `json:"force_refresh"`
		ResultType   string                 `json:"result_type"`
		SourceType   string                 `json:"source_type"`
		Plugins      []string               `json:"plugins"`
		CloudTypes   []string               `json:"cloud_types"`
		Ext          map[string]interface{} `json:"ext"`
		Actor        string                 `json:"actor"`
		UserID       int64                  `json:"user_id"`
		Role         string                 `json:"role"`
		AuthType     string                 `json:"auth_type"`
		APIKeyID     int64                  `json:"api_key_id"`
	}{
		Keyword: request.Keyword, Channels: channels, Concurrency: request.Concurrency,
		ForceRefresh: request.ForceRefresh, ResultType: request.ResultType, SourceType: request.SourceType,
		Plugins: plugins, CloudTypes: cloudTypes, Ext: request.Ext,
		Actor: request.Identity.Actor, UserID: request.Identity.UserID, Role: request.Identity.Role,
		AuthType: request.Identity.AuthType, APIKeyID: request.Identity.APIKeyID,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return prefix + ":" + hex.EncodeToString(digest[:]), nil
}

func sortedSearchValues(values []string) []string {
	if values == nil {
		return nil
	}
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
