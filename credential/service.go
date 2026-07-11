package credential

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"pansou/storage"
)

const (
	ActorUser      = "user"
	ActorAdmin     = "admin"
	ActorCollector = "collector"
)

type Repository interface {
	CreatePluginCredential(context.Context, storage.CreatePluginCredentialInput) (storage.PluginCredential, error)
	GetPluginCredentialByPublicID(context.Context, string) (storage.PluginCredential, error)
	ListUserPluginCredentials(context.Context, int64, storage.PluginCredentialFilter) (storage.PluginCredentialPage, error)
	ListAdminPluginCredentials(context.Context, storage.PluginCredentialFilter) (storage.PluginCredentialPage, error)
	ListUsableUserPrivateCredentialCandidates(context.Context, int64, string, time.Time, int) ([]storage.PluginCredential, error)
	ListUsableAdminPrivateCredentialCandidates(context.Context, string, time.Time, int) ([]storage.PluginCredential, error)
	ListUsableSharedCredentialCandidates(context.Context, string, time.Time, int) ([]storage.PluginCredential, error)
	ReplacePluginCredentialEnvelopeCAS(context.Context, storage.ReplaceCredentialEnvelopeInput) (storage.PluginCredential, error)
	ReplaceUserPluginCredentialEnvelopeCAS(context.Context, int64, storage.ReplaceCredentialEnvelopeInput) (storage.PluginCredential, error)
	SetAdminCredentialEnabled(context.Context, string, bool) error
	SetCredentialOwnerEnabled(context.Context, string, int64, bool) error
	SetCredentialAdminSuspension(context.Context, storage.CredentialSuspensionInput) error
	DeleteUserPluginCredential(context.Context, string, int64) error
	DeleteAdminPluginCredential(context.Context, string) error
	RecordPluginCredentialSuccess(context.Context, storage.CredentialSuccessInput) error
	RecordPluginCredentialFailure(context.Context, storage.CredentialFailureInput) error
}

type Service struct {
	repository Repository
	cipher     *Cipher
	flows      *FlowStore
	now        func() time.Time
}

func NewService(repository Repository, cipher *Cipher) *Service {
	return &Service{repository: repository, cipher: cipher, flows: NewFlowStore(5*time.Minute, 2, time.Second), now: time.Now}
}

func (s *Service) Flows() *FlowStore { return s.flows }

type CreateInput struct {
	PluginKey       string
	Scope           string
	OwnerUserID     *int64
	CreatedByUserID *int64
	DisplayName     string
	PublicMetadata  map[string]any
	Secret          []byte
	StableID        []byte
	ConfigBinding   []byte
	Status          string
	ExpiresAt       *time.Time
}

func (s *Service) Create(ctx context.Context, input CreateInput) (storage.PluginCredential, error) {
	if s == nil || s.repository == nil || s.cipher == nil {
		return storage.PluginCredential{}, errors.New("credential service is unavailable")
	}
	prepared, err := s.Prepare(input)
	if err != nil {
		return storage.PluginCredential{}, err
	}
	created, err := s.repository.CreatePluginCredential(ctx, prepared)
	clear(input.Secret)
	return created, err
}

// Prepare encrypts and validates a credential without writing it. Legacy data
// migration uses this to prepare every file before opening its single database
// transaction.
func (s *Service) Prepare(input CreateInput) (storage.CreatePluginCredentialInput, error) {
	if s == nil || s.cipher == nil {
		return storage.CreatePluginCredentialInput{}, errors.New("credential cipher is unavailable")
	}
	if err := validateCreateInput(input); err != nil {
		return storage.CreatePluginCredentialInput{}, err
	}
	publicID, err := randomPublicID()
	if err != nil {
		return storage.CreatePluginCredentialInput{}, err
	}
	binding := Binding{PublicID: publicID, PluginKey: input.PluginKey, Scope: input.Scope, OwnerUserID: input.OwnerUserID, SecretSchemaVersion: 1, ConfigBinding: input.ConfigBinding}
	envelope, err := s.cipher.Encrypt(binding, input.Secret, input.StableID)
	if err != nil {
		return storage.CreatePluginCredentialInput{}, err
	}
	metadata := sanitizeMetadata(NormalizePublicMetadata(input.PluginKey, input.PublicMetadata))
	if len(input.ConfigBinding) > 0 {
		metadata["binding_context"] = base64.RawURLEncoding.EncodeToString(input.ConfigBinding)
	}
	return storage.CreatePluginCredentialInput{
		PublicID: publicID, PluginKey: strings.ToLower(strings.TrimSpace(input.PluginKey)), Scope: input.Scope,
		OwnerUserID: input.OwnerUserID, CreatedByUserID: input.CreatedByUserID, DisplayName: strings.TrimSpace(input.DisplayName),
		PublicMetadata: metadata, SecretSchemaVersion: 1, BindingFingerprint: envelope.BindingFingerprint,
		Ciphertext: envelope.Ciphertext, Nonce: envelope.Nonce, KeyVersion: envelope.KeyVersion,
		CredentialFingerprint: envelope.CredentialFingerprint, OwnerEnabled: true, Status: normalizedCredentialStatus(input.Status, input.ExpiresAt, s.now()),
		ExpiresAt: input.ExpiresAt, CreatedAt: s.now(),
	}, nil
}

