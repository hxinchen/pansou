package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"pansou/credential"
	"pansou/service"
	"pansou/storage"
)

type adminOnlyTokenAdapter struct{}

func (adminOnlyTokenAdapter) LoginWithToken(context.Context, string) (credential.LoginMaterial, error) {
	return credential.LoginMaterial{}, nil
}
func (adminOnlyTokenAdapter) SupportsCredentialScope(scope string) bool {
	return scope == storage.CredentialScopeAdminPrivate || scope == storage.CredentialScopePublicShared
}

type fakeCredentialDiagnostics struct {
	result service.CredentialDiagnosticResult
	err    error
}

func (f fakeCredentialDiagnostics) TestCredential(context.Context, service.CredentialDiagnosticRequest) (service.CredentialDiagnosticResult, error) {
	return f.result, f.err
}

func TestAdminCredentialFilterRestrictsCredentialScopes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/api/admin/plugin-credentials?scope=user_private&plugin_key=qqpd&page=2&page_size=25", nil)

	filter := adminCredentialFilter(c)
	wantScopes := []string{storage.CredentialScopeAdminPrivate, storage.CredentialScopePublicShared}
	if !reflect.DeepEqual(filter.Scopes, wantScopes) {
		t.Fatalf("admin scopes = %v, want %v", filter.Scopes, wantScopes)
	}
	if !reflect.DeepEqual(filter.PluginKeys, []string{"qqpd"}) {
		t.Fatalf("plugin keys = %v", filter.PluginKeys)
	}
	if filter.Page != 2 || filter.PageSize != 25 {
		t.Fatalf("pagination = (%d, %d), want (2, 25)", filter.Page, filter.PageSize)
	}
}

func TestSafeCredentialIncludesHealthWithoutSecrets(t *testing.T) {
	checkedAt := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	dto := safeCredential(storage.PluginCredential{
		PublicID: "cred", PluginKey: "gying", Status: storage.CredentialStatusInvalid,
		LastHealthCheckAt: &checkedAt, LastHealthStatus: storage.CredentialHealthInvalid,
		LastHealthErrorCode: "auth_failed", Ciphertext: []byte("secret"), Nonce: []byte("nonce"),
	})
	if dto.LastHealthCheckAt == nil || dto.LastHealthStatus != storage.CredentialHealthInvalid || dto.LastHealthErrorCode != "auth_failed" {
		t.Fatalf("credential health dto = %#v", dto)
	}
}

func TestAisoupanDescriptorUsesTokenAndRejectsUserScope(t *testing.T) {
	descriptor := credentialPluginDescriptor("aisoupan")
	if descriptor["login_type"] != "token" || descriptor["display_name"] != "心悦搜索（Aisoupan）" {
		t.Fatalf("descriptor = %#v", descriptor)
	}
	adapter := adminOnlyTokenAdapter{}
	if !credentialScopeSupported(adapter, storage.CredentialScopePublicShared) || credentialScopeSupported(adapter, storage.CredentialScopeUserPrivate) {
		t.Fatal("token credential scope policy mismatch")
	}
}

func TestAdminCredentialSearchTestRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewCredentialHandler(&credential.Service{}, nil).WithDiagnostics(fakeCredentialDiagnostics{result: service.CredentialDiagnosticResult{
		CredentialID: "cred-test", PluginKey: "aisoupan", Total: 1,
	}})
	handler.RegisterAdmin(router.Group("/api/admin"))
	request := httptest.NewRequest(http.MethodPost, "/api/admin/plugin-credentials/cred-test/search-test", bytes.NewBufferString(`{"keyword":"电影"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"credential_id":"cred-test"`)) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
