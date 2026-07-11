package weibo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"pansou/credential"
	"pansou/model"
	coreplugin "pansou/plugin"
	"pansou/storage"
)

func newTenantTestPlugin() *WeiboPlugin {
	return &WeiboPlugin{BaseAsyncPlugin: coreplugin.NewBaseAsyncPlugin("weibo", 3)}
}

func tenantCandidate(t *testing.T, publicID string, secret tenantSecret, metadata map[string]any) (storage.PluginCredential, []byte) {
	t.Helper()
	document, err := json.Marshal(secret)
	if err != nil {
		t.Fatalf("Marshal tenant secret: %v", err)
	}
	return storage.PluginCredential{PublicID: publicID, PluginKey: "weibo", PublicMetadata: metadata}, document
}

type tenantCallbackRecorder struct {
	mu        sync.Mutex
	successes []string
	failures  []tenantFailureRecord
}

type tenantFailureRecord struct {
	publicID string
	status   string
	code     string
	cooldown *time.Time
}

func (r *tenantCallbackRecorder) success(_ context.Context, publicID string) {
	r.mu.Lock()
	r.successes = append(r.successes, publicID)
	r.mu.Unlock()
}

func (r *tenantCallbackRecorder) failure(_ context.Context, publicID, status, code string, cooldown *time.Time) {
	r.mu.Lock()
	r.failures = append(r.failures, tenantFailureRecord{publicID: publicID, status: status, code: code, cooldown: cooldown})
	r.mu.Unlock()
}

func (r *tenantCallbackRecorder) snapshot() ([]string, []tenantFailureRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.successes...), append([]tenantFailureRecord(nil), r.failures...)
}

func TestManagedInitializeDoesNotCreateLegacyStorage(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("CACHE_PATH", temp)
	previousStorageDir := StorageDir
	StorageDir = ""
	t.Cleanup(func() { StorageDir = previousStorageDir })

	instance := newTenantTestPlugin()
	instance.SetManagedCredentialMode(true)
	if err := instance.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if !instance.initialized || !instance.managedCredentials {
		t.Fatalf("managed instance state = initialized:%v managed:%v", instance.initialized, instance.managedCredentials)
	}
	if _, err := os.Stat(filepath.Join(temp, "weibo_users")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed initialization touched legacy storage: %v", err)
	}
	if StorageDir != "" {
		t.Fatalf("managed initialization changed StorageDir to %q", StorageDir)
	}
}

func TestParseLegacyCredentialPreservesSearchSettings(t *testing.T) {
	expiresAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	lastRefresh := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	legacy := User{
		Hash: "0123456789abcdef", Cookie: "SUB=secret-cookie; SCF=secondary", Status: "expired",
		UserIDs: []string{"200", "100", "200", " "}, ExpireAt: expiresAt, LastRefresh: lastRefresh,
	}
	document, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("Marshal legacy user: %v", err)
	}
	material, err := newTenantTestPlugin().ParseLegacyCredential(document)
	if err != nil {
		t.Fatalf("ParseLegacyCredential: %v", err)
	}
	if string(material.StableID) != legacy.Hash || material.ExpiresAt == nil || !material.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("material identity/expiry = %+v", material)
	}
	if material.PublicMetadata["legacy_status"] != "expired" {
		t.Fatalf("legacy status metadata = %+v", material.PublicMetadata)
	}
	if material.Status != storage.CredentialStatusExpired {
		t.Fatalf("credential status = %q, want expired", material.Status)
	}
	var secret tenantSecret
	if err := json.Unmarshal(material.Secret, &secret); err != nil {
		t.Fatalf("decode material secret: %v", err)
	}
	if secret.Cookie != legacy.Cookie || secret.Status != "expired" || !secret.LastRefresh.Equal(lastRefresh) {
		t.Fatalf("tenant secret = %+v", secret)
	}
	if got, want := secret.UserIDs, []string{"100", "200"}; !equalStrings(got, want) {
		t.Fatalf("user ids = %v, want %v", got, want)
	}
}

func TestParseLegacyCredentialRejectsMalformedOrEmptyCookie(t *testing.T) {
	instance := newTenantTestPlugin()
	if _, err := instance.ParseLegacyCredential([]byte(`{"cookie":`)); err == nil {
		t.Fatal("malformed legacy credential was accepted")
	}
	document, err := json.Marshal(User{Hash: "id", Status: "active"})
	if err != nil {
		t.Fatalf("Marshal legacy user: %v", err)
	}
	if _, err := instance.ParseLegacyCredential(document); err == nil {
		t.Fatal("empty-cookie legacy credential was accepted")
	}
}