func normalizedCredentialStatus(status string, expiresAt *time.Time, now time.Time) string {
	status = strings.TrimSpace(status)
	if expiresAt != nil && !expiresAt.After(now) {
		return storage.CredentialStatusExpired
	}
	switch status {
	case storage.CredentialStatusActive, storage.CredentialStatusInvalid, storage.CredentialStatusExpired:
		return status
	default:
		return storage.CredentialStatusActive
	}
}

func (s *Service) Open(credential storage.PluginCredential, configBinding []byte) ([]byte, error) {
	if s == nil || s.cipher == nil {
		return nil, errors.New("credential cipher is unavailable")
	}
	binding := Binding{PublicID: credential.PublicID, PluginKey: credential.PluginKey, Scope: credential.Scope, OwnerUserID: credential.OwnerUserID, SecretSchemaVersion: credential.SecretSchemaVersion, ConfigBinding: configBinding}
	return s.cipher.Decrypt(binding, Envelope{Ciphertext: credential.Ciphertext, Nonce: credential.Nonce, BindingFingerprint: credential.BindingFingerprint, CredentialFingerprint: credential.CredentialFingerprint, KeyVersion: credential.KeyVersion})
}

// OpenStored decrypts a credential using the non-secret binding context that
// was persisted with it. Callers should prefer this method during searches so
// runtime configuration stays part of the authenticated envelope.
func (s *Service) OpenStored(value storage.PluginCredential) ([]byte, error) {
	return s.Open(value, metadataBinding(value.PublicMetadata))
}

type Identity struct {
	Actor  string
	UserID int64
}

type Layers struct {
	Private []storage.PluginCredential
	Shared  []storage.PluginCredential
}

func (s *Service) Resolve(ctx context.Context, identity Identity, pluginKey string, limit int) (Layers, error) {
	if s == nil || s.repository == nil {
		return Layers{}, errors.New("credential service is unavailable")
	}
	now := s.now()
	var private []storage.PluginCredential
	var err error
	switch identity.Actor {
	case ActorAdmin, ActorCollector:
		private, err = s.repository.ListUsableAdminPrivateCredentialCandidates(ctx, pluginKey, now, limit)
	case ActorUser:
		if identity.UserID <= 0 {
			return Layers{}, storage.ErrInvalid
		}
		private, err = s.repository.ListUsableUserPrivateCredentialCandidates(ctx, identity.UserID, pluginKey, now, limit)
	default:
		return Layers{}, nil
	}
	if err != nil {
		return Layers{}, err
	}
	shared, err := s.repository.ListUsableSharedCredentialCandidates(ctx, pluginKey, now, limit)
	if err != nil {
		return Layers{}, err
	}
	return Layers{Private: private, Shared: shared}, nil
}

func (s *Service) ListUser(ctx context.Context, userID int64, filter storage.PluginCredentialFilter) (storage.PluginCredentialPage, error) {
	return s.repository.ListUserPluginCredentials(ctx, userID, filter)
}

func (s *Service) ListAdmin(ctx context.Context, filter storage.PluginCredentialFilter) (storage.PluginCredentialPage, error) {
	return s.repository.ListAdminPluginCredentials(ctx, filter)
}

func (s *Service) SetUserEnabled(ctx context.Context, userID int64, publicID string, enabled bool) error {
	return s.repository.SetCredentialOwnerEnabled(ctx, publicID, userID, enabled)
}

func (s *Service) SetAdminEnabled(ctx context.Context, publicID string, enabled bool) error {
	return s.repository.SetAdminCredentialEnabled(ctx, publicID, enabled)
}

