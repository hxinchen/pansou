package service

import (
	"context"

	"pansou/model"
)

const (
	SearchActorLegacy    = "legacy"
	SearchActorUser      = "user"
	SearchActorAdmin     = "admin"
	SearchActorCollector = "collector"
)

type SearchIdentity struct {
	Actor     string
	UserID    int64
	Role      string
	AuthType  string
	APIKeyID  int64
	RequestID string
}

type ContextSearchRequest struct {
	Keyword        string
	Channels       []string
	Concurrency    int
	ForceRefresh   bool
	ResultType     string
	SourceType     string
	Plugins        []string
	CloudTypes     []string
	Ext            map[string]interface{}
	Identity       SearchIdentity
	requiresLiveTG bool
}

type ContextSearchProvider interface {
	SearchContext(context.Context, ContextSearchRequest) (model.SearchResponse, error)
}

// ContextSearchRequestResolver resolves omitted managed-source defaults and
// normalizes explicitly requested sources before database or live searching.
type ContextSearchRequestResolver interface {
	ResolveSearchRequest(context.Context, ContextSearchRequest) (ContextSearchRequest, error)
}

// ManagedSourceProvider reports whether a search provider resolves its default
// channels and plugins from the hot-reloadable source snapshot. API handlers
// use this optional capability to avoid replacing an omitted channel filter
// with the process-startup CHANNELS value.
type ManagedSourceProvider interface {
	UsesManagedSources() bool
}

func UsesManagedSources(provider SearchProvider) bool {
	managed, ok := provider.(ManagedSourceProvider)
	return ok && managed.UsesManagedSources()
}

func SearchWithContext(ctx context.Context, provider SearchProvider, request ContextSearchRequest) (model.SearchResponse, error) {
	if contextual, ok := provider.(ContextSearchProvider); ok {
		return contextual.SearchContext(ctx, request)
	}
	return provider.Search(
		request.Keyword, request.Channels, request.Concurrency, request.ForceRefresh,
		request.ResultType, request.SourceType, request.Plugins, request.CloudTypes, request.Ext,
	)
}

func ResolveSearchRequest(ctx context.Context, provider SearchProvider, request ContextSearchRequest) (ContextSearchRequest, error) {
	resolver, ok := provider.(ContextSearchRequestResolver)
	if !ok {
		return request, nil
	}
	return resolver.ResolveSearchRequest(ctx, request)
}
