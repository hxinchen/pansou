package collection

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"

	"pansou/model"
)

const (
	DefaultScheduleInterval = time.Minute
	DefaultCooldown         = 7 * 24 * time.Hour
)

var (
	ErrNotStarted        = errors.New("collection runner is not started")
	ErrBatchRunning      = errors.New("a collection batch is already running")
	ErrNoEligibleKeyword = errors.New("no eligible collection keyword")
	ErrNoSources         = errors.New("no live search sources configured")
	ErrEmptyKeyword      = errors.New("keyword is empty")
	ErrQueueFull         = errors.New("link check queue is full")
	ErrQueueNotStarted   = errors.New("link check queue is not started")
	ErrQueueStopping     = errors.New("link check queue is stopping")
)

type Trigger string

const (
	TriggerScheduled Trigger = "scheduled"
	TriggerManual    Trigger = "manual"
	TriggerExternal  Trigger = "external"
)

type RunStatus string

const (
	StatusPending      RunStatus = "pending"
	StatusRunning      RunStatus = "running"
	StatusSuccess      RunStatus = "success"
	StatusSuccessEmpty RunStatus = "success_empty"
	StatusFailed       RunStatus = "failed"
)

type DetectionStatus string

const (
	DetectionPending     DetectionStatus = "pending"
	DetectionValid       DetectionStatus = "valid"
	DetectionInvalid     DetectionStatus = "invalid"
	DetectionExpired     DetectionStatus = "expired"
	DetectionCancelled   DetectionStatus = "cancelled"
	DetectionViolation   DetectionStatus = "violation"
	DetectionLocked      DetectionStatus = "locked"
	DetectionUnknown     DetectionStatus = "unknown"
	DetectionUnsupported DetectionStatus = "unsupported"
)

// Keyword is the immutable snapshot used for one collection run.
type Keyword struct {
	ID             int64         `json:"id"`
	Value          string        `json:"keyword"`
	Normalized     string        `json:"normalized_keyword"`
	KeywordType    string        `json:"keyword_type"`
	SourceType     string        `json:"source_type"`
	SourceKey      string        `json:"source_key,omitempty"`
	Enabled        bool          `json:"enabled"`
	Priority       int           `json:"priority"`
	Cooldown       time.Duration `json:"cooldown"`
	NextEligibleAt *time.Time    `json:"next_eligible_at,omitempty"`
}

type KeywordSelection struct {
	IDs         []int64
	EnabledOnly bool
}

type NewBatch struct {
	Trigger   Trigger
	Forced    bool
	Keywords  []Keyword
	CreatedAt time.Time
}

