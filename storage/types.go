package storage

import (
	"encoding/json"
	"errors"
	"time"

	"pansou/model"
)

const (
	DefaultKeywordType = "general"
	DefaultSourceType  = "manual"

	CheckPending     = "pending"
	CheckValid       = "valid"
	CheckInvalid     = "invalid"
	CheckUnknown     = "unknown"
	CheckUnsupported = "unsupported"

	RunPending      = "pending"
	RunRunning      = "running"
	RunSuccess      = "success"
	RunSuccessEmpty = "success_empty"
	RunFailed       = "failed"
)

var (
	ErrNotFound = errors.New("storage: not found")
	ErrConflict = errors.New("storage: conflict")
	ErrInvalid  = errors.New("storage: invalid input")
)

// Resource is one unique share URL and its accumulated discovery metadata.
type Resource struct {
	ID             int64                   `json:"id"`
	NormalizedURL  string                  `json:"normalized_url"`
	URL            string                  `json:"url"`
	Password       string                  `json:"password,omitempty"`
	Platform       string                  `json:"platform,omitempty"`
	Title          string                  `json:"title,omitempty"`
	Content        string                  `json:"content,omitempty"`
	LinkDatetime   *time.Time              `json:"link_datetime,omitempty"`
	CheckStatus    string                  `json:"check_status"`
	LastCheckedAt  *time.Time              `json:"last_checked_at,omitempty"`
	FirstSeenAt    time.Time               `json:"first_seen_at"`
	LastSeenAt     time.Time               `json:"last_seen_at"`
	DiscoveryCount int64                   `json:"discovery_count"`
	CreatedAt      time.Time               `json:"created_at"`
	UpdatedAt      time.Time               `json:"updated_at"`
	SourceCount    int64                   `json:"source_count"`
	KeywordCount   int64                   `json:"keyword_count"`
	SourcePreview  []ResourceSourcePreview `json:"source_preview,omitempty"`
	Sources        []ResourceSource        `json:"sources,omitempty"`
	Keywords       []ResourceKeyword       `json:"keywords,omitempty"`
}

type ResourceSourcePreview struct {
	ID             int64     `json:"id"`
	ResourceID     int64     `json:"resource_id"`
	SourceType     string    `json:"source_type"`
	SourceKey      string    `json:"source_key"`
	SourceIdentity string    `json:"source_identity"`
	Title          string    `json:"title,omitempty"`
	LastSeenAt     time.Time `json:"last_seen_at"`
	DiscoveryCount int64     `json:"discovery_count"`
}

type ResourceSource struct {
	ID             int64          `json:"id"`
	ResourceID     int64          `json:"resource_id"`
	SourceType     string         `json:"source_type"`
	SourceKey      string         `json:"source_key"`
	SourceIdentity string         `json:"source_identity"`
	MessageID      string         `json:"message_id,omitempty"`
	UniqueID       string         `json:"unique_id,omitempty"`
	Title          string         `json:"title,omitempty"`
	Content        string         `json:"content,omitempty"`
	DiscoveredAt   time.Time      `json:"discovered_at"`
	FirstSeenAt    time.Time      `json:"first_seen_at"`
	LastSeenAt     time.Time      `json:"last_seen_at"`
	DiscoveryCount int64          `json:"discovery_count"`
	SourceMetadata map[string]any `json:"source_metadata,omitempty"`
}

type ResourceKeyword struct {
	ResourceID        int64     `json:"resource_id"`
	KeywordID         *int64    `json:"keyword_id,omitempty"`
	Keyword           string    `json:"keyword"`
	NormalizedKeyword string    `json:"normalized_keyword"`
	KeywordType       string    `json:"keyword_type"`
	FirstSeenAt       time.Time `json:"first_seen_at"`
	LastSeenAt        time.Time `json:"last_seen_at"`
	DiscoveryCount    int64     `json:"discovery_count"`
}

