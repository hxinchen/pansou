package storage

import "time"

const (
	KeywordAPISourceStatusPending = "pending"
	KeywordAPISourceStatusRunning = "running"
	KeywordAPISourceStatusSuccess = "success"
	KeywordAPISourceStatusPartial = "partial"
	KeywordAPISourceStatusFailed  = "failed"

	KeywordAPISyncRunStatusQueued      = "queued"
	KeywordAPISyncRunStatusRunning     = "running"
	KeywordAPISyncRunStatusSuccess     = "success"
	KeywordAPISyncRunStatusPartial     = "partial"
	KeywordAPISyncRunStatusFailed      = "failed"
	KeywordAPISyncRunStatusInterrupted = "interrupted"
	KeywordAPISyncRunStatusCancelled   = "cancelled"

	KeywordAPISyncIterationStatusQueued      = "queued"
	KeywordAPISyncIterationStatusRunning     = "running"
	KeywordAPISyncIterationStatusSuccess     = "success"
	KeywordAPISyncIterationStatusFailed      = "failed"
	KeywordAPISyncIterationStatusSkipped     = "skipped"
	KeywordAPISyncIterationStatusInterrupted = "interrupted"

	KeywordAPISyncTriggerManual    = "manual"
	KeywordAPISyncTriggerSave      = "save"
	KeywordAPISyncTriggerScheduled = "scheduled"
	KeywordAPISyncTriggerLegacy    = "legacy"

	KeywordAPISourceDefaultIntervalSeconds int64 = 3600
	KeywordAPISourceDefaultTimeoutSeconds        = 15
)

type KeywordAPISource struct {
	ID                             int64              `json:"id"`
	Name                           string             `json:"name"`
	Enabled                        bool               `json:"enabled"`
	RequestMethod                  string             `json:"request_method"`
	RequestURL                     string             `json:"request_url"`
	RequestHeaders                 map[string]string  `json:"request_headers"`
	QueryParams                    map[string]string  `json:"query_params"`
	BodyType                       string             `json:"body_type"`
	RequestBody                    string             `json:"request_body"`
	ProxyURL                       string             `json:"proxy_url"`
	TimeoutSeconds                 int                `json:"timeout_seconds"`
	ResponsePath                   string             `json:"response_path"`
	SyncIntervalSeconds            int64              `json:"sync_interval_seconds"`
	DefaultKeywordType             string             `json:"default_keyword_type"`
	DefaultKeywordEnabled          bool               `json:"default_keyword_enabled"`
	DefaultPriority                int                `json:"default_priority"`
	DefaultCooldownSeconds         *int64             `json:"default_cooldown_seconds,omitempty"`
	IterationEnabled               bool               `json:"iteration_enabled"`
	IterationLocation              string             `json:"iteration_location"`
	IterationPath                  string             `json:"iteration_path"`
	IterationStart                 int64              `json:"iteration_start"`
	IterationStep                  int64              `json:"iteration_step"`
	IterationCount                 int                `json:"iteration_count"`
	IterationDelaySeconds          int                `json:"iteration_delay_seconds"`
	IterationUnlimited             bool               `json:"iteration_unlimited"`
	IterationNoKeywordStopCount    int                `json:"iteration_no_keyword_stop_count"`
	IterationRandomDelayMinSeconds int                `json:"iteration_random_delay_min_seconds"`
	IterationRandomDelayMaxSeconds int                `json:"iteration_random_delay_max_seconds"`
	NextSyncAt                     *time.Time         `json:"next_sync_at,omitempty"`
	LastSyncedAt                   *time.Time         `json:"last_synced_at,omitempty"`
	LastStatus                     string             `json:"last_status"`
	LastError                      string             `json:"last_error,omitempty"`
	LastItemCount                  int                `json:"last_item_count"`
	LastRequestCount               int                `json:"last_request_count"`
	LastSuccessCount               int                `json:"last_success_count"`
	LastFailureCount               int                `json:"last_failure_count"`
	SyncConfigRevision             int64              `json:"sync_config_revision"`
	LastAppliedConfigRevision      int64              `json:"last_applied_config_revision"`
	ResultStale                    bool               `json:"result_stale"`
	ActiveRun                      *KeywordAPISyncRun `json:"active_run,omitempty"`
	LatestRun                      *KeywordAPISyncRun `json:"latest_run,omitempty"`
	CreatedAt                      time.Time          `json:"created_at"`
	UpdatedAt                      time.Time          `json:"updated_at"`
}

