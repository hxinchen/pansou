package collection

import (
	"context"
	"time"

	"pansou/model"
)

// RunRepository owns persistence and transaction boundaries. CreateBatch must
// persist the supplied keyword snapshots and return their generated item IDs.
type RunRepository interface {
	SelectKeywords(context.Context, KeywordSelection) ([]Keyword, error)
	// FindKeyword returns (nil, nil) when the normalized keyword is not managed.
	FindKeyword(context.Context, string) (*Keyword, error)
	CreateBatch(context.Context, NewBatch) (Batch, error)
	MarkItemRunning(context.Context, int64, int64, time.Time) error
	Ingest(context.Context, IngestRequest) (IngestResult, error)
	CompleteItem(context.Context, int64, int64, ItemCompletion) error
	CompleteBatch(context.Context, int64, BatchCompletion) error
	// RecoverRunning changes abandoned running items back to pending.
	RecoverRunning(context.Context, time.Time) (int, error)
	// ClaimPending atomically claims the oldest pending item across existing
	// batches. It returns (nil, nil) when there is no resumable work. CompleteItem
	// must keep the containing batch aggregate status in sync.
	ClaimPending(context.Context) (*ClaimedRunItem, error)
}

type LiveSearcher interface {
	Search(context.Context, SearchRequest) (model.SearchResponse, error)
}

type LiveSearchFunc func(context.Context, SearchRequest) (model.SearchResponse, error)

func (f LiveSearchFunc) Search(ctx context.Context, request SearchRequest) (model.SearchResponse, error) {
	return f(ctx, request)
}

type SourceProvider interface {
	Sources(context.Context, Keyword) ([]Source, error)
}

type SourceProviderFunc func(context.Context, Keyword) ([]Source, error)

func (f SourceProviderFunc) Sources(ctx context.Context, keyword Keyword) ([]Source, error) {
	return f(ctx, keyword)
}

type SourceLease interface {
	Sources() []Source
	Searcher() LiveSearcher
	Release()
}

type LeasedSourceProvider interface {
	AcquireSources(context.Context, Keyword) (SourceLease, error)
}

type StaticSourceLease struct {
	Items      []Source
	LiveSearch LiveSearcher
}

func (l *StaticSourceLease) Sources() []Source      { return append([]Source(nil), l.Items...) }
func (l *StaticSourceLease) Searcher() LiveSearcher { return l.LiveSearch }
func (l *StaticSourceLease) Release()               {}

type StaticSources []Source

func (s StaticSources) Sources(context.Context, Keyword) ([]Source, error) {
	result := make([]Source, len(s))
	copy(result, s)
	return result, nil
}

type CheckEnqueuer interface {
	Enqueue(context.Context, LinkCheckCandidate) error
}

type LinkChecker interface {
	Check(context.Context, LinkCheckCandidate) (DetectionStatus, error)
}

type LinkCheckerFunc func(context.Context, LinkCheckCandidate) (DetectionStatus, error)

func (f LinkCheckerFunc) Check(ctx context.Context, candidate LinkCheckCandidate) (DetectionStatus, error) {
	return f(ctx, candidate)
}

type LinkCheckRepository interface {
	CompleteLinkCheck(context.Context, LinkCheckResult) error
	DueLinkChecks(context.Context, int, time.Time) ([]LinkCheckCandidate, error)
}

// LinkCheckBatchRepository atomically persists a small group of completed
// checks. The queue falls back to CompleteLinkCheck when it is unavailable.
type LinkCheckBatchRepository interface {
	CompleteLinkChecks(context.Context, []LinkCheckResult) error
}

// LinkCheckBacklogCounter is optional. Repositories that implement it let the
// queue sample the exact due backlog even when no administrator is watching.
type LinkCheckBacklogObservation struct {
	DueCount       int64
	PolicyRevision string
}

type LinkCheckBacklogCounter interface {
	CountDueLinkChecks(context.Context, time.Time) (LinkCheckBacklogObservation, error)
}
