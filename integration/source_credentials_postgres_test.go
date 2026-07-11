package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pansou/storage"
)

func newSourceCredentialStore(t *testing.T) (*storage.Store, *pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = os.Getenv("PANSOU_TEST_DATABASE_URL")
	}
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL or PANSOU_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	schema := fmt.Sprintf("pansou_sources_test_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		admin.Close()
		t.Fatalf("create schema: %v", err)
	}
	store, err := storage.Open(ctx, databaseURL, storage.WithPoolConfig(func(config *pgxpool.Config) {
		config.ConnConfig.RuntimeParams["search_path"] = schema
		config.MaxConns = 8
	}))
	if err != nil {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE")
		admin.Close()
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		store.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupCtx, "DROP SCHEMA "+identifier+" CASCADE")
		admin.Close()
	})
	return store, storePoolForTest(t, databaseURL, schema)
}

func storePoolForTest(t *testing.T, databaseURL, schema string) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse PostgreSQL URL: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatalf("open inspection pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestPostgresSourceConfigCredentialsAndDataMigration(t *testing.T) {
	store, pool := newSourceCredentialStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	admin, err := store.CreateUser(ctx, storage.CreateUserInput{
		Username: "Source Admin", PasswordHash: "bcrypt-admin", Role: storage.UserRoleAdmin,
	})
	if err != nil {
		t.Fatalf("CreateUser(admin): %v", err)
	}
	user, err := store.CreateUser(ctx, storage.CreateUserInput{Username: "Source User", PasswordHash: "bcrypt-user"})
	if err != nil {
		t.Fatalf("CreateUser(user): %v", err)
	}

	seed, created, err := store.InitializeSearchSourceConfig(ctx, storage.InitializeSearchSourceConfigInput{
		SchemaVersion: 1,
		Config:        json.RawMessage(`{"async_plugin_enabled":true,"channels":["one"],"plugins":{"qqpd":{"enabled":true}}}`),
	})
	if err != nil || !created || seed.Version != 1 {
		t.Fatalf("InitializeSearchSourceConfig() = %+v, %v, %v", seed, created, err)
	}
	secondSeed, created, err := store.InitializeSearchSourceConfig(ctx, storage.InitializeSearchSourceConfigInput{
		SchemaVersion: 1, Config: json.RawMessage(`{"async_plugin_enabled":false}`),
	})
	if err != nil || created || secondSeed.Version != seed.Version || !bytes.Equal(secondSeed.Config, seed.Config) {
		t.Fatalf("second InitializeSearchSourceConfig() = %+v, %v, %v", secondSeed, created, err)
	}
	updated, err := store.CompareAndSwapSearchSourceConfig(ctx, storage.UpdateSearchSourceConfigInput{
		ExpectedVersion: seed.Version, SchemaVersion: 1, UpdatedBy: &admin.ID,
		Config:        json.RawMessage(`{"async_plugin_enabled":true,"channels":["two"]}`),
		ChangeSummary: map[string]any{"channels_changed": 1},
	})
	if err != nil || updated.Version != 2 {
		t.Fatalf("CompareAndSwapSearchSourceConfig() = %+v, %v", updated, err)
	}
	if _, err := store.CompareAndSwapSearchSourceConfig(ctx, storage.UpdateSearchSourceConfigInput{
		ExpectedVersion: seed.Version, SchemaVersion: 1, UpdatedBy: &admin.ID,
		Config: json.RawMessage(`{"async_plugin_enabled":false}`),
	}); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("stale CompareAndSwap error = %v", err)
	}
	if _, err := store.RecordSearchSourceConfigEvent(ctx, storage.CreateSearchSourceConfigEventInput{
		ActorUserID: &admin.ID, BaseVersion: updated.Version, Result: storage.SourceConfigEventFailed,
		ErrorCode: "source_init_failed", ChangeSummary: map[string]any{"plugin": "qqpd"},
	}); err != nil {
		t.Fatalf("RecordSearchSourceConfigEvent(): %v", err)
	}
	events, err := store.ListSearchSourceConfigEvents(ctx, storage.SearchSourceConfigEventFilter{Page: 1, PageSize: 10})
	if err != nil || events.Total != 3 || events.Items[0].Result != storage.SourceConfigEventFailed {
		t.Fatalf("ListSearchSourceConfigEvents() = %+v, %v", events, err)
	}

	private := testCredentialInput(user.ID, admin.ID, "private-one", storage.CredentialScopeUserPrivate, "fp-private", now)
	createdPrivate, err := store.CreatePluginCredential(ctx, private)
	if err != nil {
		t.Fatalf("CreatePluginCredential(private): %v", err)
	}
	shared := testCredentialInput(0, admin.ID, "shared-one", storage.CredentialScopePublicShared, "fp-shared", now)
	createdShared, err := store.CreatePluginCredential(ctx, shared)
	if err != nil {
		t.Fatalf("CreatePluginCredential(shared): %v", err)
	}
	if _, err := store.CreatePluginCredential(ctx, private); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("duplicate credential error = %v", err)
	}
	candidates, err := store.ListUsableUserCredentialCandidates(ctx, user.ID, "qqpd", now.Add(time.Minute), 20)
	if err != nil || len(candidates) != 2 || candidates[0].Scope != storage.CredentialScopeUserPrivate {
		t.Fatalf("ListUsableUserCredentialCandidates() = %+v, %v", candidates, err)
	}
	if err := store.SetCredentialAdminSuspension(ctx, storage.CredentialSuspensionInput{
		PublicID: createdPrivate.PublicID, Suspended: true, AdminUserID: admin.ID, At: now.Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("SetCredentialAdminSuspension(): %v", err)
	}
	candidates, err = store.ListUsableUserCredentialCandidates(ctx, user.ID, "qqpd", now.Add(3*time.Minute), 20)
	if err != nil || len(candidates) != 1 || candidates[0].PublicID != createdShared.PublicID {
		t.Fatalf("suspended user candidates = %+v, %v", candidates, err)
	}
	if err := store.RecordPluginCredentialFailure(ctx, storage.CredentialFailureInput{
		PublicID: createdShared.PublicID, ErrorCode: "rate_limited", FailedAt: now.Add(4 * time.Minute),
		CooldownUntil: pointerTime(now.Add(time.Hour)),
	}); err != nil {
		t.Fatalf("RecordPluginCredentialFailure(): %v", err)
	}
	if err := store.RecordPluginCredentialSuccess(ctx, storage.CredentialSuccessInput{
		PublicID: createdShared.PublicID, SucceededAt: now.Add(5 * time.Minute),
	}); err != nil {
		t.Fatalf("RecordPluginCredentialSuccess(): %v", err)
	}

	plaintext := "plain-secret-password-cookie-token"
	var rawText string
	if err := pool.QueryRow(ctx, `SELECT row_to_json(c)::text FROM plugin_credentials c WHERE public_id=$1`, createdPrivate.PublicID).Scan(&rawText); err != nil {
		t.Fatalf("inspect stored credential: %v", err)
	}
	if bytes.Contains([]byte(rawText), []byte(plaintext)) {
		t.Fatalf("database row leaked plaintext: %s", rawText)
	}

	migrated := testCredentialInput(0, admin.ID, "migrated-one", storage.CredentialScopeAdminPrivate, "fp-migrated", now)
	result, err := store.ImportPluginCredentialsAndCompleteMigration(ctx, storage.ImportPluginCredentialsInput{
		MigrationKey: "legacy_accounts_v1", Credentials: []storage.CreatePluginCredentialInput{migrated},
		Summary: map[string]any{"imported": 1}, CompletedAt: now.Add(6 * time.Minute),
	})
	if err != nil || !result.Applied || result.Imported != 1 {
		t.Fatalf("ImportPluginCredentialsAndCompleteMigration() = %+v, %v", result, err)
	}
	result, err = store.ImportPluginCredentialsAndCompleteMigration(ctx, storage.ImportPluginCredentialsInput{
		MigrationKey: "legacy_accounts_v1", Credentials: []storage.CreatePluginCredentialInput{migrated},
	})
	if err != nil || result.Applied || result.Imported != 0 {
		t.Fatalf("idempotent migration = %+v, %v", result, err)
	}
	if _, err := store.SoftDeleteUser(ctx, user.ID, now.Add(7*time.Minute)); err != nil {
		t.Fatalf("SoftDeleteUser(): %v", err)
	}
	if _, err := store.GetPluginCredentialByPublicID(ctx, createdPrivate.PublicID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("deleted user's private credential error = %v", err)
	}
	if _, err := store.GetPluginCredentialByPublicID(ctx, createdShared.PublicID); err != nil {
		t.Fatalf("shared credential removed with user: %v", err)
	}

	raceUser, err := store.CreateUser(ctx, storage.CreateUserInput{Username: "Delete Race", PasswordHash: "bcrypt-race"})
	if err != nil {
		t.Fatalf("CreateUser(delete race): %v", err)
	}
	raceCredential := testCredentialInput(raceUser.ID, admin.ID, "delete-race", storage.CredentialScopeUserPrivate, "fp-delete-race", now)
	results := make(chan error, 2)
	go func() {
		_, createErr := store.CreatePluginCredential(ctx, raceCredential)
		results <- createErr
	}()
	go func() {
		_, deleteErr := store.SoftDeleteUser(ctx, raceUser.ID, now.Add(8*time.Minute))
		results <- deleteErr
	}()
	for range 2 {
		operationErr := <-results
		if operationErr != nil && !errors.Is(operationErr, storage.ErrConflict) && !errors.Is(operationErr, storage.ErrNotFound) {
			t.Fatalf("credential/delete race error = %v", operationErr)
		}
	}
	raceStoredUser, err := store.GetUserByID(ctx, raceUser.ID)
	if err != nil || raceStoredUser.DeletedAt == nil {
		t.Fatalf("delete race user was not soft deleted: %+v, %v", raceStoredUser, err)
	}
	if _, err := store.GetPluginCredentialByPublicID(ctx, raceCredential.PublicID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("credential survived owner soft delete race: %v", err)
	}
}

func testCredentialInput(ownerID, creatorID int64, publicID, scope, fingerprint string, at time.Time) storage.CreatePluginCredentialInput {
	var owner *int64
	if ownerID > 0 {
		owner = &ownerID
	}
	bindingHash := sha256.Sum256([]byte("binding-" + publicID))
	credentialHash := sha256.Sum256([]byte(fingerprint))
	return storage.CreatePluginCredentialInput{
		PublicID: publicID, PluginKey: "qqpd", Scope: scope, OwnerUserID: owner,
		CreatedByUserID: &creatorID, DisplayName: "masked-account",
		PublicMetadata: map[string]any{"masked": "u***r"}, SecretSchemaVersion: 1,
		BindingFingerprint: bindingHash[:], Ciphertext: []byte("ciphertext-tag-" + publicID),
		Nonce: []byte("123456789012"), KeyVersion: 1, CredentialFingerprint: credentialHash[:],
		OwnerEnabled: true, Status: storage.CredentialStatusActive, CreatedAt: at,
	}
}

func pointerTime(value time.Time) *time.Time { return &value }
