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
	privateEmpty   bool
	sharedEmpty    bool
	privatePartial bool
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
		if p.privatePartial {
			return []model.SearchResult{{UniqueID: "partial-result", Links: []model.Link{{URL: "https://example.com/partial"}}}}, true, fakeSourceStatusError{}
		}
		if p.privateEmpty {
			return nil, false, credential.ErrNoResults
		}
		return nil, p.privateSuccess, nil
	}
	if label == "shared" {
		if p.sharedEmpty {
			return nil, false, credential.ErrNoResults
		}
		return []model.SearchResult{{UniqueID: "shared-result", Links: []model.Link{{URL: "https://example.com/shared"}}}}, true, nil
	}
	return nil, false, nil
}

type fakeSourceStatusError struct{}

func (fakeSourceStatusError) Error() string { return "partial detail failure" }
func (fakeSourceStatusError) SourceStatus() model.SourceStatus {
	return model.SourceStatus{Completion: model.SearchCompletionPartial, Candidates: 10, Attempted: 8, Succeeded: 7, Failed: 1, Message: "partial detail failure"}
}

func TestCredentialLayerFallsBackToSharedAfterHealthyPrivateEmpty(t *testing.T) {
	config.AppConfig = &config.Config{PluginTimeout: time.Second, DefaultConcurrency: 1}
	manager := plugin.NewPluginManager()
	instance := &fakeLayerPlugin{privateEmpty: true}
	manager.RegisterPlugin(instance)
	service := &SearchService{pluginManager: manager, credentials: fakeCredentialProvider{layers: credential.Layers{
		Private: []storage.PluginCredential{{PublicID: "private"}}, Shared: []storage.PluginCredential{{PublicID: "shared"}},
	}}}
	batch, err := service.searchPluginsForIdentityWithStatus(context.Background(), SearchIdentity{Actor: SearchActorUser, UserID: 7}, "x", nil, false, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !batch.Complete || len(batch.Results) != 1 || batch.Results[0].UniqueID != "shared-result" {
		t.Fatalf("batch = %#v", batch)
	}
	if len(instance.calls) != 2 || instance.calls[0] != "private" || instance.calls[1] != "shared" {
		t.Fatalf("calls = %#v", instance.calls)
	}
}

func TestCredentialLayerHealthyEmptyAcrossLayersIsComplete(t *testing.T) {
	config.AppConfig = &config.Config{PluginTimeout: time.Second, DefaultConcurrency: 1}
	manager := plugin.NewPluginManager()
	instance := &fakeLayerPlugin{privateEmpty: true, sharedEmpty: true}
	manager.RegisterPlugin(instance)
	service := &SearchService{pluginManager: manager, credentials: fakeCredentialProvider{layers: credential.Layers{
		Private: []storage.PluginCredential{{PublicID: "private"}}, Shared: []storage.PluginCredential{{PublicID: "shared"}},
	}}}
	batch, err := service.searchPluginsForIdentityWithStatus(context.Background(), SearchIdentity{Actor: SearchActorUser, UserID: 7}, "x", nil, false, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !batch.Complete || len(batch.Results) != 0 || len(batch.PartialSources) != 0 {
		t.Fatalf("batch = %#v", batch)
	}
}

func TestCredentialLayerWithoutAvailableAccountsDoesNotFailSource(t *testing.T) {
	config.AppConfig = &config.Config{PluginTimeout: time.Second, DefaultConcurrency: 1}
	manager := plugin.NewPluginManager()
	instance := &fakeLayerPlugin{}
	manager.RegisterPlugin(instance)
	service := &SearchService{pluginManager: manager, credentials: fakeCredentialProvider{}}
	batch, err := service.searchPluginsForIdentityWithStatus(context.Background(), SearchIdentity{Actor: SearchActorUser, UserID: 7}, "x", nil, false, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !batch.Complete || len(batch.Results) != 0 || len(batch.PartialSources) != 0 {
		t.Fatalf("batch = %#v", batch)
	}
	if len(instance.calls) != 0 {
		t.Fatalf("credential adapter calls = %#v, want none", instance.calls)
	}
}

func TestCredentialLayerPartialStatusIsExposed(t *testing.T) {
	config.AppConfig = &config.Config{PluginTimeout: time.Second, DefaultConcurrency: 1}
	manager := plugin.NewPluginManager()
	instance := &fakeLayerPlugin{privatePartial: true}
	manager.RegisterPlugin(instance)
	service := &SearchService{pluginManager: manager, credentials: fakeCredentialProvider{layers: credential.Layers{
		Private: []storage.PluginCredential{{PublicID: "private"}},
	}}}
	batch, err := service.searchPluginsForIdentityWithStatus(context.Background(), SearchIdentity{Actor: SearchActorUser, UserID: 7}, "x", nil, false, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	status, ok := batch.SourceStatuses["plugin:managed"]
	if batch.Complete || !ok || status.Failed != 1 || len(batch.Results) != 1 {
		t.Fatalf("batch = %#v", batch)
	}
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
