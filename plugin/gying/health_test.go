package gying

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pansou/credential"
	"pansou/storage"

	cloudscraper "github.com/Advik-B/cloudscraper/lib"
)

func TestFetchSearchSuggestionsTreatsEmptyBodyAsExpiredSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	scraper, err := cloudscraper.New(cloudscraper.WithSessionConfig(false, time.Hour, 0))
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&GyingPlugin{baseURL: server.URL}).fetchSearchSuggestionsContext(context.Background(), "电影", scraper)
	if !errors.Is(err, errGyingAuthenticationRequired) {
		t.Fatalf("error = %v, want authentication required", err)
	}
}

func TestCredentialHealthReloginsAndRefreshesCookie(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/user/login" && r.Method == http.MethodGet:
			http.SetCookie(w, &http.Cookie{Name: "initial", Value: "1", Path: "/"})
			_, _ = w.Write([]byte("login"))
		case r.URL.Path == "/user/login" && r.Method == http.MethodPost:
			http.SetCookie(w, &http.Cookie{Name: "auth", Value: "valid", Path: "/"})
			_, _ = w.Write([]byte(`{"code":200}`))
		case r.URL.Path == "/mv/yX3p":
			_, _ = w.Write([]byte("warmup"))
		case r.URL.Path == "/res/search_suggest":
			cookie, _ := r.Cookie("auth")
			if cookie == nil || cookie.Value != "valid" {
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	p := &GyingPlugin{baseURL: server.URL}
	secret, _ := json.Marshal(tenantSecret{Username: "user", Password: "pass", Cookie: "auth=expired"})
	var refreshed credential.LoginMaterial
	result, err := p.CheckCredentialHealth(context.Background(), storage.PluginCredential{PublicID: "gying-1", PluginKey: "gying", Revision: 1}, credential.Access{
		Open: func(storage.PluginCredential) ([]byte, error) { return append([]byte(nil), secret...), nil },
		Refresh: func(_ context.Context, _ string, material credential.LoginMaterial) error {
			refreshed = material
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.HealthStatus != storage.CredentialHealthHealthy || result.CredentialStatus != storage.CredentialStatusActive {
		t.Fatalf("health result = %#v", result)
	}
	if len(refreshed.Secret) == 0 {
		t.Fatal("refreshed credential was not persisted")
	}
	var refreshedSecret tenantSecret
	if err := json.Unmarshal(refreshed.Secret, &refreshedSecret); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(refreshedSecret.Cookie, "auth=valid") {
		t.Fatalf("refreshed cookie = %q", refreshedSecret.Cookie)
	}
}

func TestCredentialHealthMarksRejectedPasswordInvalid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/res/search_suggest":
			return
		case r.URL.Path == "/user/login" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte("login"))
		case r.URL.Path == "/user/login" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"code":0,"msg":"密码错误"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	p := &GyingPlugin{baseURL: server.URL}
	secret, _ := json.Marshal(tenantSecret{Username: "user", Password: "wrong", Cookie: "auth=expired"})
	result, err := p.CheckCredentialHealth(context.Background(), storage.PluginCredential{PublicID: "gying-2", PluginKey: "gying", Revision: 1}, credential.Access{
		Open: func(storage.PluginCredential) ([]byte, error) { return append([]byte(nil), secret...), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.HealthStatus != storage.CredentialHealthInvalid || result.CredentialStatus != storage.CredentialStatusInvalid || result.ErrorCode != "auth_failed" {
		t.Fatalf("health result = %#v", result)
	}
}

func TestComputePowResultHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (&GyingPlugin{}).computePowResultContext(ctx, &ChallengePageData{
		N: "ffffffffffffffffffffffffffffff61", X: "2", T: 1_000_000,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}