type ResourceSourceInput struct {
	SourceType     string
	SourceKey      string
	SourceIdentity string
	MessageID      string
	UniqueID       string
	Title          string
	Content        string
	DiscoveredAt   time.Time
	Metadata       map[string]any
}

type ResourceInput struct {
	URL           string
	Password      string
	Platform      string
	Title         string
	Content       string
	LinkDatetime  *time.Time
	CheckStatus   string
	LastCheckedAt *time.Time
	DiscoveredAt  time.Time
	Source        ResourceSourceInput
	Keyword       string
	KeywordType   string
}

type ResourceFilter struct {
	Keyword        string
	KeywordType    string
	Query          string
	Platforms      []string
	CheckStatuses  []string
	SourceTypes    []string
	SourceKeys     []string
	Include        []string
	Exclude        []string
	From           *time.Time
	To             *time.Time
	IncludeInvalid bool
	Page           int
	PageSize       int
	Sort           string
}

type ResourcePage struct {
	Items    []Resource `json:"items"`
	Total    int64      `json:"total"`
	Page     int        `json:"page"`
	PageSize int        `json:"page_size"`
}

type ResourceAssociationFilter struct {
	Page     int
	PageSize int
}

type ResourceSourcePage struct {
	Items    []ResourceSource `json:"items"`
	Total    int64            `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
}

type ResourceKeywordPage struct {
	Items    []ResourceKeyword `json:"items"`
	Total    int64             `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
}

type UpsertResult struct {
	Resource Resource `json:"resource"`
	Inserted bool     `json:"inserted"`
}

type UpsertSummary struct {
	Seen       int `json:"seen"`
	Inserted   int `json:"inserted"`
	Updated    int `json:"updated"`
	Duplicates int `json:"duplicates"`
	Skipped    int `json:"skipped"`
}

