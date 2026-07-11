package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pansou/storage"
)

func newUsersPostgresStore(t *testing.T) *storage.Store {
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
	schema := fmt.Sprintf("pansou_users_test_%d", time.Now().UnixNano())
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
	return store
}

func TestPostgresUsersAPIKeysAndUsage(t *testing.T) {
	store := newUsersPostgresStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	admin, key, err := store.CreateUserWithAPIKey(ctx, storage.CreateUserInput{
		Username: "Admin", PasswordHash: "bcrypt-admin", Role: storage.UserRoleAdmin,
	}, storage.APIKeyInput{KeyPrefix: "ps_admin", KeyHash: "hash-admin", CreatedAt: now})
	if err != nil {
		t.Fatalf("CreateUserWithAPIKey(admin): %v", err)
	}
	if count, err := store.CountUsers(ctx); err != nil || count != 1 {
		t.Fatalf("CountUsers() = %d, %v", count, err)
	}
	if got, err := store.GetUserByNormalizedUsername(ctx, " ADMIN "); err != nil || got.ID != admin.ID {
		t.Fatalf("GetUserByNormalizedUsername() = %+v, %v", got, err)
	}
	if gotUser, gotKey, err := store.GetUserByAPIKeyHash(ctx, "hash-admin"); err != nil || gotUser.ID != admin.ID || gotKey.ID != key.ID {
		t.Fatalf("GetUserByAPIKeyHash() = %+v, %+v, %v", gotUser, gotKey, err)
	}
	if err := store.UpdateAPIKeyLastUsed(ctx, admin.ID, now.Add(time.Minute)); err != nil {
		t.Fatalf("UpdateAPIKeyLastUsed(): %v", err)
	}
	reset, err := store.ResetAPIKey(ctx, admin.ID, storage.APIKeyInput{KeyPrefix: "ps_reset", KeyHash: "hash-reset"}, now.Add(2*time.Minute))
	if err != nil || reset.KeyHash != "hash-reset" {
		t.Fatalf("ResetAPIKey() = %+v, %v", reset, err)
	}
	if _, _, err := store.GetUserByAPIKeyHash(ctx, "hash-admin"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("old API key lookup error = %v", err)
	}
	if err := store.RevokeAPIKey(ctx, admin.ID, now.Add(3*time.Minute)); err != nil {
		t.Fatalf("RevokeAPIKey(): %v", err)
	}
	if _, _, err := store.GetUserByAPIKeyHash(ctx, "hash-reset"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("revoked API key lookup error = %v", err)
	}

	disabled := false
	if _, err := store.UpdateUser(ctx, admin.ID, storage.UpdateUserInput{Enabled: &disabled}); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("disable last admin error = %v", err)
	}
	if _, err := store.SoftDeleteUser(ctx, admin.ID, now); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("delete last admin error = %v", err)
	}
	secondAdmin, err := store.CreateUser(ctx, storage.CreateUserInput{Username: "Second Admin", PasswordHash: "bcrypt-second", Role: storage.UserRoleAdmin})
	if err != nil {
		t.Fatalf("CreateUser(second admin): %v", err)
	}
	if _, err := store.UpdateUser(ctx, admin.ID, storage.UpdateUserInput{Enabled: &disabled}); err != nil {
		t.Fatalf("disable admin with replacement: %v", err)
	}
	beforeVersion := secondAdmin.AuthVersion
	secondAdmin, err = store.SetUserPassword(ctx, secondAdmin.ID, "bcrypt-new", false, now.Add(4*time.Minute))
	if err != nil || secondAdmin.AuthVersion != beforeVersion+1 || secondAdmin.MustChangePassword {
		t.Fatalf("SetUserPassword() = %+v, %v", secondAdmin, err)
	}
	if err := store.UpdateUserLastLogin(ctx, secondAdmin.ID, now.Add(5*time.Minute)); err != nil {
		t.Fatalf("UpdateUserLastLogin(): %v", err)
	}

	var wait sync.WaitGroup
	results := make(chan error, 2)
	for _, username := range []string{"Concurrent", " concurrent "} {
		username := username
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := store.CreateUser(ctx, storage.CreateUserInput{Username: username, PasswordHash: "bcrypt-value"})
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, storage.ErrConflict):
			conflicts++
		default:
			t.Fatalf("concurrent CreateUser error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent CreateUser successes/conflicts = %d/%d", successes, conflicts)
	}
	keyRaceUser, err := store.CreateUser(ctx, storage.CreateUserInput{Username: "Key Race", PasswordHash: "bcrypt-key-race"})
	if err != nil {
		t.Fatalf("CreateUser(key race): %v", err)
	}
	results = make(chan error, 2)
	for index, hash := range []string{"hash-race-a", "hash-race-b"} {
		index, hash := index, hash
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := store.CreateAPIKey(ctx, keyRaceUser.ID, storage.APIKeyInput{
				KeyPrefix: fmt.Sprintf("ps_race_%d", index), KeyHash: hash,
			})
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	successes, conflicts = 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, storage.ErrConflict):
			conflicts++
		default:
			t.Fatalf("concurrent CreateAPIKey error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent CreateAPIKey successes/conflicts = %d/%d", successes, conflicts)
	}
	deletedUser, err := store.SoftDeleteUser(ctx, keyRaceUser.ID, now.Add(6*time.Minute))
	if err != nil || deletedUser.DeletedAt == nil || deletedUser.Enabled {
		t.Fatalf("SoftDeleteUser() = %+v, %v", deletedUser, err)
	}
	if got, err := store.GetUserByID(ctx, keyRaceUser.ID); err != nil || got.DeletedAt == nil {
		t.Fatalf("GetUserByID(deleted) = %+v, %v", got, err)
	}

	old := now.Add(-31 * 24 * time.Hour)
	cutoff := now.Add(-30 * 24 * time.Hour)
	logs := []storage.APIRequestLogInput{
		{RequestID: "old-1", UserID: secondAdmin.ID, AuthType: storage.AuthTypeWeb, Method: "GET", Endpoint: "/api/search", Keyword: "one", StatusCode: 200, DurationMS: 10, ResultCount: 2, CacheStatus: "hit", SourceIP: "127.0.0.1", CreatedAt: old},
		{RequestID: "old-2", UserID: secondAdmin.ID, AuthType: storage.AuthTypeAPIKey, Method: "GET", Endpoint: "/api/search", Keyword: "two", StatusCode: 429, DurationMS: 1, ErrorCode: "rate_limited", SourceIP: "127.0.0.2", CreatedAt: old.Add(time.Minute)},
		{RequestID: "old-3", UserID: secondAdmin.ID, AuthType: storage.AuthTypeWeb, Method: "POST", Endpoint: "/api/search", Keyword: "three", StatusCode: 500, DurationMS: 90, ErrorCode: "search_failed", SourceIP: "127.0.0.3", CreatedAt: old.Add(2 * time.Minute)},
		{RequestID: "boundary", UserID: secondAdmin.ID, AuthType: storage.AuthTypeWeb, Method: "GET", Endpoint: "/api/search", StatusCode: 200, DurationMS: 20, CreatedAt: cutoff},
		{RequestID: "admin-log", UserID: admin.ID, AuthType: storage.AuthTypeWeb, Method: "GET", Endpoint: "/api/search", StatusCode: 200, DurationMS: 30, ResultCount: 1, CreatedAt: now.Add(-time.Hour)},
	}
	if inserted, err := store.InsertAPIRequestLogs(ctx, logs); err != nil || inserted != int64(len(logs)) {
		t.Fatalf("InsertAPIRequestLogs() = %d, %v", inserted, err)
	}
	userPage, err := store.ListUserAPIRequestLogs(ctx, secondAdmin.ID, storage.APIRequestLogFilter{Page: 1, PageSize: 20})
	if err != nil || userPage.Total != 4 {
		t.Fatalf("ListUserAPIRequestLogs() total=%d err=%v", userPage.Total, err)
	}
	adminPage, err := store.ListAdminAPIRequestLogs(ctx, storage.APIRequestLogFilter{Page: 1, PageSize: 20})
	if err != nil || adminPage.Total != 5 {
		t.Fatalf("ListAdminAPIRequestLogs() total=%d err=%v", adminPage.Total, err)
	}
	overview, err := store.UserUsageOverview(ctx, secondAdmin.ID, storage.UsageStatsFilter{From: old.Add(-time.Hour), To: now.Add(time.Hour)})
	if err != nil || overview.TotalRequests != 4 || overview.RateLimitedRequests != 1 || overview.ActiveUsers != 1 {
		t.Fatalf("UserUsageOverview() = %+v, %v", overview, err)
	}
	adminOverview, err := store.AdminUsageOverview(ctx, storage.UsageStatsFilter{From: old.Add(-time.Hour), To: now.Add(time.Hour)})
	if err != nil || adminOverview.TotalRequests != 5 || adminOverview.ActiveUsers != 2 {
		t.Fatalf("AdminUsageOverview() = %+v, %v", adminOverview, err)
	}
	trends, err := store.UserUsageTrends(ctx, secondAdmin.ID, storage.UsageStatsFilter{From: old, To: now.Add(time.Hour), Bucket: storage.UsageBucketDay})
	if err != nil || len(trends) == 0 {
		t.Fatalf("UserUsageTrends() = %+v, %v", trends, err)
	}

	if deleted, err := store.DeleteAPIRequestLogsBefore(ctx, cutoff, 2); err != nil || deleted != 2 {
		t.Fatalf("DeleteAPIRequestLogsBefore(first) = %d, %v", deleted, err)
	}
	if deleted, err := store.DeleteAPIRequestLogsBefore(ctx, cutoff, 2); err != nil || deleted != 1 {
		t.Fatalf("DeleteAPIRequestLogsBefore(second) = %d, %v", deleted, err)
	}
	if deleted, err := store.DeleteAPIRequestLogsBefore(ctx, cutoff, 2); err != nil || deleted != 0 {
		t.Fatalf("DeleteAPIRequestLogsBefore(third) = %d, %v", deleted, err)
	}
	remaining, err := store.ListAdminAPIRequestLogs(ctx, storage.APIRequestLogFilter{Page: 1, PageSize: 20})
	if err != nil || remaining.Total != 2 {
		t.Fatalf("remaining logs total=%d err=%v", remaining.Total, err)
	}
}