type Batch struct {
	ID         int64      `json:"id"`
	Trigger    Trigger    `json:"trigger"`
	Forced     bool       `json:"forced"`
	Status     RunStatus  `json:"status"`
	Items      []RunItem  `json:"items"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type RunItem struct {
	ID      int64     `json:"id"`
	BatchID int64     `json:"batch_id"`
	Keyword Keyword   `json:"keyword"`
	Status  RunStatus `json:"status"`
}

// ClaimedRunItem is an existing pending item atomically transitioned to
// running by the repository. The runner must not call MarkItemRunning again.
type ClaimedRunItem struct {
	Batch     Batch
	Item      RunItem
	StartedAt time.Time
}

// Source describes one independently retried search channel or plugin.
// Adapters can map these fields directly to service.SearchService.Search.
type Source struct {
	Key         string                 `json:"key"`
	Type        string                 `json:"type"`
	Channels    []string               `json:"channels,omitempty"`
	Plugins     []string               `json:"plugins,omitempty"`
	CloudTypes  []string               `json:"cloud_types,omitempty"`
	Concurrency int                    `json:"concurrency,omitempty"`
	ResultType  string                 `json:"result_type,omitempty"`
	Extra       map[string]interface{} `json:"extra,omitempty"`
}

type SearchRequest struct {
	Keyword      Keyword
	Source       Source
	ForceRefresh bool
}

type CurrentSearchFunc func(keyword string, channels []string, concurrency int, forceRefresh bool, resultType string, sourceType string, plugins []string, cloudTypes []string, extra map[string]interface{}) (model.SearchResponse, error)

// AdaptCurrentSearch bridges service.SearchService.Search without importing
// the service package. The current method is not context-aware, so callers
// should still configure its own HTTP/plugin timeouts.
func AdaptCurrentSearch(search CurrentSearchFunc) LiveSearcher {
	return LiveSearchFunc(func(_ context.Context, request SearchRequest) (model.SearchResponse, error) {
		if search == nil {
			return model.SearchResponse{}, errors.New("live search function is nil")
		}
		sourceType := request.Source.Type
		if sourceType == "" {
			sourceType = "all"
		}
		return search(
			request.Keyword.Value,
			request.Source.Channels,
			request.Source.Concurrency,
			request.ForceRefresh,
			request.Source.ResultType,
			sourceType,
			request.Source.Plugins,
			request.Source.CloudTypes,
			request.Source.Extra,
		)
	})
}

type IngestRequest struct {
	BatchID      int64
	ItemID       int64
	Trigger      Trigger
	Keyword      Keyword
	Source       Source
	Response     model.SearchResponse
	DiscoveredAt time.Time
}

type IngestResult struct {
	New             int
	Duplicate       int
	CheckCandidates []LinkCheckCandidate
}

type SourceRunSummary struct {
	Key            string `json:"key"`
	Type           string `json:"type"`
	Status         string `json:"status"`
	Attempts       int    `json:"attempts"`
	ResultCount    int    `json:"result_count"`
	NewCount       int    `json:"new_count"`
	DuplicateCount int    `json:"duplicate_count"`
	DurationMS     int64  `json:"duration_ms"`
	Error          string `json:"error,omitempty"`
}

type SourceSummary map[string]SourceRunSummary

func (summary SourceSummary) JSONMap() map[string]interface{} {
	result := make(map[string]interface{}, len(summary))
	for key, entry := range summary {
		result[key] = entry
	}
	return result
}

type ItemCompletion struct {
	Status         RunStatus
	NewCount       int
	DuplicateCount int
	SourceSummary  SourceSummary
	Error          string
	StartedAt      time.Time
	FinishedAt     time.Time
	NextEligibleAt *time.Time
}

type BatchCompletion struct {
	Status     RunStatus
	FinishedAt time.Time
}

type LinkCheckCandidate struct {
	ResourceID    int64
	URL           string
	Password      string
	Platform      string
	Status        DetectionStatus
	IsNew         bool
	LastCheckedAt *time.Time
}

type LinkCheckResult struct {
	ResourceID int64
	Status     DetectionStatus
	CheckedAt  time.Time
	Error      string
}

// NormalizeKeyword gives storage and external collection the same identity.
func NormalizeKeyword(value string) string {
	fields := strings.FieldsFunc(strings.TrimSpace(value), unicode.IsSpace)
	return strings.ToLower(strings.Join(fields, " "))
}

func IsEligible(keyword Keyword, now time.Time) bool {
	return keyword.NextEligibleAt == nil || !now.Before(*keyword.NextEligibleAt)
}

func EffectiveCooldown(keyword Keyword, fallback time.Duration) time.Duration {
	if keyword.Cooldown > 0 {
		return keyword.Cooldown
	}
	if fallback > 0 {
		return fallback
	}
	return DefaultCooldown
}

func CalculateNextEligibleAt(keyword Keyword, completedAt time.Time, fallback time.Duration) time.Time {
	return completedAt.Add(EffectiveCooldown(keyword, fallback))
}

// DetermineRunStatus implements the V1 outcome rule for a keyword or batch.
func DetermineRunStatus(anyData, anyCompleted bool) RunStatus {
	if anyData {
		return StatusSuccess
	}
	if anyCompleted {
		return StatusSuccessEmpty
	}
	return StatusFailed
}

func ResultCount(response model.SearchResponse) int {
	count := len(response.Results)
	merged := 0
	for _, links := range response.MergedByType {
		merged += len(links)
	}
	if merged > count {
		count = merged
	}
	if response.Total > count {
		count = response.Total
	}
	if count < 0 {
		return 0
	}
	return count
}

func ShouldQueueLinkCheck(candidate LinkCheckCandidate) bool {
	if candidate.ResourceID == 0 || strings.TrimSpace(candidate.URL) == "" {
		return false
	}
	return candidate.IsNew || candidate.Status == DetectionPending
}

func prepareKeywords(keywords []Keyword, now time.Time, force bool) []Keyword {
	seenIDs := make(map[int64]struct{}, len(keywords))
	seenValues := make(map[string]struct{}, len(keywords))
	result := make([]Keyword, 0, len(keywords))
	for _, keyword := range keywords {
		keyword.Value = strings.TrimSpace(keyword.Value)
		if keyword.Normalized == "" {
			keyword.Normalized = NormalizeKeyword(keyword.Value)
		}
		if keyword.Normalized == "" || (!force && !IsEligible(keyword, now)) {
			continue
		}
		if keyword.ID != 0 {
			if _, exists := seenIDs[keyword.ID]; exists {
				continue
			}
			seenIDs[keyword.ID] = struct{}{}
		} else {
			if _, exists := seenValues[keyword.Normalized]; exists {
				continue
			}
			seenValues[keyword.Normalized] = struct{}{}
		}
		result = append(result, keyword)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Priority == result[j].Priority {
			return result[i].ID < result[j].ID
		}
		return result[i].Priority > result[j].Priority
	})
	return result
}