func (s *Service) Suspend(ctx context.Context, adminID int64, publicID string, suspended bool) error {
	return s.repository.SetCredentialAdminSuspension(ctx, storage.CredentialSuspensionInput{PublicID: publicID, Suspended: suspended, AdminUserID: adminID, At: s.now()})
}

func (s *Service) DeleteUser(ctx context.Context, userID int64, publicID string) error {
	return s.repository.DeleteUserPluginCredential(ctx, publicID, userID)
}

func (s *Service) DeleteAdmin(ctx context.Context, publicID string) error {
	return s.repository.DeleteAdminPluginCredential(ctx, publicID)
}

func (s *Service) DeleteAnyAsAdmin(ctx context.Context, publicID string) error {
	repository, ok := s.repository.(interface {
		DeletePluginCredentialByAdmin(context.Context, string) error
	})
	if !ok {
		return errors.New("administrator credential deletion is unavailable")
	}
	return repository.DeletePluginCredentialByAdmin(ctx, publicID)
}

func (s *Service) ReplaceUser(ctx context.Context, userID int64, publicID string, material LoginMaterial) (storage.PluginCredential, error) {
	current, err := s.repository.GetPluginCredentialByPublicID(ctx, publicID)
	if err != nil {
		return storage.PluginCredential{}, err
	}
	if current.Scope != storage.CredentialScopeUserPrivate || current.OwnerUserID == nil || *current.OwnerUserID != userID {
		return storage.PluginCredential{}, storage.ErrNotFound
	}
	input, err := s.replacementInput(current, material)
	if err != nil {
		return storage.PluginCredential{}, err
	}
	return s.repository.ReplaceUserPluginCredentialEnvelopeCAS(ctx, userID, input)
}

func (s *Service) ReplaceAdmin(ctx context.Context, publicID string, material LoginMaterial) (storage.PluginCredential, error) {
	current, err := s.repository.GetPluginCredentialByPublicID(ctx, publicID)
	if err != nil {
		return storage.PluginCredential{}, err
	}
	if current.Scope != storage.CredentialScopeAdminPrivate && current.Scope != storage.CredentialScopePublicShared {
		return storage.PluginCredential{}, storage.ErrNotFound
	}
	input, err := s.replacementInput(current, material)
	if err != nil {
		return storage.PluginCredential{}, err
	}
	return s.repository.ReplacePluginCredentialEnvelopeCAS(ctx, input)
}

func (s *Service) UpdateUserMetadata(ctx context.Context, userID int64, publicID, displayName string, metadata map[string]any) (storage.PluginCredential, error) {
	current, err := s.repository.GetPluginCredentialByPublicID(ctx, publicID)
	if err != nil {
		return storage.PluginCredential{}, err
	}
	if current.Scope != storage.CredentialScopeUserPrivate || current.OwnerUserID == nil || *current.OwnerUserID != userID {
		return storage.PluginCredential{}, storage.ErrNotFound
	}
	configBinding := metadataBinding(current.PublicMetadata)
	plaintext, err := s.Open(current, configBinding)
	if err != nil {
		return storage.PluginCredential{}, err
	}
	defer clear(plaintext)
	binding := Binding{PublicID: current.PublicID, PluginKey: current.PluginKey, Scope: current.Scope, OwnerUserID: current.OwnerUserID, SecretSchemaVersion: current.SecretSchemaVersion, ConfigBinding: configBinding}
	envelope, err := s.cipher.Encrypt(binding, plaintext, []byte("metadata-update"))
	if err != nil {
		return storage.PluginCredential{}, err
	}
	envelope.CredentialFingerprint = current.CredentialFingerprint
	merged := sanitizeMetadata(current.PublicMetadata)
	for key, value := range sanitizeMetadata(NormalizePublicMetadata(current.PluginKey, metadata)) {
		merged[key] = value
	}
	merged = NormalizePublicMetadata(current.PluginKey, merged)
	if len(configBinding) > 0 {
		merged["binding_context"] = base64.RawURLEncoding.EncodeToString(configBinding)
	}
	if strings.TrimSpace(displayName) == "" {
		displayName = current.DisplayName
	}
	return s.repository.ReplaceUserPluginCredentialEnvelopeCAS(ctx, userID, storage.ReplaceCredentialEnvelopeInput{
		PublicID: current.PublicID, ExpectedRevision: current.Revision, Scope: current.Scope,
		DisplayName: strings.TrimSpace(displayName), PublicMetadata: merged, SecretSchemaVersion: current.SecretSchemaVersion,
		BindingFingerprint: envelope.BindingFingerprint, Ciphertext: envelope.Ciphertext, Nonce: envelope.Nonce,
		KeyVersion: envelope.KeyVersion, CredentialFingerprint: envelope.CredentialFingerprint,
		Status: current.Status, ExpiresAt: current.ExpiresAt, UpdatedAt: s.now(),
	})
}

