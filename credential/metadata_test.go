package credential

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"testing"
	"time"

	"pansou/storage"
)

func TestNormalizePublicMetadataSelectors(t *testing.T) {
	tests := []struct {
		name      string
		pluginKey string
		field     string
		value     any
		want      []string
	}{
		{name: "qqpd delimited string", pluginKey: " QQPD ", field: "channels", value: " alpha，beta; alpha\n gamma；beta ", want: []string{"alpha", "beta", "gamma"}},
		{name: "weibo mixed array", pluginKey: "weibo", field: "user_ids", value: []any{"one,two", "two", " three；four "}, want: []string{"one", "two", "three", "four"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := map[string]any{test.field: test.value, "keep": "value"}
			got := NormalizePublicMetadata(test.pluginKey, input)
			if !reflect.DeepEqual(got[test.field], test.want) {
				t.Fatalf("%s = %#v, want %#v", test.field, got[test.field], test.want)
			}
			if got["keep"] != "value" {
				t.Fatalf("unrelated metadata changed: %#v", got)
			}
			if reflect.DeepEqual(input[test.field], test.want) {
				t.Fatal("input map was mutated")
			}
		})
	}
}

func TestUpdateAdminMetadataPreservesEnvelopeSemantics(t *testing.T) {
	for _, scope := range []string{storage.CredentialScopeAdminPrivate, storage.CredentialScopePublicShared} {
		t.Run(scope, func(t *testing.T) {
			repository := &metadataRepository{}
			cipher, err := NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
			if err != nil {
				t.Fatal(err)
			}
			service := NewService(repository, cipher)
			expiresAt := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Microsecond)
			created, err := service.Create(context.Background(), CreateInput{
				PluginKey: "qqpd", Scope: scope, DisplayName: "Original",
				PublicMetadata: map[string]any{"keep": "value", "channels": "old"},
				Secret:         []byte("secret payload"), StableID: []byte("stable account"),
				ConfigBinding: []byte("https://example.test"), Status: storage.CredentialStatusInvalid, ExpiresAt: &expiresAt,
			})
			if err != nil {
				t.Fatal(err)
			}
			originalFingerprint := append([]byte(nil), created.CredentialFingerprint...)
			originalNonce := append([]byte(nil), created.Nonce...)

			updated, err := service.UpdateAdminMetadata(context.Background(), created.PublicID, " Renamed ", map[string]any{
				"channels": "alpha，beta;alpha\ngamma", "cookie": "must-not-persist",
			})
			if err != nil {
				t.Fatal(err)
			}
			if updated.DisplayName != "Renamed" || updated.Scope != scope {
				t.Fatalf("updated identity = %#v", updated)
			}
			if updated.Status != storage.CredentialStatusInvalid || updated.ExpiresAt == nil || !updated.ExpiresAt.Equal(expiresAt) {
				t.Fatalf("status/expiry changed: status=%q expires=%v", updated.Status, updated.ExpiresAt)
			}
			if !reflect.DeepEqual(updated.CredentialFingerprint, originalFingerprint) {
				t.Fatal("credential fingerprint changed")
			}
			if reflect.DeepEqual(updated.Nonce, originalNonce) {
				t.Fatal("metadata update did not produce a fresh nonce")
			}
			if updated.PublicMetadata["keep"] != "value" || updated.PublicMetadata["binding_context"] == nil {
				t.Fatalf("existing metadata was not preserved: %#v", updated.PublicMetadata)
			}
			if _, exists := updated.PublicMetadata["cookie"]; exists {
				t.Fatal("secret-like metadata was persisted")
			}
			if want := []string{"alpha", "beta", "gamma"}; !reflect.DeepEqual(updated.PublicMetadata["channels"], want) {
				t.Fatalf("channels = %#v, want %#v", updated.PublicMetadata["channels"], want)
			}
			plaintext, err := service.OpenStored(updated)
			if err != nil || string(plaintext) != "secret payload" {
				t.Fatalf("open updated credential = %q, %v", plaintext, err)
			}
			if repository.replaceInput.ExpectedRevision != created.Revision {
				t.Fatalf("expected revision = %d, want %d", repository.replaceInput.ExpectedRevision, created.Revision)
			}
		})
	}
}

func TestUpdateAdminMetadataRejectsUserCredential(t *testing.T) {
	ownerID := int64(42)
	repository := &metadataRepository{current: storage.PluginCredential{PublicID: "cred_user", Scope: storage.CredentialScopeUserPrivate, OwnerUserID: &ownerID}}
	cipher, err := NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewService(repository, cipher).UpdateAdminMetadata(context.Background(), "cred_user", "name", nil)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("error = %v, want storage.ErrNotFound", err)
	}
	if repository.replaceCalls != 0 {
		t.Fatalf("replace calls = %d, want 0", repository.replaceCalls)
	}
}

