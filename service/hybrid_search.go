package service

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"pansou/model"
	"pansou/plugin"
	"pansou/storage"
)

// ResourceSearchRepository is the persistence surface used by hybrid search.
// Keeping it narrow makes database outage behavior straightforward to test.
type ResourceSearchRepository interface {
	SearchResources(context.Context, storage.ResourceFilter) (storage.ResourcePage, error)
	UpsertSearchResponse(context.Context, string, string, string, model.SearchResponse) (storage.UpsertSummary, error)
}

// ExternalResultRecorder records external searches as collection runs. When it
// is absent, HybridSearchService writes search results directly to storage.
type ExternalResultRecorder func(context.Context, string, model.SearchResponse) error

type HybridSearchService struct {
	live         SearchProvider
	store        ResourceSearchRepository
	refreshAfter time.Duration
	recorder     ExternalResultRecorder

	refreshMu sync.Mutex
	refreshes map[string]struct{}
}

func NewHybridSearchService(live SearchProvider, store ResourceSearchRepository, refreshAfter time.Duration) *HybridSearchService {
	if refreshAfter <= 0 {
		refreshAfter = time.Hour
	}
	return &HybridSearchService{
		live:         live,
		store:        store,
		refreshAfter: refreshAfter,
		refreshes:    make(map[string]struct{}),
	}
}

func (s *HybridSearchService) SetExternalResultRecorder(recorder ExternalResultRecorder) {
	s.recorder = recorder
}

func (s *HybridSearchService) GetPluginManager() *plugin.PluginManager {
	if s == nil || s.live == nil {
		return nil
	}
	return s.live.GetPluginManager()
}

func (s *HybridSearchService) UsesManagedSources() bool {
	return s != nil && UsesManagedSources(s.live)
}

func (s *HybridSearchService) Search(keyword string, channels []string, concurrency int, forceRefresh bool, resultType string, sourceType string, plugins []string, cloudTypes []string, ext map[string]interface{}) (model.SearchResponse, error) {
	return s.SearchContext(context.Background(), ContextSearchRequest{
		Keyword: keyword, Channels: channels, Concurrency: concurrency, ForceRefresh: forceRefresh,
		ResultType: resultType, SourceType: sourceType, Plugins: plugins, CloudTypes: cloudTypes,
		Ext: ext, Identity: SearchIdentity{Actor: SearchActorLegacy},
	})
}

func (s *HybridSearchService) SearchContext(ctx context.Context, request ContextSearchRequest) (model.SearchResponse, error) {
	if s == nil || s.live == nil {
		return model.SearchResponse{}, fmt.Errorf("live search service is unavailable")
	}
	resolved, err := ResolveSearchRequest(ctx, s.live, request)
	if err != nil {
		return model.SearchResponse{}, err
	}
	request = resolved
	keyword, channels, forceRefresh := request.Keyword, request.Channels, request.ForceRefresh
	resultType, sourceType, plugins, cloudTypes, ext := request.ResultType, request.SourceType, request.Plugins, request.CloudTypes, request.Ext
	if s.store == nil {
		return SearchWithContext(ctx, s.live, request)
	}

	if forceRefresh || request.requiresLiveTG {
		request.ForceRefresh = forceRefresh
		response, err := SearchWithContext(ctx, s.live, request)
		if err != nil {
			return response, err
		}
		s.persist(keyword, response)
		return response, nil
	}

	dbCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	databaseResponse, latestSeen, err := s.searchDatabase(dbCtx, keyword, resultType, sourceType, channels, plugins, cloudTypes)
	cancel()
	if err == nil && databaseResponse.Total > 0 {
		if latestSeen.IsZero() || time.Since(latestSeen) > s.refreshAfter {
			request.ResultType = "all"
			request.Ext = cloneExt(ext)
			s.refreshInBackground(request)
		}
		return databaseResponse, nil
	}
	if err != nil {
		log.Printf("resource library search unavailable, falling back to live search: %v", err)
	}

	request.ForceRefresh = false
	request.ResultType = "all"
	response, liveErr := SearchWithContext(ctx, s.live, request)
	if liveErr != nil {
		return response, liveErr
	}
	s.persist(keyword, response)
	return formatSearchResponse(response, resultType), nil
}

func formatSearchResponse(response model.SearchResponse, resultType string) model.SearchResponse {
	if response.MergedByType == nil {
		response.MergedByType = mergeResultsByType(response.Results, "", nil)
	}
	if response.Total == 0 {
		if resultType == "merged_by_type" || resultType == "merge" || resultType == "" {
			for _, links := range response.MergedByType {
				response.Total += len(links)
			}
		} else {
			response.Total = len(response.Results)
		}
	}
	return filterResponseByType(response, resultType)
}

