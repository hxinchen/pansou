package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pansou/config"
	"pansou/model"
	"pansou/plugin"
	"pansou/service"
	"pansou/tgchannel"
	"pansou/util"

	"github.com/gin-gonic/gin"
)

type routerTestSearch struct {
	response model.SearchResponse
	err      error
}

func (s routerTestSearch) Search(string, []string, int, bool, string, string, []string, []string, map[string]interface{}) (model.SearchResponse, error) {
	return s.response, s.err
}

func (routerTestSearch) GetPluginManager() *plugin.PluginManager { return nil }

type contextRouterTestSearch struct {
	search func(context.Context) (model.SearchResponse, error)
}

func (s contextRouterTestSearch) Search(string, []string, int, bool, string, string, []string, []string, map[string]interface{}) (model.SearchResponse, error) {
	return s.search(context.Background())
}

func (s contextRouterTestSearch) SearchContext(ctx context.Context, _ service.ContextSearchRequest) (model.SearchResponse, error) {
	return s.search(ctx)
}

func (contextRouterTestSearch) GetPluginManager() *plugin.PluginManager { return nil }

type managedRouterTestSearch struct {
	channels    []string
	channelsNil bool
	pluginsNil  bool
}

func (s *managedRouterTestSearch) Search(_ string, channels []string, _ int, _ bool, _ string, _ string, plugins []string, _ []string, _ map[string]interface{}) (model.SearchResponse, error) {
	s.channelsNil = channels == nil
	s.pluginsNil = plugins == nil
	s.channels = append([]string(nil), channels...)
	return model.SearchResponse{}, nil
}

func (*managedRouterTestSearch) GetPluginManager() *plugin.PluginManager { return nil }
func (*managedRouterTestSearch) UsesManagedSources() bool                { return true }

type unmanagedRouterTestSearch struct{ managedRouterTestSearch }

func (*unmanagedRouterTestSearch) UsesManagedSources() bool { return false }

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

