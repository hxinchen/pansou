package storage

import (
	"encoding/json"
	"time"
)

const (
	SourceConfigEventSuccess = "success"
	SourceConfigEventFailed  = "failed"

	CredentialScopeUserPrivate  = "user_private"
	CredentialScopeAdminPrivate = "admin_private"
	CredentialScopePublicShared = "public_shared"

	CredentialStatusActive  = "active"
	CredentialStatusInvalid = "invalid"
	CredentialStatusExpired = "expired"
)

type SearchSourceConfig struct {
	ID            int16           `json:"id"`
	Version       int64           `json:"version"`
	SchemaVersion int             `json:"schema_version"`
	Config        json.RawMessage `json:"config"`
	UpdatedBy     *int64          `json:"updated_by,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type InitializeSearchSourceConfigInput struct {
	SchemaVersion int
	Config        json.RawMessage
	UpdatedBy     *int64
}

type UpdateSearchSourceConfigInput struct {
	ExpectedVersion int64
	SchemaVersion   int
	Config          json.RawMessage
	UpdatedBy       *int64
	ChangeSummary   map[string]any
	UpdatedAt       time.Time
}

type SearchSourceConfigEvent struct {
	ID            int64          `json:"id"`
	ActorUserID   *int64         `json:"actor_user_id,omitempty"`
	BaseVersion   int64          `json:"base_version"`
	ResultVersion *int64         `json:"result_version,omitempty"`
	Result        string         `json:"result"`
	ErrorCode     string         `json:"error_code,omitempty"`
	ChangeSummary map[string]any `json:"change_summary"`
	CreatedAt     time.Time      `json:"created_at"`
}

type CreateSearchSourceConfigEventInput struct {
	ActorUserID   *int64
	BaseVersion   int64
	ResultVersion *int64
	Result        string
	ErrorCode     string
	ChangeSummary map[string]any
	CreatedAt     time.Time
}

type SearchSourceConfigEventFilter struct {
	ActorUserID *int64
	Results     []string
	From        *time.Time
	To          *time.Time
	Page        int
	PageSize    int
}

type SearchSourceConfigEventPage struct {
	Items    []SearchSourceConfigEvent `json:"items"`
	Total    int64                     `json:"total"`
	Page     int                       `json:"page"`
	PageSize int                       `json:"page_size"`
}

type PluginCredential struct {
	ID                    int64          `json:"id"`
	PublicID              string         `json:"public_id"`
	PluginKey             string         `json:"plugin_key"`
	Scope                 string         `json:"scope"`
	OwnerUserID           *int64         `json:"owner_user_id,omitempty"`
	CreatedByUserID       *int64         `json:"created_by_user_id,omitempty"`
	DisplayName           string         `json:"display_name"`
	PublicMetadata        map[string]any `json:"public_metadata"`
	SecretSchemaVersion   int            `json:"secret_schema_version"`
	BindingFingerprint    []byte         `json:"-"`
	Ciphertext            []byte         `json:"-"`
	Nonce                 []byte         `json:"-"`
	KeyVersion            int            `json:"key_version"`
	CredentialFingerprint []byte         `json:"-"`
	Revision              int64          `json:"revision"`
	OwnerEnabled          bool           `json:"owner_enabled"`
	AdminSuspendedAt      *time.Time     `json:"admin_suspended_at,omitempty"`
	AdminSuspendedBy      *int64         `json:"admin_suspended_by,omitempty"`
	Status                string         `json:"status"`
	ExpiresAt             *time.Time     `json:"expires_at,omitempty"`
	CooldownUntil         *time.Time     `json:"cooldown_until,omitempty"`
	LastUsedAt            *time.Time     `json:"last_used_at,omitempty"`
	LastSuccessAt         *time.Time     `json:"last_success_at,omitempty"`
	LastFailureAt         *time.Time     `json:"last_failure_at,omitempty"`
	LastErrorCode         string         `json:"last_error_code,omitempty"`
	ConsecutiveFailures   int            `json:"consecutive_failures"`
	CreatedAt             time.Time      `json:"created_at"`
	UpdatedAt             time.Time      `json:"updated_at"`
}

func (credential PluginCredential) IsUsableAt(at time.Time) bool {
	if !credential.OwnerEnabled || credential.AdminSuspendedAt != nil || credential.Status != CredentialStatusActive {
		return false
	}
	if credential.ExpiresAt != nil && !credential.ExpiresAt.After(at) {
		return false
	}
	return credential.CooldownUntil == nil || !credential.CooldownUntil.After(at)
}

type CreatePluginCredentialInput struct {
	PublicID              string
	PluginKey             string
	Scope                 string
	OwnerUserID           *int64
	CreatedByUserID       *int64
	DisplayName           string
	PublicMetadata        map[string]any
	SecretSchemaVersion   int
	BindingFingerprint    []byte
	Ciphertext            []byte
	Nonce                 []byte
	KeyVersion            int
	CredentialFingerprint []byte
	OwnerEnabled          bool
	Status                string
	ExpiresAt             *time.Time
	CreatedAt             time.Time
}

type PluginCredentialFilter struct {
	OwnerUserID    *int64
	PluginKeys     []string
	Scopes         []string
	Statuses       []string
	OwnerEnabled   *bool
	Suspended      *bool
	IncludeSecrets bool
	Page           int
	PageSize       int
}

type PluginCredentialPage struct {
	Items    []PluginCredential `json:"items"`
	Total    int64              `json:"total"`
	Page     int                `json:"page"`
	PageSize int                `json:"page_size"`
}

type ReplaceCredentialEnvelopeInput struct {
	PublicID              string
	ExpectedRevision      int64
	Scope                 string
	DisplayName           string
	PublicMetadata        map[string]any
	SecretSchemaVersion   int
	BindingFingerprint    []byte
	Ciphertext            []byte
	Nonce                 []byte
	KeyVersion            int
	CredentialFingerprint []byte
	Status                string
	ExpiresAt             *time.Time
	UpdatedAt             time.Time
}

type CredentialSuspensionInput struct {
	PublicID    string
	Suspended   bool
	AdminUserID int64
	At          time.Time
}

type CredentialSuccessInput struct {
	PublicID    string
	SucceededAt time.Time
}

type CredentialFailureInput struct {
	PublicID      string
	Status        string
	ErrorCode     string
	FailedAt      time.Time
	CooldownUntil *time.Time
}

type DataMigration struct {
	MigrationKey string         `json:"migration_key"`
	CompletedAt  time.Time      `json:"completed_at"`
	Summary      map[string]any `json:"summary"`
}

type ImportPluginCredentialsInput struct {
	MigrationKey string
	Credentials  []CreatePluginCredentialInput
	Summary      map[string]any
	CompletedAt  time.Time
}

type DataMigrationResult struct {
	DataMigration
	Applied  bool `json:"applied"`
	Imported int  `json:"imported"`
}
