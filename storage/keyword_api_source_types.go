package storage

import "time"

const (
	KeywordAPISourceStatusPending = "pending"
	KeywordAPISourceStatusRunning = "running"
	KeywordAPISourceStatusSuccess = "success"
	KeywordAPISourceStatusPartial = "partial"
	KeywordAPISourceStatusFailed  = "failed"

	KeywordAPISourceDefaultIntervalSeconds int64 = 3600
	KeywordAPISourceDefaultTimeoutSeconds        = 15
)

type KeywordAPISource struct {
	ID                     int64             `json:"id"`
	Name                   string            `json:"name"`
	Enabled                bool              `json:"enabled"`
	RequestMethod          string            `json:"request_method"`
	RequestURL             string            `json:"request_url"`
	RequestHeaders         map[string]string `json:"request_headers"`
	QueryParams            map[string]string `json:"query_params"`
	BodyType               string            `json:"body_type"`
	RequestBody            string            `json:"request_body"`
	ProxyURL               string            `json:"proxy_url"`
	TimeoutSeconds         int               `json:"timeout_seconds"`
	ResponsePath           string            `json:"response_path"`
	SyncIntervalSeconds    int64             `json:"sync_interval_seconds"`
	DefaultKeywordType     string            `json:"default_keyword_type"`
	DefaultKeywordEnabled  bool              `json:"default_keyword_enabled"`
	DefaultPriority        int               `json:"default_priority"`
	DefaultCooldownSeconds *int64            `json:"default_cooldown_seconds,omitempty"`
	IterationEnabled       bool              `json:"iteration_enabled"`
	IterationLocation      string            `json:"iteration_location"`
	IterationPath          string            `json:"iteration_path"`
	IterationStart         int64             `json:"iteration_start"`
	IterationStep          int64             `json:"iteration_step"`
	IterationCount         int               `json:"iteration_count"`
	IterationDelaySeconds  int               `json:"iteration_delay_seconds"`
	NextSyncAt             *time.Time        `json:"next_sync_at,omitempty"`
	LastSyncedAt           *time.Time        `json:"last_synced_at,omitempty"`
	LastStatus             string            `json:"last_status"`
	LastError              string            `json:"last_error,omitempty"`
	LastItemCount          int               `json:"last_item_count"`
	LastRequestCount       int               `json:"last_request_count"`
	LastSuccessCount       int               `json:"last_success_count"`
	LastFailureCount       int               `json:"last_failure_count"`
	CreatedAt              time.Time         `json:"created_at"`
	UpdatedAt              time.Time         `json:"updated_at"`
}

type CreateKeywordAPISourceInput struct {
	Name                   string
	Enabled                bool
	RequestMethod          string
	RequestURL             string
	RequestHeaders         map[string]string
	QueryParams            map[string]string
	BodyType               string
	RequestBody            string
	ProxyURL               string
	TimeoutSeconds         int
	ResponsePath           string
	SyncIntervalSeconds    int64
	DefaultKeywordType     string
	DefaultKeywordEnabled  *bool
	DefaultPriority        int
	DefaultCooldownSeconds *int64
	IterationEnabled       bool
	IterationLocation      string
	IterationPath          string
	IterationStart         int64
	IterationStep          int64
	IterationCount         int
	IterationDelaySeconds  int
	NextSyncAt             *time.Time
}

type UpdateKeywordAPISourceInput struct {
	Name                   *string
	Enabled                *bool
	RequestMethod          *string
	RequestURL             *string
	RequestHeaders         *map[string]string
	QueryParams            *map[string]string
	BodyType               *string
	RequestBody            *string
	ProxyURL               *string
	TimeoutSeconds         *int
	ResponsePath           *string
	SyncIntervalSeconds    *int64
	DefaultKeywordType     *string
	DefaultKeywordEnabled  *bool
	DefaultPriority        *int
	DefaultCooldownSeconds **int64
	IterationEnabled       *bool
	IterationLocation      *string
	IterationPath          *string
	IterationStart         *int64
	IterationStep          *int64
	IterationCount         *int
	IterationDelaySeconds  *int
	NextSyncAt             **time.Time
}

type KeywordAPISourceFilter struct {
	Query    string
	Enabled  *bool
	Statuses []string
	Page     int
	PageSize int
}

type KeywordAPISourcePage struct {
	Items    []KeywordAPISource `json:"items"`
	Total    int64              `json:"total"`
	Page     int                `json:"page"`
	PageSize int                `json:"page_size"`
}

type KeywordAPISourceItem struct {
	SourceID        int64     `json:"source_id"`
	KeywordID       int64     `json:"keyword_id"`
	ExternalValue   string    `json:"external_value"`
	NormalizedValue string    `json:"normalized_value"`
	FirstSeenAt     time.Time `json:"first_seen_at"`
	LastSeenAt      time.Time `json:"last_seen_at"`
}

type KeywordAPISourceSyncInput struct {
	SourceID     int64
	Values       []string
	SyncedAt     time.Time
	Status       string
	ErrorMessage string
	RequestCount int
	SuccessCount int
	FailureCount int
}

type KeywordAPISourceSyncResult struct {
	Source           KeywordAPISource `json:"source"`
	Seen             int              `json:"seen"`
	Unique           int              `json:"unique"`
	InsertedKeywords int              `json:"inserted_keywords"`
	ExistingKeywords int              `json:"existing_keywords"`
	LinkedItems      int              `json:"linked_items"`
}