type Keyword struct {
	ID                int64          `json:"id"`
	Keyword           string         `json:"keyword"`
	NormalizedKeyword string         `json:"normalized_keyword"`
	KeywordType       string         `json:"keyword_type"`
	SourceType        string         `json:"source_type"`
	SourceKey         string         `json:"source_key,omitempty"`
	ExternalID        string         `json:"external_id,omitempty"`
	SourceMetadata    map[string]any `json:"source_metadata,omitempty"`
	Enabled           bool           `json:"enabled"`
	Priority          int            `json:"priority"`
	CooldownSeconds   *int64         `json:"cooldown_seconds,omitempty"`
	LastRunAt         *time.Time     `json:"last_run_at,omitempty"`
	LastSuccessAt     *time.Time     `json:"last_success_at,omitempty"`
	NextEligibleAt    *time.Time     `json:"next_eligible_at,omitempty"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

type CreateKeywordInput struct {
	Keyword         string
	KeywordType     string
	SourceType      string
	SourceKey       string
	ExternalID      string
	SourceMetadata  map[string]any
	Enabled         *bool
	Priority        int
	CooldownSeconds *int64
}

type UpdateKeywordInput struct {
	Keyword         *string
	KeywordType     *string
	SourceType      *string
	SourceKey       *string
	ExternalID      *string
	SourceMetadata  *map[string]any
	Enabled         *bool
	Priority        *int
	CooldownSeconds **int64
}

type KeywordFilter struct {
	Query       string
	KeywordType string
	SourceType  string
	Enabled     *bool
	EligibleAt  *time.Time
	Page        int
	PageSize    int
}

type KeywordPage struct {
	Items    []Keyword `json:"items"`
	Total    int64     `json:"total"`
	Page     int       `json:"page"`
	PageSize int       `json:"page_size"`
}

type CollectionRun struct {
	ID             int64               `json:"id"`
	Trigger        string              `json:"trigger"`
	Status         string              `json:"status"`
	Forced         bool                `json:"forced"`
	TotalItems     int                 `json:"total_items"`
	PendingItems   int                 `json:"pending_items"`
	RunningItems   int                 `json:"running_items"`
	CompletedItems int                 `json:"completed_items"`
	SuccessItems   int                 `json:"success_items"`
	EmptyItems     int                 `json:"empty_items"`
	FailedItems    int                 `json:"failed_items"`
	FoundCount     int                 `json:"found_count"`
	NewCount       int                 `json:"new_count"`
	DuplicateCount int                 `json:"duplicate_count"`
	ErrorMessage   string              `json:"error_message,omitempty"`
	CreatedAt      time.Time           `json:"created_at"`
	StartedAt      *time.Time          `json:"started_at,omitempty"`
	CompletedAt    *time.Time          `json:"completed_at,omitempty"`
	CurrentItem    *CollectionRunItem  `json:"current_item,omitempty"`
	Items          []CollectionRunItem `json:"items,omitempty"`
}

type CollectionRunItem struct {
	ID                int64          `json:"id"`
	RunID             int64          `json:"run_id"`
	KeywordID         *int64         `json:"keyword_id,omitempty"`
	Keyword           string         `json:"keyword"`
	NormalizedKeyword string         `json:"normalized_keyword"`
	KeywordType       string         `json:"keyword_type"`
	Priority          int            `json:"priority"`
	CooldownSeconds   *int64         `json:"cooldown_seconds,omitempty"`
	Status            string         `json:"status"`
	Attempts          int            `json:"attempts"`
	FoundCount        int            `json:"found_count"`
	NewCount          int            `json:"new_count"`
	DuplicateCount    int            `json:"duplicate_count"`
	SourceTotal       int            `json:"source_total"`
	SourceSuccess     int            `json:"source_success"`
	SourceEmpty       int            `json:"source_empty"`
	SourceFailed      int            `json:"source_failed"`
	SourceSummary     map[string]any `json:"source_summary,omitempty"`
	ErrorMessage      string         `json:"error_message,omitempty"`
	CreatedAt         time.Time      `json:"created_at"`
	StartedAt         *time.Time     `json:"started_at,omitempty"`
	CompletedAt       *time.Time     `json:"completed_at,omitempty"`
	DurationMS        int64          `json:"duration_ms"`
}

type RunKeywordInput struct {
	KeywordID       *int64
	Keyword         string
	KeywordType     string
	Priority        int
	CooldownSeconds *int64
}

type CreateRunInput struct {
	Trigger    string
	Force      bool
	KeywordIDs []int64
	Keywords   []RunKeywordInput
}

type CompleteRunItemInput struct {
	Status         string
	FoundCount     int
	NewCount       int
	DuplicateCount int
	SourceSummary  map[string]any
	ErrorMessage   string
	CompletedAt    time.Time
	NextEligibleAt *time.Time
}

type RunFilter struct {
	Trigger  string
	Statuses []string
	From     *time.Time
	To       *time.Time
	Page     int
	PageSize int
}

type RunPage struct {
	Items    []CollectionRun `json:"items"`
	Total    int64           `json:"total"`
	Page     int             `json:"page"`
	PageSize int             `json:"page_size"`
}

type RunItemFilter struct {
	Query    string
	Statuses []string
	Page     int
	PageSize int
}

type RunItemPage struct {
	Items    []CollectionRunItem `json:"items"`
	Total    int64               `json:"total"`
	Page     int                 `json:"page"`
	PageSize int                 `json:"page_size"`
}

type RunSourceFilter struct {
	Types    []string
	Statuses []string
	Page     int
	PageSize int
}

type RunSource struct {
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

type RunSourcePage struct {
	Items    []RunSource `json:"items"`
	Total    int64       `json:"total"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
}

type StatusCount struct {
	Status string `json:"status"`
	Count  int64  `json:"count"`
}