func (s *Service) UpdateAdminMetadata(ctx context.Context, publicID, displayName string, metadata map[string]any) (storage.PluginCredential, error) {
	current, err := s.repository.GetPluginCredentialByPublicID(ctx, publicID)
	if err != nil {
		return storage.PluginCredential{}, err
	}
	if current.Scope != storage.CredentialScopeAdminPrivate && current.Scope != storage.CredentialScopePublicShared {
		return storage.PluginCredential{}, storage.ErrNotFound
	}
	configBinding := metadataBinding(current.PublicMetadata)
	plaintext, err := s.Open(current, configBinding)
	if err != nil {
		return storage.PluginCredential{}, err
	}
	defer clear(plaintext)
	binding := Binding{PublicID: current.PublicID, PluginKey: current.PluginKey, Scope: current.Scope, OwnerUserID: current.OwnerUserID, SecretSchemaVersion: current.SecretSchemaVersion, ConfigBinding: configBinding}
	envelope, err := s.cipher.Encrypt(binding, plaintext, []byte("metadata-update"))
	if err != nil {
		return storage.PluginCredential{}, err
	}
	envelope.CredentialFingerprint = current.CredentialFingerprint
	merged := sanitizeMetadata(current.PublicMetadata)
	for key, value := range sanitizeMetadata(NormalizePublicMetadata(current.PluginKey, metadata)) {
		merged[key] = value
	}
	merged = NormalizePublicMetadata(current.PluginKey, merged)
	if len(configBinding) > 0 {
		merged["binding_context"] = base64.RawURLEncoding.EncodeToString(configBinding)
	}
	if strings.TrimSpace(displayName) == "" {
		displayName = current.DisplayName
	}
	return s.repository.ReplacePluginCredentialEnvelopeCAS(ctx, storage.ReplaceCredentialEnvelopeInput{
		PublicID: current.PublicID, ExpectedRevision: current.Revision, Scope: current.Scope,
		DisplayName: strings.TrimSpace(displayName), PublicMetadata: merged, SecretSchemaVersion: current.SecretSchemaVersion,
		BindingFingerprint: envelope.BindingFingerprint, Ciphertext: envelope.Ciphertext, Nonce: envelope.Nonce,
		KeyVersion: envelope.KeyVersion, CredentialFingerprint: envelope.CredentialFingerprint,
		Status: current.Status, ExpiresAt: current.ExpiresAt, UpdatedAt: s.now(),
	})
}

func (s *Service) replacementInput(current storage.PluginCredential, material LoginMaterial) (storage.ReplaceCredentialEnvelopeInput, error) {
	if len(material.Secret) == 0 || len(material.StableID) == 0 {
		return storage.ReplaceCredentialEnvelopeInput{}, storage.ErrInvalid
	}
	binding := Binding{PublicID: current.PublicID, PluginKey: current.PluginKey, Scope: current.Scope, OwnerUserID: current.OwnerUserID, SecretSchemaVersion: current.SecretSchemaVersion, ConfigBinding: material.ConfigBinding}
	envelope, err := s.cipher.Encrypt(binding, material.Secret, material.StableID)
	clear(material.Secret)
	if err != nil {
		return storage.ReplaceCredentialEnvelopeInput{}, err
	}
	metadata := sanitizeMetadata(NormalizePublicMetadata(current.PluginKey, material.PublicMetadata))
	if len(material.ConfigBinding) > 0 {
		metadata["binding_context"] = base64.RawURLEncoding.EncodeToString(material.ConfigBinding)
	}
	displayName := strings.TrimSpace(material.DisplayName)
	if displayName == "" {
		displayName = current.DisplayName
	}
	return storage.ReplaceCredentialEnvelopeInput{
		PublicID: current.PublicID, ExpectedRevision: current.Revision, Scope: current.Scope,
		DisplayName: displayName, PublicMetadata: metadata, SecretSchemaVersion: current.SecretSchemaVersion,
		BindingFingerprint: envelope.BindingFingerprint, Ciphertext: envelope.Ciphertext, Nonce: envelope.Nonce,
		KeyVersion: envelope.KeyVersion, CredentialFingerprint: envelope.CredentialFingerprint,
		Status: normalizedCredentialStatus(material.Status, material.ExpiresAt, s.now()), ExpiresAt: material.ExpiresAt, UpdatedAt: s.now(),
	}, nil
}

