package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	accountauth "pansou/auth"
	"pansou/storage"
)

func adminLinkCheckPolicyTestRouter(handler *AdminHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler.Register(router.Group("/api/admin"))
	return router
}

func performAdminLinkCheckPolicyRequest(t *testing.T, router http.Handler, method string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "/api/admin/link-check-policy", bytes.NewReader(body))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestAdminHandlerRegistersLinkCheckPolicyRoutes(t *testing.T) {
	router := adminLinkCheckPolicyTestRouter(NewAdminHandler(nil, nil))
	routes := make(map[string]bool)
	for _, route := range router.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	for _, route := range []string{
		"GET /api/admin/link-check-policy",
		"PUT /api/admin/link-check-policy",
	} {
		if !routes[route] {
			t.Fatalf("route %q is not registered", route)
		}
	}
}

func TestAdminLinkCheckPolicyReturnsServiceUnavailableWithoutStore(t *testing.T) {
	router := adminLinkCheckPolicyTestRouter(NewAdminHandler(nil, nil))
	for _, test := range []struct {
		method string
		body   []byte
	}{
		{method: http.MethodGet},
		{method: http.MethodPut, body: []byte(`{"enabled":false,"statuses":["valid"],"interval_seconds":3600}`)},
	} {
		response := performAdminLinkCheckPolicyRequest(t, router, test.method, test.body)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want 503; body=%s", test.method, response.Code, response.Body.String())
		}
	}
}

func TestAdminLinkCheckPolicyRejectsNonAdminUser(t *testing.T) {
	server, user, _ := setupDatabaseAuthRouter(t, accountauth.RoleUser, false)
	defer server.Close()
	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/admin/link-check-policy", nil)
	if err != nil {
		t.Fatal(err)
	}
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

func TestRespondAdminErrorMapsInvalidPolicyToBadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	respondAdminError(context, fmt.Errorf("invalid link-check policy: %w", storage.ErrInvalid))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", response.Code, response.Body.String())
	}
}

func TestAdminLinkCheckPolicyGetAndPut(t *testing.T) {
	store := newAdminLinkCheckPolicyTestStore(t)
	router := adminLinkCheckPolicyTestRouter(NewAdminHandler(store, nil))

	getResponse := performAdminLinkCheckPolicyRequest(t, router, http.MethodGet, nil)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("initial GET status = %d, want 200; body=%s", getResponse.Code, getResponse.Body.String())
	}
	initial := decodeAdminLinkCheckPolicyResponse(t, getResponse)
	if initial.Enabled || initial.IntervalSeconds != 7*24*60*60 || !sameStatuses(initial.Statuses, []string{"valid", "unknown"}) {
		t.Fatalf("initial policy = %#v", initial)
	}
	if initial.UpdatedAt.IsZero() {
		t.Fatal("initial updated_at is zero")
	}

	putResponse := performAdminLinkCheckPolicyRequest(t, router, http.MethodPut,
		[]byte(`{"enabled":true,"statuses":["unknown","valid","unknown"],"interval_seconds":3600}`))
	if putResponse.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body=%s", putResponse.Code, putResponse.Body.String())
	}
	updated := decodeAdminLinkCheckPolicyResponse(t, putResponse)
	if !updated.Enabled || updated.IntervalSeconds != 3600 || !sameStatuses(updated.Statuses, []string{"valid", "unknown"}) {
		t.Fatalf("updated policy = %#v", updated)
	}

	getResponse = performAdminLinkCheckPolicyRequest(t, router, http.MethodGet, nil)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("updated GET status = %d, want 200; body=%s", getResponse.Code, getResponse.Body.String())
	}
	persisted := decodeAdminLinkCheckPolicyResponse(t, getResponse)
	if !persisted.Enabled || persisted.IntervalSeconds != 3600 || !sameStatuses(persisted.Statuses, []string{"valid", "unknown"}) {
		t.Fatalf("persisted policy = %#v", persisted)
	}

	invalidResponse := performAdminLinkCheckPolicyRequest(t, router, http.MethodPut,
		[]byte(`{"enabled":true,"statuses":["unsupported"],"interval_seconds":3600}`))
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid PUT status = %d, want 400; body=%s", invalidResponse.Code, invalidResponse.Body.String())
	}
}

func decodeAdminLinkCheckPolicyResponse(t *testing.T, response *httptest.ResponseRecorder) storage.LinkCheckPolicy {
	t.Helper()
	var payload struct {
		Code int                     `json:"code"`
		Data storage.LinkCheckPolicy `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode policy response: %v", err)
	}
	if payload.Code != 0 {
		t.Fatalf("response code = %d, want 0; body=%s", payload.Code, response.Body.String())
	}
	return payload.Data
}

func sameStatuses(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	counts := make(map[string]int, len(want))
	for _, status := range want {
		counts[status]++
	}
	for _, status := range got {
		counts[status]--
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}

func newAdminLinkCheckPolicyTestStore(t *testing.T) *storage.Store {
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
		t.Fatalf("open integration database: %v", err)
	}
	schema := fmt.Sprintf("pansou_api_policy_test_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		admin.Close()
		t.Fatalf("create integration schema: %v", err)
	}
	store, err := storage.Open(ctx, databaseURL, storage.WithPoolConfig(func(config *pgxpool.Config) {
		config.ConnConfig.RuntimeParams["search_path"] = schema
		config.MaxConns = 4
	}))
	if err != nil {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE")
		admin.Close()
		t.Fatalf("open integration store: %v", err)
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