type metadataRepository struct {
	current      storage.PluginCredential
	replaceInput storage.ReplaceCredentialEnvelopeInput
	replaceCalls int
}

func (r *metadataRepository) CreatePluginCredential(_ context.Context, input storage.CreatePluginCredentialInput) (storage.PluginCredential, error) {
	r.current = storage.PluginCredential{
		ID: 1, PublicID: input.PublicID, PluginKey: input.PluginKey, Scope: input.Scope,
		OwnerUserID: input.OwnerUserID, CreatedByUserID: input.CreatedByUserID, DisplayName: input.DisplayName,
		PublicMetadata: input.PublicMetadata, SecretSchemaVersion: input.SecretSchemaVersion,
		BindingFingerprint: input.BindingFingerprint, Ciphertext: input.Ciphertext, Nonce: input.Nonce,
		KeyVersion: input.KeyVersion, CredentialFingerprint: input.CredentialFingerprint,
		Revision: 1, OwnerEnabled: input.OwnerEnabled, Status: input.Status, ExpiresAt: input.ExpiresAt,
		CreatedAt: input.CreatedAt, UpdatedAt: input.CreatedAt,
	}
	return r.current, nil
}

func (r *metadataRepository) GetPluginCredentialByPublicID(_ context.Context, publicID string) (storage.PluginCredential, error) {
	if r.current.PublicID != publicID {
		return storage.PluginCredential{}, storage.ErrNotFound
	}
	return r.current, nil
}

func (r *metadataRepository) ReplacePluginCredentialEnvelopeCAS(_ context.Context, input storage.ReplaceCredentialEnvelopeInput) (storage.PluginCredential, error) {
	r.replaceCalls++
	r.replaceInput = input
	if input.ExpectedRevision != r.current.Revision {
		return storage.PluginCredential{}, storage.ErrConflict
	}
	r.current.Scope = input.Scope
	r.current.DisplayName = input.DisplayName
	r.current.PublicMetadata = input.PublicMetadata
	r.current.SecretSchemaVersion = input.SecretSchemaVersion
	r.current.BindingFingerprint = input.BindingFingerprint
	r.current.Ciphertext = input.Ciphertext
	r.current.Nonce = input.Nonce
	r.current.KeyVersion = input.KeyVersion
	r.current.CredentialFingerprint = input.CredentialFingerprint
	r.current.Status = input.Status
	r.current.ExpiresAt = input.ExpiresAt
	r.current.UpdatedAt = input.UpdatedAt
	r.current.Revision++
	return r.current, nil
}

func (*metadataRepository) ListUserPluginCredentials(context.Context, int64, storage.PluginCredentialFilter) (storage.PluginCredentialPage, error) {
	return storage.PluginCredentialPage{}, nil
}
func (*metadataRepository) ListAdminPluginCredentials(context.Context, storage.PluginCredentialFilter) (storage.PluginCredentialPage, error) {
	return storage.PluginCredentialPage{}, nil
}
func (*metadataRepository) ListUsableUserPrivateCredentialCandidates(context.Context, int64, string, time.Time, int) ([]storage.PluginCredential, error) {
	return nil, nil
}
func (*metadataRepository) ListUsableAdminPrivateCredentialCandidates(context.Context, string, time.Time, int) ([]storage.PluginCredential, error) {
	return nil, nil
}
func (*metadataRepository) ListUsableSharedCredentialCandidates(context.Context, string, time.Time, int) ([]storage.PluginCredential, error) {
	return nil, nil
}
func (*metadataRepository) ReplaceUserPluginCredentialEnvelopeCAS(context.Context, int64, storage.ReplaceCredentialEnvelopeInput) (storage.PluginCredential, error) {
	return storage.PluginCredential{}, nil
}
func (*metadataRepository) SetAdminCredentialEnabled(context.Context, string, bool) error { return nil }
func (*metadataRepository) SetCredentialOwnerEnabled(context.Context, string, int64, bool) error {
	return nil
}
func (*metadataRepository) SetCredentialAdminSuspension(context.Context, storage.CredentialSuspensionInput) error {
	return nil
}
func (*metadataRepository) DeleteUserPluginCredential(context.Context, string, int64) error {
	return nil
}
func (*metadataRepository) DeleteAdminPluginCredential(context.Context, string) error { return nil }
func (*metadataRepository) RecordPluginCredentialSuccess(context.Context, storage.CredentialSuccessInput) error {
	return nil
}
func (*metadataRepository) RecordPluginCredentialFailure(context.Context, storage.CredentialFailureInput) error {
	return nil
}