func (s *Service) Success(ctx context.Context, publicID string) error {
	return s.repository.RecordPluginCredentialSuccess(ctx, storage.CredentialSuccessInput{PublicID: publicID, SucceededAt: s.now()})
}

func (s *Service) Failure(ctx context.Context, publicID, status, code string, cooldown *time.Time) error {
	return s.repository.RecordPluginCredentialFailure(ctx, storage.CredentialFailureInput{PublicID: publicID, Status: status, ErrorCode: code, FailedAt: s.now(), CooldownUntil: cooldown})
}

func (s *Service) ChangeAdminScope(ctx context.Context, publicID, scope string) (storage.PluginCredential, error) {
	if scope != storage.CredentialScopeAdminPrivate && scope != storage.CredentialScopePublicShared {
		return storage.PluginCredential{}, storage.ErrInvalid
	}
	current, err := s.repository.GetPluginCredentialByPublicID(ctx, publicID)
	if err != nil {
		return storage.PluginCredential{}, err
	}
	if current.Scope != storage.CredentialScopeAdminPrivate && current.Scope != storage.CredentialScopePublicShared {
		return storage.PluginCredential{}, storage.ErrNotFound
	}
	configBinding := metadataBinding(current.PublicMetadata)
	plaintext, err := s.Open(current, configBinding)
	if err != nil {
		return storage.PluginCredential{}, err
	}
	defer clear(plaintext)
	newBinding := Binding{PublicID: current.PublicID, PluginKey: current.PluginKey, Scope: scope, SecretSchemaVersion: current.SecretSchemaVersion, ConfigBinding: configBinding}
	envelope, err := s.cipher.Encrypt(newBinding, plaintext, []byte("scope-change"))
	if err != nil {
		return storage.PluginCredential{}, err
	}
	envelope.CredentialFingerprint = current.CredentialFingerprint
	return s.repository.ReplacePluginCredentialEnvelopeCAS(ctx, storage.ReplaceCredentialEnvelopeInput{
		PublicID: current.PublicID, ExpectedRevision: current.Revision, Scope: scope, DisplayName: current.DisplayName,
		PublicMetadata: current.PublicMetadata, SecretSchemaVersion: current.SecretSchemaVersion,
		BindingFingerprint: envelope.BindingFingerprint, Ciphertext: envelope.Ciphertext, Nonce: envelope.Nonce,
		KeyVersion: envelope.KeyVersion, CredentialFingerprint: envelope.CredentialFingerprint,
		Status: current.Status, ExpiresAt: current.ExpiresAt, UpdatedAt: s.now(),
	})
}

func validateCreateInput(input CreateInput) error {
	if strings.TrimSpace(input.PluginKey) == "" || len(input.Secret) == 0 || len(input.StableID) == 0 {
		return storage.ErrInvalid
	}
	if input.Scope == storage.CredentialScopeUserPrivate {
		if input.OwnerUserID == nil || *input.OwnerUserID <= 0 {
			return storage.ErrInvalid
		}
	} else if input.Scope != storage.CredentialScopeAdminPrivate && input.Scope != storage.CredentialScopePublicShared {
		return storage.ErrInvalid
	} else if input.OwnerUserID != nil {
		return storage.ErrInvalid
	}
	return nil
}

func randomPublicID() (string, error) {
	value := make([]byte, 18)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "cred_" + base64.RawURLEncoding.EncodeToString(value), nil
}

func sanitizeMetadata(metadata map[string]any) map[string]any {
	result := make(map[string]any, len(metadata)+1)
	for key, value := range metadata {
		lower := strings.ToLower(strings.TrimSpace(key))
		if lower == "" || strings.Contains(lower, "password") || strings.Contains(lower, "cookie") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") {
			continue
		}
		result[key] = value
	}
	return result
}

func metadataBinding(metadata map[string]any) []byte {
	value, _ := metadata["binding_context"].(string)
	decoded, _ := base64.RawURLEncoding.DecodeString(value)
	return decoded
}

func clear(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func WrapIntegrityError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("credential integrity check failed: %w", err)
}