func TestPartialSearchReturns200AndCompletionMetadata(t *testing.T) {
	previous := config.AppConfig
	config.AppConfig = testConfig(false)
	defer func() { config.AppConfig = previous }()

	router := SetupRouter(routerTestSearch{response: model.SearchResponse{
		Total: 1, Completion: model.SearchCompletionPartial,
		PartialSources: []string{"plugin:slow"},
		MergedByType:   model.MergedLinks{"quark": {{URL: "https://pan.quark.cn/s/test"}}},
	}})
	request := httptest.NewRequest(http.MethodGet, "/api/search?kw=test", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After = %q, want 2", got)
	}
	var payload struct {
		Data model.SearchResponse `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Completion != model.SearchCompletionPartial || len(payload.Data.PartialSources) != 1 {
		t.Fatalf("partial response = %+v", payload.Data)
	}
}

func TestSearchSoftDeadlineReturnsProcessingResponse(t *testing.T) {
	previous := config.AppConfig
	config.AppConfig = testConfig(false)
	config.AppConfig.SearchResponseTimeout = 20 * time.Millisecond
	defer func() { config.AppConfig = previous }()

	provider := contextRouterTestSearch{search: func(ctx context.Context) (model.SearchResponse, error) {
		<-ctx.Done()
		return model.SearchResponse{}, ctx.Err()
	}}
	router := SetupRouter(provider)
	request := httptest.NewRequest(http.MethodGet, "/api/search?kw=test", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After = %q, want 2", got)
	}
	var payload struct {
		Data model.SearchResponse `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Completion != model.SearchCompletionProcessing || payload.Data.Total != 0 {
		t.Fatalf("processing response = %+v", payload.Data)
	}
}

func TestSearchUpstreamDeadlineReturnsGatewayTimeout(t *testing.T) {
	previous := config.AppConfig
	config.AppConfig = testConfig(false)
	config.AppConfig.SearchResponseTimeout = time.Second
	defer func() { config.AppConfig = previous }()

	provider := contextRouterTestSearch{search: func(context.Context) (model.SearchResponse, error) {
		return model.SearchResponse{}, context.DeadlineExceeded
	}}
	router := SetupRouter(provider)
	request := httptest.NewRequest(http.MethodGet, "/api/search?kw=test", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504; body=%s", response.Code, response.Body.String())
	}
}

func TestCanceledSearchRequestIsNotReportedAsServerError(t *testing.T) {
	previous := config.AppConfig
	config.AppConfig = testConfig(false)
	config.AppConfig.SearchResponseTimeout = time.Second
	defer func() { config.AppConfig = previous }()

	provider := contextRouterTestSearch{search: func(ctx context.Context) (model.SearchResponse, error) {
		<-ctx.Done()
		return model.SearchResponse{}, ctx.Err()
	}}
	router := SetupRouter(provider)
	requestCtx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/search?kw=test", nil).WithContext(requestCtx)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != statusClientClosedRequest {
		t.Fatalf("status = %d, want 499; body=%s", response.Code, response.Body.String())
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
	if !search.channelsNil {
		t.Fatal("omitted channels should remain nil")
	}
}

func TestSearchPostPreservesExplicitEmptyChannelArray(t *testing.T) {
	previous := config.AppConfig
	config.AppConfig = testConfig(false)
	defer func() { config.AppConfig = previous }()

	search := &managedRouterTestSearch{}
	router := SetupRouter(search)
	request := httptest.NewRequest(http.MethodPost, "/api/search", strings.NewReader(`{"kw":"test","src":"all","channels":[],"plugins":[]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	if search.channelsNil {
		t.Fatal("explicit empty channels should remain a non-nil empty slice")
	}
	if search.pluginsNil {
		t.Fatal("explicit empty plugins should remain a non-nil empty slice")
	}
}

func TestUnmanagedSearchPostPreservesExplicitEmptyChannelArray(t *testing.T) {
	previous := config.AppConfig
	config.AppConfig = testConfig(false)
	defer func() { config.AppConfig = previous }()

	search := &unmanagedRouterTestSearch{}
	router := SetupRouter(search)
	request := httptest.NewRequest(http.MethodPost, "/api/search", strings.NewReader(`{"kw":"test","src":"all","channels":[],"plugins":[]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	if search.channelsNil || len(search.channels) != 0 {
		t.Fatalf("explicit empty channels = %#v, want non-nil empty slice", search.channels)
	}
}

func TestSearchPostAcceptsChannelArrays(t *testing.T) {
	previous := config.AppConfig
	config.AppConfig = testConfig(false)
	defer func() { config.AppConfig = previous }()

	search := &managedRouterTestSearch{}
	router := SetupRouter(search)
	request := httptest.NewRequest(http.MethodPost, "/api/search", strings.NewReader(`{"kw":"test","src":"tg","channels":["@Custom_Channel","https://t.me/custom_channel"]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	if len(search.channels) != 2 {
		t.Fatalf("channels = %v, want POST array passed to managed service", search.channels)
	}
}

func TestSearchInvalidChannelReturnsBadRequest(t *testing.T) {
	previous := config.AppConfig
	config.AppConfig = testConfig(false)
	defer func() { config.AppConfig = previous }()

	router := SetupRouter(routerTestSearch{err: errors.Join(tgchannel.ErrInvalidChannel, errors.New("bad channel"))})
	request := httptest.NewRequest(http.MethodGet, "/api/search?kw=test&channels=bad-channel", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", response.Code, response.Body.String())
	}
}

func TestRouterIgnoresForwardedIPWithoutTrustedProxy(t *testing.T) {
	previous := config.AppConfig
	config.AppConfig = testConfig(false)
	config.AppConfig.TrustedProxies = nil
	defer func() { config.AppConfig = previous }()

	router := SetupRouter(routerTestSearch{})
	router.GET("/test-client-ip", func(c *gin.Context) { c.String(http.StatusOK, c.ClientIP()) })
	request := httptest.NewRequest(http.MethodGet, "/test-client-ip", nil)
	request.RemoteAddr = "198.51.100.10:4567"
	request.Header.Set("X-Forwarded-For", "203.0.113.25")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if got := strings.TrimSpace(response.Body.String()); got != "198.51.100.10" {
		t.Fatalf("client IP = %q, want direct peer", got)
	}
}

func TestRouterUsesForwardedChainFromTrustedProxy(t *testing.T) {
	previous := config.AppConfig
	config.AppConfig = testConfig(false)
	config.AppConfig.TrustedProxies = []string{"172.18.0.0/16"}
	defer func() { config.AppConfig = previous }()

	router := SetupRouter(routerTestSearch{})
	router.GET("/test-client-ip", func(c *gin.Context) { c.String(http.StatusOK, c.ClientIP()) })
	request := httptest.NewRequest(http.MethodGet, "/test-client-ip", nil)
	request.RemoteAddr = "172.18.0.4:4567"
	request.Header.Set("X-Forwarded-For", "2001:db8::25, 172.18.0.3")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if got := strings.TrimSpace(response.Body.String()); got != "2001:db8::25" {
		t.Fatalf("client IP = %q, want forwarded IPv6", got)
	}

	request = httptest.NewRequest(http.MethodGet, "/test-client-ip", nil)
	request.RemoteAddr = "172.18.0.4:4568"
	request.Header.Set("X-Real-IP", "203.0.113.26")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if got := strings.TrimSpace(response.Body.String()); got != "203.0.113.26" {
		t.Fatalf("client IP = %q, want trusted X-Real-IP", got)
	}
}