func (s *HybridSearchService) searchDatabase(ctx context.Context, keyword, resultType, sourceType string, channels, plugins, cloudTypes []string) (model.SearchResponse, time.Time, error) {
	base := storage.ResourceFilter{
		Keyword:   keyword,
		Platforms: cloudTypes,
		Page:      1,
		PageSize:  200,
	}

	filters := make([]storage.ResourceFilter, 0, 2)
	switch strings.ToLower(strings.TrimSpace(sourceType)) {
	case "tg":
		if len(channels) > 0 {
			base.SourceTypes = []string{"tg"}
			base.SourceKeys = channels
			filters = append(filters, base)
		}
	case "plugin":
		if len(plugins) > 0 {
			base.SourceTypes = []string{"plugin"}
			base.SourceKeys = plugins
			filters = append(filters, base)
		}
	default:
		if len(channels) > 0 {
			tgFilter := base
			tgFilter.SourceTypes = []string{"tg"}
			tgFilter.SourceKeys = channels
			filters = append(filters, tgFilter)
		}
		if len(plugins) > 0 {
			pluginFilter := base
			pluginFilter.SourceTypes = []string{"plugin"}
			pluginFilter.SourceKeys = plugins
			filters = append(filters, pluginFilter)
		}
	}

	allResults := make([]model.SearchResult, 0)
	latestSeen := time.Time{}
	for _, filter := range filters {
		page, err := s.store.SearchResources(ctx, filter)
		if err != nil {
			return model.SearchResponse{}, time.Time{}, err
		}
		for _, resource := range page.Items {
			if resource.LastSeenAt.After(latestSeen) {
				latestSeen = resource.LastSeenAt
			}
		}
		allResults = mergeDatabaseSearchResults(allResults, page.ToSearchResponse().Results)
	}

	response := model.SearchResponse{
		Results:      allResults,
		MergedByType: mergeResultsByType(allResults, keyword, cloudTypes),
	}
	if resultType == "merged_by_type" || resultType == "merge" || resultType == "" {
		for _, links := range response.MergedByType {
			response.Total += len(links)
		}
	} else {
		response.Total = len(response.Results)
	}
	return filterResponseByType(response, resultType), latestSeen, nil
}

func mergeDatabaseSearchResults(existing, incoming []model.SearchResult) []model.SearchResult {
	seen := make(map[string]struct{}, len(existing)+len(incoming))
	for _, result := range existing {
		key := result.UniqueID
		if len(result.Links) > 0 && result.Links[0].URL != "" {
			key = result.Links[0].URL
		}
		seen[key] = struct{}{}
	}
	for _, result := range incoming {
		key := result.UniqueID
		if len(result.Links) > 0 && result.Links[0].URL != "" {
			key = result.Links[0].URL
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		existing = append(existing, result)
	}
	return existing
}

func (s *HybridSearchService) refreshInBackground(request ContextSearchRequest) {
	keyword, channels, sourceType := request.Keyword, request.Channels, request.SourceType
	plugins, cloudTypes := request.Plugins, request.CloudTypes
	key := strings.Join([]string{
		strings.ToLower(strings.TrimSpace(keyword)),
		strings.ToLower(sourceType),
		strings.Join(channels, ","),
		strings.Join(plugins, ","),
		strings.Join(cloudTypes, ","),
		fmt.Sprintf("%s:%d", request.Identity.Actor, request.Identity.UserID),
	}, "|")
	s.refreshMu.Lock()
	if _, exists := s.refreshes[key]; exists {
		s.refreshMu.Unlock()
		return
	}
	s.refreshes[key] = struct{}{}
	s.refreshMu.Unlock()

	go func() {
		defer func() {
			s.refreshMu.Lock()
			delete(s.refreshes, key)
			s.refreshMu.Unlock()
		}()
		request.ForceRefresh = true
		response, err := SearchWithContext(context.Background(), s.live, request)
		if err != nil {
			log.Printf("background live refresh failed for %q: %v", keyword, err)
			return
		}
		s.persist(keyword, response)
	}()
}

func (s *HybridSearchService) persist(keyword string, response model.SearchResponse) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if s.recorder != nil {
		if err := s.recorder(ctx, keyword, response); err == nil {
			return
		} else {
			log.Printf("record external collection run failed for %q: %v", keyword, err)
		}
	}
	if _, err := s.store.UpsertSearchResponse(ctx, keyword, storage.DefaultKeywordType, "external", response); err != nil {
		log.Printf("persist live search results failed for %q: %v", keyword, err)
	}
}

func cloneExt(ext map[string]interface{}) map[string]interface{} {
	if ext == nil {
		return make(map[string]interface{})
	}
	cloned := make(map[string]interface{}, len(ext))
	for key, value := range ext {
		cloned[key] = value
	}
	return cloned
}
