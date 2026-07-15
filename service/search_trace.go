package service

import (
	"context"
	"sync/atomic"
)

// SearchCacheStatus is the request-level cache path taken by a search.
type SearchCacheStatus string

const (
	SearchCacheNotApplicable SearchCacheStatus = "not_applicable"
	SearchCacheHit           SearchCacheStatus = "hit"
	SearchCacheMiss          SearchCacheStatus = "miss"
	SearchCacheRefresh       SearchCacheStatus = "refresh"
	SearchCacheBypass        SearchCacheStatus = "bypass"
)

const (
	searchTraceHit uint32 = 1 << iota
	searchTraceMiss
	searchTraceRefresh
	searchTraceBypass
)

// SearchTrace collects cache decisions made by concurrent search branches.
// Refresh and misses take precedence; a bypass takes precedence over a hit so
// mixed cached/live requests are not reported as complete cache hits.
type SearchTrace struct {
	flags atomic.Uint32
}

func NewSearchTrace() *SearchTrace { return &SearchTrace{} }

func (t *SearchTrace) Mark(status SearchCacheStatus) {
	if t == nil {
		return
	}
	var flag uint32
	switch status {
	case SearchCacheHit:
		flag = searchTraceHit
	case SearchCacheMiss:
		flag = searchTraceMiss
	case SearchCacheRefresh:
		flag = searchTraceRefresh
	case SearchCacheBypass:
		flag = searchTraceBypass
	default:
		return
	}
	for {
		current := t.flags.Load()
		if current&flag != 0 || t.flags.CompareAndSwap(current, current|flag) {
			return
		}
	}
}

func (t *SearchTrace) Status() SearchCacheStatus {
	if t == nil {
		return SearchCacheNotApplicable
	}
	flags := t.flags.Load()
	switch {
	case flags&searchTraceRefresh != 0:
		return SearchCacheRefresh
	case flags&searchTraceMiss != 0:
		return SearchCacheMiss
	case flags&searchTraceBypass != 0:
		return SearchCacheBypass
	case flags&searchTraceHit != 0:
		return SearchCacheHit
	default:
		return SearchCacheNotApplicable
	}
}

type searchTraceContextKey struct{}

func ContextWithSearchTrace(ctx context.Context, trace *SearchTrace) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if trace == nil {
		trace = NewSearchTrace()
	}
	return context.WithValue(ctx, searchTraceContextKey{}, trace)
}

func SearchTraceFromContext(ctx context.Context) *SearchTrace {
	if ctx == nil {
		return nil
	}
	trace, _ := ctx.Value(searchTraceContextKey{}).(*SearchTrace)
	return trace
}

func MarkSearchCacheStatus(ctx context.Context, status SearchCacheStatus) {
	if trace := SearchTraceFromContext(ctx); trace != nil {
		trace.Mark(status)
	}
}
