package service

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"pansou/config"
	"pansou/credential"
	"pansou/model"
	"pansou/plugin"
	"pansou/storage"
)

type fakeCredentialProvider struct{ layers credential.Layers }

func (f fakeCredentialProvider) Resolve(context.Context, credential.Identity, string, int) (credential.Layers, error) {
	return f.layers, nil
}
func (fakeCredentialProvider) OpenStored(storage.PluginCredential) ([]byte, error) {
	return []byte("{}"), nil
}
func (fakeCredentialProvider) Success(context.Context, string) error { return nil }
func (fakeCredentialProvider) Failure(context.Context, string, string, string, *time.Time) error {
	return nil
}

type fakeLayerPlugin struct {
	mu             sync.Mutex
	calls          []string
	privateSuccess bool
}

func (p *fakeLayerPlugin) Name() string             { return "managed" }
func (p *fakeLayerPlugin) Priority() int            { return 1 }
func (p *fakeLayerPlugin) SkipServiceFilter() bool  { return false }
func (p *fakeLayerPlugin) SetMainCacheKey(string)   {}
func (p *fakeLayerPlugin) SetCurrentKeyword(string) {}
func (p *fakeLayerPlugin) Search(string, map[string]interface{}) ([]model.SearchResult, error) {
	return nil, nil
}
func (p *fakeLayerPlugin) AsyncSearch(keyword string, fn func(*http.Client, string, map[string]interface{}) ([]model.SearchResult, error), _ string, ext map[string]interface{}) ([]model.SearchResult, error) {
	return fn(http.DefaultClient, keyword, ext)
}
func (p *fakeLayerPlugin) SearchCredentialLayer(_ context.Context, _ string, _ map[string]interface{}, candidates []storage.PluginCredential, _ credential.Access) ([]model.SearchResult, bool, error) {
	label := "empty"
	if len(candidates) > 0 {
		label = candidates[0].PublicID
	}
	p.mu.Lock()
	p.calls = append(p.calls, label)
	p.mu.Unlock()
	if label == "private" {
		return nil, p.privateSuccess, nil
	}
	if label == "shared" {
		return []model.SearchResult{{UniqueID: "shared-result", Links: []model.Link{{URL: "https://example.com/shared"}}}}, true, nil
	}
	return nil, false, nil
}

func TestCredentialLayerPrivateSuccessStopsSharedFallback(t *testing.T) {
	config.AppConfig = &config.Config{PluginTimeout: time.Second, DefaultConcurrency: 1}
	manager := plugin.NewPluginManager()
	instance := &fakeLayerPlugin{privateSuccess: true}
	manager.RegisterPlugin(instance)
	service := &SearchService{pluginManager: manager, credentials: fakeCredentialProvider{layers: credential.Layers{
		Private: []storage.PluginCredential{{PublicID: "private"}}, Shared: []storage.PluginCredential{{PublicID: "shared"}},
	}}}
	results, err := service.searchPluginsForIdentity(context.Background(), SearchIdentity{Actor: SearchActorUser, UserID: 7}, "x", nil, false, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("results = %d, want zero", len(results))
	}
	if len(instance.calls) != 1 || instance.calls[0] != "private" {
		t.Fatalf("calls = %#v", instance.calls)
	}
}

func TestCredentialLayerFallsBackToSharedAfterPrivateFailure(t *testing.T) {
	config.AppConfig = &config.Config{PluginTimeout: time.Second, DefaultConcurrency: 1}
	manager := plugin.NewPluginManager()
	instance := &fakeLayerPlugin{}
	manager.RegisterPlugin(instance)
	service := &SearchService{pluginManager: manager, credentials: fakeCredentialProvider{layers: credential.Layers{
		Private: []storage.PluginCredential{{PublicID: "private"}}, Shared: []storage.PluginCredential{{PublicID: "shared"}},
	}}}
	results, err := service.searchPluginsForIdentity(context.Background(), SearchIdentity{Actor: SearchActorUser, UserID: 7}, "x", nil, false, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].UniqueID != "shared-result" {
		t.Fatalf("results = %#v", results)
	}
	if len(instance.calls) != 2 || instance.calls[0] != "private" || instance.calls[1] != "shared" {
		t.Fatalf("calls = %#v", instance.calls)
	}
}

var _ plugin.AsyncSearchPlugin = (*fakeLayerPlugin)(nil)
var _ credential.LayerSearcher = (*fakeLayerPlugin)(nil)
