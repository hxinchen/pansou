package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"pansou/config"
	"pansou/model"
	"pansou/plugin"
	"pansou/util"
)

type routerTestSearch struct {
	response model.SearchResponse
}

func (s routerTestSearch) Search(string, []string, int, bool, string, string, []string, []string, map[string]interface{}) (model.SearchResponse, error) {
	return s.response, nil
}

func (routerTestSearch) GetPluginManager() *plugin.PluginManager { return nil }

type managedRouterTestSearch struct {
	channels []string
}

func (s *managedRouterTestSearch) Search(_ string, channels []string, _ int, _ bool, _ string, _ string, _ []string, _ []string, _ map[string]interface{}) (model.SearchResponse, error) {
	s.channels = append([]string(nil), channels...)
	return model.SearchResponse{}, nil
}

func (*managedRouterTestSearch) GetPluginManager() *plugin.PluginManager { return nil }
func (*managedRouterTestSearch) UsesManagedSources() bool                { return true }

func testConfig(authEnabled bool) *config.Config {
	return &config.Config{
		DefaultChannels: []string{"test"}, AuthEnabled: authEnabled,
		AuthUsers: map[string]string{"admin": "password"}, AuthJWTSecret: "test-secret",
		AuthTokenExpiry: time.Hour,
	}
}

func TestAdminRoutesRequireJWT(t *testing.T) {
	previous := config.AppConfig
	config.AppConfig = testConfig(true)
	defer func() { config.AppConfig = previous }()

	router := SetupRouter(routerTestSearch{})
	request := httptest.NewRequest(http.MethodGet, "/api/admin/overview", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", response.Code, response.Body.String())
	}
}

func TestAdminRoutesReturnUnavailableWithoutDatabase(t *testing.T) {
	previous := config.AppConfig
	config.AppConfig = testConfig(true)
	defer func() { config.AppConfig = previous }()
	token, err := util.GenerateToken("admin", config.AppConfig.AuthJWTSecret, time.Hour)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	router := SetupRouter(routerTestSearch{})
	request := httptest.NewRequest(http.MethodGet, "/api/admin/overview", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", response.Code, response.Body.String())
	}
}

func TestSearchResponseContractRemainsWrapped(t *testing.T) {
	previous := config.AppConfig
	config.AppConfig = testConfig(false)
	defer func() { config.AppConfig = previous }()

	router := SetupRouter(routerTestSearch{response: model.SearchResponse{
		Total:        1,
		MergedByType: model.MergedLinks{"quark": {{URL: "https://pan.quark.cn/s/test"}}},
	}})
	request := httptest.NewRequest(http.MethodGet, "/api/search?kw=test", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Code int                  `json:"code"`
		Data model.SearchResponse `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Code != 0 || payload.Data.Total != 1 || len(payload.Data.MergedByType["quark"]) != 1 {
		t.Fatalf("unexpected response: %+v", payload)
	}
}

func TestSearchLeavesOmittedChannelsEmptyForManagedSources(t *testing.T) {
	previous := config.AppConfig
	config.AppConfig = testConfig(false)
	defer func() { config.AppConfig = previous }()

	search := &managedRouterTestSearch{}
	router := SetupRouter(search)
	request := httptest.NewRequest(http.MethodGet, "/api/search?kw=test", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	if len(search.channels) != 0 {
		t.Fatalf("channels = %v, want omitted so the managed snapshot supplies all enabled channels", search.channels)
	}
}
