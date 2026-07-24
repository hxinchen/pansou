package aisoupan

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pansou/credential"
	"pansou/model"
	"pansou/storage"
)

func testPlugin(server *httptest.Server) *AisoupanPlugin {
	p := NewAisoupanPlugin()
	p.endpoint = server.URL
	p.client = server.Client()
	p.client.Timeout = requestTimeout
	return p
}

func writeSearchResponse(t *testing.T, w http.ResponseWriter, response model.SearchResponse) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(apiEnvelope{Code: 0, Message: "success", Data: response}); err != nil {
		t.Fatal(err)
	}
}

func testJWT(username string, expires time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, _ := json.Marshal(tokenClaims{Username: username, Issuer: "pansou", Expires: expires.Unix()})
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func TestLoginWithTokenValidatesAndExtractsJWTMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer valid-token" && !strings.HasPrefix(got, "Bearer ey") {
			t.Errorf("Authorization = %q", got)
		}
		if r.URL.Query().Get("src") != "all" || r.URL.Query().Get("res") != "all" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		writeSearchResponse(t, w, model.SearchResponse{})
	}))
	defer server.Close()

	expires := time.Now().Add(time.Hour).Truncate(time.Second)
	material, err := testPlugin(server).LoginWithToken(context.Background(), testJWT("account-a", expires))
	if err != nil {
		t.Fatal(err)
	}
	if material.DisplayName != "account-a" || string(material.StableID) != "pansou:account-a" {
		t.Fatalf("material = %#v", material)
	}
	if material.ExpiresAt == nil || !material.ExpiresAt.Equal(expires) {
		t.Fatalf("expires = %v, want %v", material.ExpiresAt, expires)
	}
	if strings.Contains(string(material.Secret), testJWT("account-a", expires)) == false {
		t.Fatalf("encrypted material source payload does not contain token before storage")
	}
	if !testPlugin(server).SupportsCredentialScope(storage.CredentialScopePublicShared) || testPlugin(server).SupportsCredentialScope(storage.CredentialScopeUserPrivate) {
		t.Fatal("credential scope policy mismatch")
	}
}

func TestLoginWithTokenRejectsExpiredJWTBeforeRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeSearchResponse(t, w, model.SearchResponse{})
	}))
	defer server.Close()
	_, err := testPlugin(server).LoginWithToken(context.Background(), testJWT("expired", time.Now().Add(-time.Minute)))
	if !errors.Is(err, storage.ErrInvalid) || calls.Load() != 0 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestSearchCredentialLayerCachesPerCredentialAndDiagnosticBypasses(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer token-a" {
			t.Fatalf("missing bearer token")
		}
		writeSearchResponse(t, w, model.SearchResponse{Results: []model.SearchResult{{
			UniqueID: "remote-id", Title: "测试电影", Links: []model.Link{{URL: "https://pan.quark.cn/s/test"}},
		}}})
	}))
	defer server.Close()
	p := testPlugin(server)
	secret, _ := json.Marshal(tenantSecret{Token: "token-a"})
	access := credential.Access{Open: func(storage.PluginCredential) ([]byte, error) { return append([]byte(nil), secret...), nil }}
	candidate := storage.PluginCredential{PublicID: "cred-a"}
	for index := 0; index < 2; index++ {
		results, succeeded, err := p.SearchCredentialLayer(context.Background(), "测试电影", nil, []storage.PluginCredential{candidate}, access)
		if err != nil || !succeeded || len(results) != 1 || !strings.HasPrefix(results[0].UniqueID, PluginName+":") {
			t.Fatalf("results=%#v succeeded=%v err=%v", results, succeeded, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("cached calls = %d, want 1", calls.Load())
	}
	_, _, _ = p.SearchCredentialLayer(context.Background(), "测试电影", map[string]interface{}{"diagnostic": true}, []storage.PluginCredential{candidate}, access)
	if calls.Load() != 2 {
		t.Fatalf("diagnostic calls = %d, want 2", calls.Load())
	}
	second := candidate
	second.PublicID = "cred-b"
	_, _, _ = p.SearchCredentialLayer(context.Background(), "测试电影", nil, []storage.PluginCredential{second}, access)
	if calls.Load() != 3 {
		t.Fatalf("credential-isolated calls = %d, want 3", calls.Load())
	}
}

func TestSearchRemoteConvertsMergedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSearchResponse(t, w, model.SearchResponse{MergedByType: model.MergedLinks{"quark": []model.MergedLink{{
			URL: "https://pan.quark.cn/s/merged", Password: "1234", Note: "合并结果", Source: "plugin:remote",
		}}}})
	}))
	defer server.Close()
	results, err := testPlugin(server).searchRemote(context.Background(), "token", "合并结果")
	if err != nil || len(results) != 1 || results[0].Links[0].Type != "quark" || results[0].SubSource != "plugin:remote" {
		t.Fatalf("results=%#v err=%v", results, err)
	}
}

func TestSearchCredentialLayerClassifiesAuthAndRateLimit(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		wantStatus string
		wantCode   string
		cooldown   bool
	}{
		{name: "auth", status: http.StatusUnauthorized, wantStatus: storage.CredentialStatusInvalid, wantCode: "auth_failed"},
		{name: "rate", status: http.StatusTooManyRequests, wantStatus: storage.CredentialStatusActive, wantCode: "rate_limited", cooldown: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.status == http.StatusTooManyRequests {
					w.Header().Set("Retry-After", "2")
				}
				w.WriteHeader(test.status)
			}))
			defer server.Close()
			secret, _ := json.Marshal(tenantSecret{Token: "token"})
			var gotStatus, gotCode string
			var gotCooldown *time.Time
			access := credential.Access{
				Open: func(storage.PluginCredential) ([]byte, error) { return append([]byte(nil), secret...), nil },
				Failure: func(_ context.Context, _ string, status, code string, cooldown *time.Time) {
					gotStatus, gotCode, gotCooldown = status, code, cooldown
				},
			}
			_, succeeded, err := testPlugin(server).SearchCredentialLayer(context.Background(), "test", nil, []storage.PluginCredential{{PublicID: "cred"}}, access)
			if err == nil || succeeded || gotStatus != test.wantStatus || gotCode != test.wantCode || (gotCooldown != nil) != test.cooldown {
				t.Fatalf("succeeded=%v err=%v status=%s code=%s cooldown=%v", succeeded, err, gotStatus, gotCode, gotCooldown)
			}
		})
	}
}

func TestSearchRemoteHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := testPlugin(server).searchRemote(ctx, "token", "test")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context canceled", err)
	}
}