func TestSearchCredentialLayerFansOutAndFailsOverWithinLayer(t *testing.T) {
	now := time.Now()
	bad, badSecret := tenantCandidate(t, "bad", tenantSecret{Cookie: "bad-cookie", UserIDs: []string{"100"}, Status: "active", LastRefresh: now}, nil)
	good, goodSecret := tenantCandidate(t, "good", tenantSecret{Cookie: "good-cookie", Status: "active", LastRefresh: now}, map[string]any{"user_ids": []any{"100", "200"}})
	secrets := map[string][]byte{"bad": badSecret, "good": goodSecret}

	instance := newTenantTestPlugin()
	var callsMu sync.Mutex
	var calls []string
	instance.credentialSearch = func(_ context.Context, userID, cookie, _ string) ([]model.SearchResult, error) {
		callsMu.Lock()
		calls = append(calls, userID+":"+cookie)
		callsMu.Unlock()
		if cookie == "bad-cookie" {
			return nil, newWeiboUpstreamError(401, "")
		}
		if userID == "100" {
			return []model.SearchResult{{UniqueID: "weibo-100-result"}}, nil
		}
		return nil, nil
	}
	recorder := &tenantCallbackRecorder{}
	results, succeeded, err := instance.SearchCredentialLayer(context.Background(), "movie", nil, []storage.PluginCredential{bad, good}, credential.Access{
		Open: func(candidate storage.PluginCredential) ([]byte, error) {
			return append([]byte(nil), secrets[candidate.PublicID]...), nil
		},
		Success: recorder.success,
		Failure: recorder.failure,
	})
	if err != nil || !succeeded {
		t.Fatalf("SearchCredentialLayer() = %d results, %v, %v", len(results), succeeded, err)
	}
	if len(results) != 1 || results[0].UniqueID != "weibo-100-result" {
		t.Fatalf("results = %+v", results)
	}
	callsMu.Lock()
	sort.Strings(calls)
	gotCalls := append([]string(nil), calls...)
	callsMu.Unlock()
	wantCalls := []string{"100:bad-cookie", "100:good-cookie", "200:good-cookie"}
	sort.Strings(wantCalls)
	if !equalStrings(gotCalls, wantCalls) {
		t.Fatalf("search calls = %v, want %v", gotCalls, wantCalls)
	}
	successes, failures := recorder.snapshot()
	if len(successes) != 2 || successes[0] != "good" || successes[1] != "good" {
		t.Fatalf("success callbacks = %v", successes)
	}
	if len(failures) != 1 || failures[0].publicID != "bad" || failures[0].status != storage.CredentialStatusInvalid || failures[0].code != "auth_failed" {
		t.Fatalf("failure callbacks = %+v", failures)
	}
	users := 0
	instance.users.Range(func(_, _ any) bool { users++; return true })
	if users != 0 {
		t.Fatalf("managed search populated legacy user cache with %d entries", users)
	}
}

func TestSearchCredentialLayerTreatsZeroResultsAsSuccess(t *testing.T) {
	now := time.Now()
	candidate, secret := tenantCandidate(t, "zero", tenantSecret{Cookie: "cookie", UserIDs: []string{"100"}, Status: "active", LastRefresh: now}, nil)
	instance := newTenantTestPlugin()
	instance.credentialSearch = func(context.Context, string, string, string) ([]model.SearchResult, error) { return nil, nil }
	recorder := &tenantCallbackRecorder{}
	results, succeeded, err := instance.SearchCredentialLayer(context.Background(), "nothing", nil, []storage.PluginCredential{candidate}, credential.Access{
		Open:    func(storage.PluginCredential) ([]byte, error) { return append([]byte(nil), secret...), nil },
		Success: recorder.success,
		Failure: recorder.failure,
	})
	if err != nil || !succeeded || len(results) != 0 {
		t.Fatalf("zero-result search = %v, %v, %v", results, succeeded, err)
	}
	successes, failures := recorder.snapshot()
	if len(successes) != 1 || successes[0] != "zero" || len(failures) != 0 {
		t.Fatalf("callbacks = successes %v failures %+v", successes, failures)
	}
}

func TestSearchCredentialLayerReportsRateLimitAndAuthFailure(t *testing.T) {
	now := time.Now()
	first, firstSecret := tenantCandidate(t, "rate", tenantSecret{Cookie: "rate-cookie", UserIDs: []string{"100"}, Status: "active", LastRefresh: now}, nil)
	second, secondSecret := tenantCandidate(t, "auth", tenantSecret{Cookie: "auth-cookie", UserIDs: []string{"100"}, Status: "active", LastRefresh: now}, nil)
	secrets := map[string][]byte{"rate": firstSecret, "auth": secondSecret}
	instance := newTenantTestPlugin()
	instance.credentialSearch = func(_ context.Context, _ string, cookie, _ string) ([]model.SearchResult, error) {
		if cookie == "rate-cookie" {
			return nil, newWeiboUpstreamError(429, "")
		}
		return nil, newWeiboUpstreamError(403, "")
	}
	recorder := &tenantCallbackRecorder{}
	_, succeeded, err := instance.SearchCredentialLayer(context.Background(), "movie", nil, []storage.PluginCredential{first, second}, credential.Access{
		Open: func(candidate storage.PluginCredential) ([]byte, error) {
			return append([]byte(nil), secrets[candidate.PublicID]...), nil
		},
		Success: recorder.success, Failure: recorder.failure,
	})
	if succeeded || err == nil {
		t.Fatalf("all-failed search = succeeded %v, error %v", succeeded, err)
	}
	_, failures := recorder.snapshot()
	if len(failures) != 2 {
		t.Fatalf("failures = %+v", failures)
	}
	byID := make(map[string]tenantFailureRecord, len(failures))
	for _, failure := range failures {
		byID[failure.publicID] = failure
	}
	if byID["rate"].code != "rate_limited" || byID["rate"].cooldown == nil || byID["rate"].status != storage.CredentialStatusActive {
		t.Fatalf("rate failure = %+v", byID["rate"])
	}
	if byID["auth"].code != "auth_failed" || byID["auth"].cooldown != nil || byID["auth"].status != storage.CredentialStatusInvalid {
		t.Fatalf("auth failure = %+v", byID["auth"])
	}
}

func TestWeiboQRAdapterRejectsInvalidFlowState(t *testing.T) {
	instance := newTenantTestPlugin()
	if _, err := instance.PollQRLogin(context.Background(), struct{}{}); err == nil {
		t.Fatal("invalid QR flow state was accepted")
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