type SourceContribution struct {
	SourceType     string `json:"source_type"`
	SourceKey      string `json:"source_key"`
	ResourceCount  int64  `json:"resource_count"`
	DiscoveryCount int64  `json:"discovery_count"`
}

type StatusCounts map[string]int64

type OverviewStats struct {
	ResourceCount       int64                `json:"resource_count"`
	TodayNew            int64                `json:"today_new"`
	LastSevenDaysNew    int64                `json:"last_seven_days_new"`
	KeywordCount        int64                `json:"keyword_count"`
	EnabledKeywordCount int64                `json:"enabled_keyword_count"`
	StatusCounts        StatusCounts         `json:"status_counts"`
	ActiveRun           *CollectionRun       `json:"active_run,omitempty"`
	TopSources          []SourceContribution `json:"top_sources"`
	RecentRuns          []CollectionRun      `json:"recent_runs"`
}

type TrendPoint struct {
	Date           time.Time `json:"date"`
	NewCount       int64     `json:"new_count"`
	NewResources   int64     `json:"new_resources"`
	Discoveries    int64     `json:"discoveries"`
	ValidCount     int64     `json:"valid_count"`
	ValidResources int64     `json:"valid_resources"`
}

func (r Resource) ToMergedLink() model.MergedLink {
	source := ""
	images := []string(nil)
	if len(r.Sources) > 0 {
		source = r.Sources[0].SourceType + ":" + r.Sources[0].SourceKey
		images = stringSlice(r.Sources[0].SourceMetadata["images"])
	}
	dt := r.LastSeenAt
	if r.LinkDatetime != nil {
		dt = *r.LinkDatetime
	}
	return model.MergedLink{URL: r.URL, Password: r.Password, Note: firstNonEmpty(r.Title, r.Content), Datetime: dt, Source: source, Images: images}
}

func (r Resource) ToSearchResult() model.SearchResult {
	source := ResourceSource{}
	if len(r.Sources) > 0 {
		source = r.Sources[0]
	}
	channel := ""
	uniqueID := r.NormalizedURL
	switch source.SourceType {
	case "tg":
		channel = source.SourceKey
	case "plugin":
		uniqueID = firstNonEmpty(source.SourceKey, "plugin") + "-" + r.NormalizedURL
	}
	dt := r.LastSeenAt
	if r.LinkDatetime != nil {
		dt = *r.LinkDatetime
	}
	return model.SearchResult{
		MessageID: source.MessageID,
		UniqueID:  uniqueID,
		Channel:   channel,
		Datetime:  dt,
		Title:     r.Title,
		Content:   r.Content,
		Links:     []model.Link{{Type: r.Platform, URL: r.URL, Password: r.Password, Datetime: dt, WorkTitle: r.Title}},
		Tags:      stringSlice(source.SourceMetadata["tags"]),
		Images:    stringSlice(source.SourceMetadata["images"]),
	}
}

func (p ResourcePage) ToSearchResponse() model.SearchResponse {
	result := model.SearchResponse{Total: int(p.Total), Results: make([]model.SearchResult, 0, len(p.Items)), MergedByType: make(model.MergedLinks)}
	for _, resource := range p.Items {
		result.Results = append(result.Results, resource.ToSearchResult())
		result.MergedByType[resource.Platform] = append(result.MergedByType[resource.Platform], resource.ToMergedLink())
	}
	return result
}

func metadataJSON(metadata map[string]any) []byte {
	if metadata == nil {
		return []byte("{}")
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return []byte("{}")
	}
	return data
}

func decodeMetadata(data []byte) map[string]any {
	result := make(map[string]any)
	if len(data) > 0 {
		_ = json.Unmarshal(data, &result)
	}
	return result
}

func stringSlice(value any) []string {
	switch items := value.(type) {
	case []string:
		return items
	case []any:
		result := make([]string, 0, len(items))
		for _, item := range items {
			if value, ok := item.(string); ok {
				result = append(result, value)
			}
		}
		return result
	default:
		return nil
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
