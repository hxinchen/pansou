package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	accountauth "pansou/auth"
	"pansou/config"
	"pansou/model"
	"pansou/usage"
	"pansou/util"
)

type databaseAuthTestRepository struct {
	users      map[int64]accountauth.User
	byUsername map[string]int64
	byKey      map[string]int64
}

func (r *databaseAuthTestRepository) FindUserByNormalizedUsername(_ context.Context, username string) (accountauth.User, error) {
	id := r.byUsername[username]
	return r.users[id], nil
}

func (r *databaseAuthTestRepository) FindUserByID(_ context.Context, id int64) (accountauth.User, error) {
	return r.users[id], nil
}

func (r *databaseAuthTestRepository) FindUserByAPIKeyHash(_ context.Context, hash string) (accountauth.User, error) {
	id := r.byKey[hash]
	return r.users[id], nil
}

func (r *databaseAuthTestRepository) SetPassword(_ context.Context, id int64, hash string, mustChange bool) error {
	user := r.users[id]
	user.PasswordHash = hash
	user.MustChangePassword = mustChange
	user.AuthVersion++
	r.users[id] = user
	return nil
}

func (r *databaseAuthTestRepository) UpdateLastLogin(context.Context, int64, time.Time) error {
	return nil
}

func (r *databaseAuthTestRepository) UpdateAPIKeyLastUsed(context.Context, int64, time.Time) error {
	return nil
}

func setupDatabaseAuthRouter(t *testing.T, role string, mustChange bool) (*httptest.Server, accountauth.User, string) {
	t.Helper()
	previousConfig := config.AppConfig
	config.AppConfig = testConfig(false)
	t.Cleanup(func() { config.AppConfig = previousConfig })

	passwordHash, err := accountauth.HashPassword("Password!123")
	if err != nil {
		t.Fatal(err)
	}
	apiKey, _, _, err := accountauth.GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	user := accountauth.User{
		ID:                 7,
		Username:           "alice",
		NormalizedUsername: "alice",
		PasswordHash:       passwordHash,
		Role:               role,
		Enabled:            true,
		MustChangePassword: mustChange,
		AuthVersion:        3,
		RPSLimit:           3,
		RPMLimit:           60,
	}
	repository := &databaseAuthTestRepository{
		users:      map[int64]accountauth.User{user.ID: user},
		byUsername: map[string]int64{user.NormalizedUsername: user.ID},
		byKey:      map[string]int64{accountauth.HashAPIKey(apiKey): user.ID},
	}
	SetAuthService(accountauth.NewService(repository, "test-secret", time.Hour))
	SetUsageServices(usage.NewLimiter(usage.DefaultLimitConfig()), nil)
	t.Cleanup(func() {
		SetAuthService(nil)
		SetUsageServices(nil, nil)
	})
	return httptest.NewServer(SetupRouter(routerTestSearch{response: model.SearchResponse{Total: 1}})), user, apiKey
}

func userToken(t *testing.T, user accountauth.User) string {
	t.Helper()
	token, err := util.GenerateUserToken(util.TokenIdentity{
		UserID:      user.ID,
		Username:    user.Username,
		Role:        user.Role,
		AuthVersion: user.AuthVersion,
	}, "test-secret", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestDatabaseLoginReturnsRoleAndPasswordChangeState(t *testing.T) {
	server, _, _ := setupDatabaseAuthRouter(t, accountauth.RoleUser, true)
	defer server.Close()
	body, _ := json.Marshal(LoginRequest{Username: "alice", Password: "Password!123"})
	response, err := http.Post(server.URL+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var result LoginResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Role != accountauth.RoleUser || !result.MustChangePassword || result.Token == "" {
		t.Fatalf("response = %#v", result)
	}
}

func TestDatabasePasswordChangeRequiredBlocksUserAPIs(t *testing.T) {
	server, user, _ := setupDatabaseAuthRouter(t, accountauth.RoleUser, true)
	defer server.Close()
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/api/user/me", nil)
	request.Header.Set("Authorization", "Bearer "+userToken(t, user))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.StatusCode)
	}
}

func TestDatabaseAdminMiddlewareEnforcesRole(t *testing.T) {
	server, user, _ := setupDatabaseAuthRouter(t, accountauth.RoleUser, false)
	defer server.Close()
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/api/admin/overview", nil)
	request.Header.Set("Authorization", "Bearer "+userToken(t, user))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.StatusCode)
	}
}

func TestDatabaseAPIKeyOnlyAuthenticatesSearch(t *testing.T) {
	server, _, apiKey := setupDatabaseAuthRouter(t, accountauth.RoleUser, false)
	defer server.Close()

	searchRequest, _ := http.NewRequest(http.MethodGet, server.URL+"/api/search?kw=test", nil)
	searchRequest.Header.Set("X-API-Key", apiKey)
	searchResponse, err := http.DefaultClient.Do(searchRequest)
	if err != nil {
		t.Fatal(err)
	}
	searchResponse.Body.Close()
	if searchResponse.StatusCode != http.StatusOK {
		t.Fatalf("search status = %d", searchResponse.StatusCode)
	}

	meRequest, _ := http.NewRequest(http.MethodGet, server.URL+"/api/user/me", nil)
	meRequest.Header.Set("X-API-Key", apiKey)
	meResponse, err := http.DefaultClient.Do(meRequest)
	if err != nil {
		t.Fatal(err)
	}
	meResponse.Body.Close()
	if meResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me status = %d, want 401", meResponse.StatusCode)
	}
}

func TestDatabaseWebAndAPIKeyShareRateLimit(t *testing.T) {
	server, user, apiKey := setupDatabaseAuthRouter(t, accountauth.RoleUser, false)
	defer server.Close()
	token := userToken(t, user)

	for requestNumber := 1; requestNumber <= 4; requestNumber++ {
		request, _ := http.NewRequest(http.MethodGet, server.URL+"/api/search?kw=test", nil)
		if requestNumber <= 2 {
			request.Header.Set("Authorization", "Bearer "+token)
		} else {
			request.Header.Set("X-API-Key", apiKey)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if requestNumber <= 3 && response.StatusCode != http.StatusOK {
			t.Fatalf("request %d status = %d", requestNumber, response.StatusCode)
		}
		if requestNumber == 4 {
			if response.StatusCode != http.StatusTooManyRequests {
				t.Fatalf("request 4 status = %d, want 429", response.StatusCode)
			}
			if response.Header.Get("Retry-After") == "" || response.Header.Get("RateLimit-Limit") == "" {
				t.Fatalf("rate headers = %#v", response.Header)
			}
		}
	}
}

func TestDatabaseNormalUserCannotForceRefresh(t *testing.T) {
	server, user, _ := setupDatabaseAuthRouter(t, accountauth.RoleUser, false)
	defer server.Close()
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/api/search?kw=test&refresh=true", nil)
	request.Header.Set("Authorization", "Bearer "+userToken(t, user))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.StatusCode)
	}
}
