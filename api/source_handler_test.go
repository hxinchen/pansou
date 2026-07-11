package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/gin-gonic/gin"

	"pansou/sourceconfig"
)

func sourceRouteCatalog(t *testing.T) *sourceconfig.Catalog {
	t.Helper()
	catalog, err := sourceconfig.NewCatalog([]sourceconfig.PluginDescriptor{
		{Key: "plain", DisplayName: "Plain"},
		{
			Key: "gying", DisplayName: "观影", RequiresAccount: true, LoginType: "password",
			AllowedConfigKeys: []string{"base_url"}, BindingConfigKeys: []string{"base_url"},
		},
	})
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	return catalog
}

func sourceRouteConfig() sourceconfig.Config {
	return sourceconfig.Config{
		SchemaVersion:       1,
		AsyncPluginsEnabled: true,
		Channels: []sourceconfig.Channel{
			{Key: "@Movies", DisplayName: "Movies", Enabled: true, Order: 2},
		},
		Plugins: map[string]sourceconfig.PluginConfig{
			"gying": {Enabled: true, Order: 1, Config: map[string]any{"base_url": "https://example.com/path"}},
		},
	}
}

func sourceRouteRouter(handler *AdminHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler.Register(router.Group("/api/admin"))
	return router
}

func sourceRouteRequest(t *testing.T, router http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var document []byte
	if body != nil {
		var err error
		document, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("Marshal request: %v", err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(document))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func sourceRouteData(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	var envelope struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, response.Body.String())
	}
	if envelope.Code != 0 {
		t.Fatalf("response code = %d; body=%s", envelope.Code, response.Body.String())
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		t.Fatalf("decode response data: %v; body=%s", err, response.Body.String())
	}
}

func TestAdminSourceRoutesAreRegistered(t *testing.T) {
	router := sourceRouteRouter(NewAdminHandler(nil, nil, &sourceconfig.Service{Catalog: sourceRouteCatalog(t)}))
	want := []string{
		http.MethodGet + " /api/admin/search-sources/catalog",
		http.MethodGet + " /api/admin/search-sources/config",
		http.MethodPost + " /api/admin/search-sources/validate",
		http.MethodPut + " /api/admin/search-sources/config",
		http.MethodGet + " /api/admin/search-sources/events",
	}
	got := make([]string, 0, len(want))
	for _, route := range router.Routes() {
		candidate := route.Method + " " + route.Path
		for _, expected := range want {
			if candidate == expected {
				got = append(got, candidate)
			}
		}
	}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("registered source routes = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("registered source routes = %v, want %v", got, want)
		}
	}
}

func TestAdminSourceCatalogReturnsPublicDescriptors(t *testing.T) {
	router := sourceRouteRouter(NewAdminHandler(nil, nil, &sourceconfig.Service{Catalog: sourceRouteCatalog(t)}))
	response := sourceRouteRequest(t, router, http.MethodGet, "/api/admin/search-sources/catalog", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", response.Code, response.Body.String())
	}
	var data struct {
		Items []sourceconfig.PluginDescriptor `json:"items"`
	}
	sourceRouteData(t, response, &data)
	if len(data.Items) != 2 || data.Items[0].Key != "gying" || data.Items[1].Key != "plain" {
		t.Fatalf("catalog items = %+v", data.Items)
	}
	if !data.Items[0].RequiresAccount || data.Items[0].LoginType != "password" {
		t.Fatalf("gying descriptor = %+v", data.Items[0])
	}
}

func TestAdminSourceValidateNormalizesWithoutPublishing(t *testing.T) {
	runtime := &sourceconfig.Service{Catalog: sourceRouteCatalog(t)}
	router := sourceRouteRouter(NewAdminHandler(nil, nil, runtime))
	response := sourceRouteRequest(t, router, http.MethodPost, "/api/admin/search-sources/validate", sourceConfigRequest{Config: sourceRouteConfig()})
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", response.Code, response.Body.String())
	}
	var data struct {
		Valid  bool                `json:"valid"`
		Config sourceconfig.Config `json:"config"`
	}
	sourceRouteData(t, response, &data)
	if !data.Valid || len(data.Config.Channels) != 1 || data.Config.Channels[0].Key != "movies" {
		t.Fatalf("validated config = %+v", data)
	}
	if got := data.Config.Plugins["gying"].Config["base_url"]; got != "https://example.com" {
		t.Fatalf("normalized base_url = %v", got)
	}
}

func TestAdminSourceUpdateRequiresPrincipal(t *testing.T) {
	runtime := &sourceconfig.Service{Catalog: sourceRouteCatalog(t)}
	router := sourceRouteRouter(NewAdminHandler(nil, nil, runtime))
	response := sourceRouteRequest(t, router, http.MethodPut, "/api/admin/search-sources/config", sourceConfigRequest{
		ExpectedVersion: 1,
		Config:          sourceRouteConfig(),
	})
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", response.Code, response.Body.String())
	}
}

func TestAdminSourceEndpointsReportUnavailableRuntime(t *testing.T) {
	router := sourceRouteRouter(NewAdminHandler(nil, nil))
	for _, request := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/api/admin/search-sources/catalog", nil},
		{http.MethodGet, "/api/admin/search-sources/config", nil},
		{http.MethodPost, "/api/admin/search-sources/validate", sourceConfigRequest{Config: sourceRouteConfig()}},
	} {
		response := sourceRouteRequest(t, router, request.method, request.path, request.body)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s status = %d, want 503; body=%s", request.method, request.path, response.Code, response.Body.String())
		}
	}
}