type CreateKeywordAPISourceInput struct {
	Name                           string
	Enabled                        bool
	RequestMethod                  string
	RequestURL                     string
	RequestHeaders                 map[string]string
	QueryParams                    map[string]string
	BodyType                       string
	RequestBody                    string
	ProxyURL                       string
	TimeoutSeconds                 int
	ResponsePath                   string
	SyncIntervalSeconds            int64
	DefaultKeywordType             string
	DefaultKeywordEnabled          *bool
	DefaultPriority                int
	DefaultCooldownSeconds         *int64
	IterationEnabled               bool
	IterationLocation              string
	IterationPath                  string
	IterationStart                 int64
	IterationStep                  int64
	IterationCount                 int
	IterationDelaySeconds          int
	IterationUnlimited             bool
	IterationNoKeywordStopCount    int
	IterationRandomDelayMinSeconds int
	IterationRandomDelayMaxSeconds int
	NextSyncAt                     *time.Time
}

type UpdateKeywordAPISourceInput struct {
	Name                           *string
	Enabled                        *bool
	RequestMethod                  *string
	RequestURL                     *string
	RequestHeaders                 *map[string]string
	QueryParams                    *map[string]string
	BodyType                       *string
	RequestBody                    *string
	ProxyURL                       *string
	TimeoutSeconds                 *int
	ResponsePath                   *string
	SyncIntervalSeconds            *int64
	DefaultKeywordType             *string
	DefaultKeywordEnabled          *bool
	DefaultPriority                *int
	DefaultCooldownSeconds         **int64
	IterationEnabled               *bool
	IterationLocation              *string
	IterationPath                  *string
	IterationStart                 *int64
	IterationStep                  *int64
	IterationCount                 *int
	IterationDelaySeconds          *int
	IterationUnlimited             *bool
	IterationNoKeywordStopCount    *int
	IterationRandomDelayMinSeconds *int
	IterationRandomDelayMaxSeconds *int
	NextSyncAt                     **time.Time
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

// KeywordAPISyncConfigSnapshot is deliberately value-free for fields that can
// carry secrets. Header and query parameter names are retained for diagnostics,
// but their values, request bodies, proxy credentials, and URL userinfo/path/
// query/fragment values are never persisted in sync history.
type KeywordAPISyncConfigSnapshot struct {
	RequestMethod                  string   `json:"request_method"`
	RequestURL                     string   `json:"request_url"`
	HeaderKeys                     []string `json:"header_keys,omitempty"`
	QueryKeys                      []string `json:"query_keys,omitempty"`
	BodyType                       string   `json:"body_type"`
	HasRequestBody                 bool     `json:"has_request_body"`
	ProxyScheme                    string   `json:"proxy_scheme,omitempty"`
	TimeoutSeconds                 int      `json:"timeout_seconds"`
	ResponsePath                   string   `json:"response_path"`
	DefaultKeywordType             string   `json:"default_keyword_type"`
	DefaultKeywordEnabled          bool     `json:"default_keyword_enabled"`
	DefaultPriority                int      `json:"default_priority"`
	DefaultCooldownSeconds         *int64   `json:"default_cooldown_seconds,omitempty"`
	IterationEnabled               bool     `json:"iteration_enabled"`
	IterationLocation              string   `json:"iteration_location"`
	IterationPath                  string   `json:"iteration_path"`
	IterationStart                 int64    `json:"iteration_start"`
	IterationStep                  int64    `json:"iteration_step"`
	IterationCount                 int      `json:"iteration_count"`
	IterationDelaySeconds          int      `json:"iteration_delay_seconds"`
	IterationUnlimited             bool     `json:"iteration_unlimited"`
	IterationNoKeywordStopCount    int      `json:"iteration_no_keyword_stop_count"`
	IterationRandomDelayMinSeconds int      `json:"iteration_random_delay_min_seconds"`
	IterationRandomDelayMaxSeconds int      `json:"iteration_random_delay_max_seconds"`
}

type KeywordAPISyncRun struct {
	ID                    int64                        `json:"id"`
	SourceID              *int64                       `json:"source_id,omitempty"`
	LiveSourceID          *int64                       `json:"-"`
	SourceExists          bool                         `json:"source_exists"`
	SourceName            string                       `json:"source_name"`
	Trigger               string                       `json:"trigger"`
	Status                string                       `json:"status"`
	ConfigRevision        int64                        `json:"config_revision"`
	RequestSummary        KeywordAPISyncConfigSnapshot `json:"request_summary"`
	Unlimited             bool                         `json:"unlimited"`
	TotalIterations       *int                         `json:"total_iterations"`
	CompletedIterations   int                          `json:"completed_iterations"`
	SuccessIterations     int                          `json:"success_iterations"`
	FailedIterations      int                          `json:"failed_iterations"`
	CurrentIteration      int                          `json:"current_iteration"`
	IterationRecordsTotal int                          `json:"iteration_records_total"`
	IterationsTruncated   bool                         `json:"iterations_truncated"`
	RawItemCount          int                          `json:"raw_extracted_count"`
	UniqueItemCount       int                          `json:"unique_count"`
	NewKeywordCount       int                          `json:"new_count"`
	ExistingKeywordCount  int                          `json:"existing_count"`
	RequestCount          int                          `json:"request_count"`
	SuccessCount          int                          `json:"success_count"`
	FailureCount          int                          `json:"failure_count"`
	ErrorMessage          string                       `json:"error,omitempty"`
	LeaseOwner            string                       `json:"-"`
	LeaseToken            string                       `json:"-"`
	LeaseUntil            *time.Time                   `json:"lease_until,omitempty"`
	QueuedAt              time.Time                    `json:"queued_at"`
	StartedAt             *time.Time                   `json:"started_at,omitempty"`
	CompletedAt           *time.Time                   `json:"finished_at,omitempty"`
	CreatedAt             time.Time                    `json:"created_at"`
	UpdatedAt             time.Time                    `json:"updated_at"`
	Iterations            []KeywordAPISyncIteration    `json:"iterations,omitempty"`
}

type KeywordAPISyncIteration struct {
	ID                   int64      `json:"id"`
	RunID                int64      `json:"run_id"`
	Sequence             int        `json:"index"`
	IterationValue       int64      `json:"iteration_value"`
	Status               string     `json:"status"`
	HTTPStatus           int        `json:"http_status"`
	DurationMS           int64      `json:"duration_ms"`
	ResponseBytes        int64      `json:"response_size"`
	RawItemCount         int        `json:"raw_extracted_count"`
	UniqueItemCount      int        `json:"unique_count"`
	CrossIterationNew    int        `json:"cross_iteration_new"`
	NewKeywordCount      int        `json:"new_count"`
	ExistingKeywordCount int        `json:"existing_count"`
	ErrorMessage         string     `json:"error,omitempty"`
	Samples              []string   `json:"samples,omitempty"`
	StartedAt            *time.Time `json:"started_at,omitempty"`
	CompletedAt          *time.Time `json:"completed_at,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

type KeywordAPISyncRunFilter struct {
	SourceID *int64
	Statuses []string
	Triggers []string
	From     *time.Time
	To       *time.Time
	Page     int
	PageSize int
}

type KeywordAPISyncRunPage struct {
	Items    []KeywordAPISyncRun `json:"items"`
	Total    int64               `json:"total"`
	Page     int                 `json:"page"`
	PageSize int                 `json:"page_size"`
}

type KeywordAPISyncClaim struct {
	Run    KeywordAPISyncRun `json:"run"`
	Source KeywordAPISource  `json:"source"`
}

type KeywordAPISyncIterationInput struct {
	RunID                int64
	LeaseOwner           string
	LeaseToken           string
	Sequence             int
	IterationValue       int64
	Status               string
	HTTPStatus           int
	DurationMS           int64
	ResponseBytes        int64
	RawItemCount         int
	UniqueItemCount      int
	CrossIterationNew    int
	NewKeywordCount      int
	ExistingKeywordCount int
	ErrorMessage         string
	Samples              []string
	StartedAt            time.Time
	CompletedAt          time.Time
}

type KeywordAPISyncFinalizeInput struct {
	RunID          int64
	LeaseOwner     string
	LeaseToken     string
	Values         []string
	ValueSequences []int
	SyncedAt       time.Time
	Status         string
	ErrorMessage   string
	RawItemCount   int
	RequestCount   int
	SuccessCount   int
	FailureCount   int
}
