package storage

import "time"

const (
	UserRoleAdmin = "admin"
	UserRoleUser  = "user"

	AuthTypeWeb    = "web"
	AuthTypeAPIKey = "api_key"

	DefaultUserRPSLimit = 3
	DefaultUserRPMLimit = 60

	UsageBucketHour = "hour"
	UsageBucketDay  = "day"
)

type User struct {
	ID                 int64      `json:"id"`
	Username           string     `json:"username"`
	NormalizedUsername string     `json:"normalized_username"`
	PasswordHash       string     `json:"-"`
	Role               string     `json:"role"`
	Enabled            bool       `json:"enabled"`
	ExpiresAt          *time.Time `json:"expires_at,omitempty"`
	MustChangePassword bool       `json:"must_change_password"`
	AuthVersion        int64      `json:"auth_version"`
	RPSLimit           int        `json:"rps_limit"`
	RPMLimit           int        `json:"rpm_limit"`
	RateLimitDisabled  bool       `json:"rate_limit_disabled"`
	LastLoginAt        *time.Time `json:"last_login_at,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	DeletedAt          *time.Time `json:"deleted_at,omitempty"`
}

func (u User) IsActiveAt(at time.Time) bool {
	if !u.Enabled || u.DeletedAt != nil {
		return false
	}
	return u.ExpiresAt == nil || u.ExpiresAt.After(at)
}

func (u User) IsEffectiveAdminAt(at time.Time) bool {
	return u.Role == UserRoleAdmin && u.IsActiveAt(at)
}

type CreateUserInput struct {
	Username           string
	PasswordHash       string
	Role               string
	Enabled            *bool
	ExpiresAt          *time.Time
	MustChangePassword *bool
	RPSLimit           int
	RPMLimit           int
	RateLimitDisabled  bool
}

type UpdateUserInput struct {
	Username           *string
	Role               *string
	Enabled            *bool
	ExpiresAt          **time.Time
	MustChangePassword *bool
	RPSLimit           *int
	RPMLimit           *int
	RateLimitDisabled  *bool
}

type UserFilter struct {
	Query          string
	Roles          []string
	Enabled        *bool
	IncludeDeleted bool
	ExpiresBefore  *time.Time
	ExpiresAfter   *time.Time
	Page           int
	PageSize       int
	SortBy         string
	SortDir        string
}

type UserPage struct {
	Items    []User `json:"items"`
	Total    int64  `json:"total"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
}

type APIKey struct {
	ID         int64      `json:"id"`
	UserID     int64      `json:"user_id"`
	KeyPrefix  string     `json:"key_prefix"`
	KeyHash    string     `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

type APIKeyInput struct {
	UserID    int64
	KeyPrefix string
	KeyHash   string
	CreatedAt time.Time
}

type APIRequestLog struct {
	ID          int64     `json:"id"`
	RequestID   string    `json:"request_id"`
	UserID      int64     `json:"user_id"`
	Username    string    `json:"username,omitempty"`
	AuthType    string    `json:"auth_type"`
	Method      string    `json:"method"`
	Endpoint    string    `json:"endpoint"`
	Keyword     string    `json:"keyword,omitempty"`
	StatusCode  int       `json:"status_code"`
	DurationMS  int64     `json:"duration_ms"`
	ResultCount int       `json:"result_count"`
	CacheStatus string    `json:"cache_status,omitempty"`
	ErrorCode   string    `json:"error_code,omitempty"`
	SourceIP    string    `json:"source_ip,omitempty"`
	UserAgent   string    `json:"user_agent,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type APIRequestLogInput struct {
	RequestID   string
	UserID      int64
	AuthType    string
	Method      string
	Endpoint    string
	Keyword     string
	StatusCode  int
	DurationMS  int64
	ResultCount int
	CacheStatus string
	ErrorCode   string
	SourceIP    string
	UserAgent   string
	CreatedAt   time.Time
}

type APIRequestLogFilter struct {
	UserID      *int64
	AuthTypes   []string
	Methods     []string
	Endpoints   []string
	StatusCodes []int
	Query       string
	From        *time.Time
	To          *time.Time
	Page        int
	PageSize    int
	SortBy      string
	SortDir     string
}

type APIRequestLogPage struct {
	Items    []APIRequestLog `json:"items"`
	Total    int64           `json:"total"`
	Page     int             `json:"page"`
	PageSize int             `json:"page_size"`
}

type UsageStatsFilter struct {
	UserID          *int64
	From            time.Time
	To              time.Time
	Bucket          string
	SlowThresholdMS int64
	RecentLimit     int
	TopUserLimit    int
}

type UserUsageSummary struct {
	UserID        int64   `json:"user_id"`
	Username      string  `json:"username"`
	RequestCount  int64   `json:"request_count"`
	SuccessRate   float64 `json:"success_rate"`
	AvgDurationMS float64 `json:"avg_duration_ms"`
}

type UsageOverviewStats struct {
	From                time.Time          `json:"from"`
	To                  time.Time          `json:"to"`
	TotalRequests       int64              `json:"total_requests"`
	SuccessfulRequests  int64              `json:"successful_requests"`
	FailedRequests      int64              `json:"failed_requests"`
	SuccessRate         float64            `json:"success_rate"`
	RateLimitedRequests int64              `json:"rate_limited_requests"`
	ActiveUsers         int64              `json:"active_users"`
	AvgDurationMS       float64            `json:"avg_duration_ms"`
	P95DurationMS       float64            `json:"p95_duration_ms"`
	CacheHits           int64              `json:"cache_hits"`
	CacheHitRate        float64            `json:"cache_hit_rate"`
	TotalResults        int64              `json:"total_results"`
	SlowRequests        int64              `json:"slow_requests"`
	StatusCounts        map[string]int64   `json:"status_counts"`
	ErrorCounts         map[string]int64   `json:"error_counts"`
	TopUsers            []UserUsageSummary `json:"top_users"`
	RecentRequests      []APIRequestLog    `json:"recent_requests"`
}

type UsageTrendPoint struct {
	Bucket              time.Time `json:"bucket"`
	RequestCount        int64     `json:"request_count"`
	SuccessfulRequests  int64     `json:"successful_requests"`
	FailedRequests      int64     `json:"failed_requests"`
	RateLimitedRequests int64     `json:"rate_limited_requests"`
	ActiveUsers         int64     `json:"active_users"`
	AvgDurationMS       float64   `json:"avg_duration_ms"`
	P95DurationMS       float64   `json:"p95_duration_ms"`
	CacheHits           int64     `json:"cache_hits"`
	ResultCount         int64     `json:"result_count"`
}
