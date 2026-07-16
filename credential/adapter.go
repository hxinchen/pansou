package credential

import (
	"context"
	"errors"
	"time"

	"pansou/model"
	"pansou/storage"
)

// ErrNoResults means at least one usable credential completed a healthy
// upstream search but returned no results. The service may try the next
// credential layer without misreporting the source as failed.
var ErrNoResults = errors.New("credential search completed with no results")

type SecretOpener func(storage.PluginCredential) ([]byte, error)

type Access struct {
	Open    SecretOpener
	Refresh func(context.Context, string, LoginMaterial) error
	Success func(context.Context, string)
	Failure func(context.Context, string, string, string, *time.Time)
}

type LayerSearcher interface {
	SearchCredentialLayer(context.Context, string, map[string]interface{}, []storage.PluginCredential, Access) ([]model.SearchResult, bool, error)
}

type LoginMaterial struct {
	Secret         []byte
	StableID       []byte
	DisplayName    string
	PublicMetadata map[string]any
	ConfigBinding  []byte
	Status         string
	ExpiresAt      *time.Time
}

type PasswordLoginAdapter interface {
	LoginWithPassword(context.Context, string, string) (LoginMaterial, error)
}

type QRBeginResult struct {
	State      any
	QRCodeData string
	ExpiresAt  time.Time
}

type QRPollResult struct {
	Status   string
	Message  string
	Material *LoginMaterial
}

type QRLoginAdapter interface {
	BeginQRLogin(context.Context) (QRBeginResult, error)
	PollQRLogin(context.Context, any) (QRPollResult, error)
}

// LegacyCredentialParser converts one legacy account JSON file without making
// any network request. The migration coordinator owns file enumeration and the
// all-or-nothing database transaction.
type LegacyCredentialParser interface {
	ParseLegacyCredential([]byte) (LoginMaterial, error)
}
