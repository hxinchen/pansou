package storage

import (
	"context"
	"testing"
	"time"
)

func TestPluginCredentialHealthPersistence(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	store := newPostgresTestStore(t, now)
	ctx := context.Background()
	var migrated bool
	if err := store.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM schema_migrations WHERE version=21)`).Scan(&migrated); err != nil || !migrated {
		t.Fatalf("credential health migration: migrated=%v err=%v", migrated, err)
	}
	created, err := store.CreatePluginCredential(ctx, CreatePluginCredentialInput{
		PublicID: "health-test", PluginKey: "gying", Scope: CredentialScopeAdminPrivate,
		DisplayName: "Health test", PublicMetadata: map[string]any{}, SecretSchemaVersion: 1,
		BindingFingerprint: make([]byte, 32), Ciphertext: make([]byte, 16), Nonce: make([]byte, 12),
		KeyVersion: 1, CredentialFingerprint: make([]byte, 32), OwnerEnabled: true,
		Status: CredentialStatusActive, CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.LastHealthStatus != CredentialHealthUnknown || created.LastHealthCheckAt != nil {
		t.Fatalf("initial health = %#v", created)
	}
	candidates, err := store.ListPluginCredentialHealthCandidates(ctx, []string{"gying"}, now.Add(-6*time.Hour), 10)
	if err != nil || len(candidates) != 1 || candidates[0].PublicID != created.PublicID {
		t.Fatalf("health candidates = %#v err=%v", candidates, err)
	}
	checkedAt := now.Add(time.Minute)
	if err := store.RecordPluginCredentialHealth(ctx, CredentialHealthInput{
		PublicID: created.PublicID, HealthStatus: CredentialHealthInvalid,
		CredentialStatus: CredentialStatusInvalid, ErrorCode: "auth_failed", CheckedAt: checkedAt,
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := store.GetPluginCredentialByPublicID(ctx, created.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != CredentialStatusInvalid || updated.LastHealthStatus != CredentialHealthInvalid ||
		updated.LastHealthCheckAt == nil || !updated.LastHealthCheckAt.Equal(checkedAt) || updated.LastHealthErrorCode != "auth_failed" {
		t.Fatalf("updated health = %#v", updated)
	}
}
