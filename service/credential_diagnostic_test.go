package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"pansou/config"
	"pansou/credential"
	"pansou/model"
	"pansou/plugin"
	"pansou/storage"
)

type diagnosticCredentialProvider struct {
	candidate    storage.PluginCredential
	healthStatus string
	healthCode   string
	healthCred   string
}

func (p *diagnosticCredentialProvider) Resolve(context.Context, credential.Identity, string, int) (credential.Layers, error) {
	return credential.Layers{}, nil
}
func (p *diagnosticCredentialProvider) OpenStored(storage.PluginCredential) ([]byte, error) {
	return []byte(`{"token":"test"}`), nil
}
func (p *diagnosticCredentialProvider) Success(context.Context, string) error { return nil }
func (p *diagnosticCredentialProvider) Failure(context.Context, string, string, string, *time.Time) error {
	return nil
}
func (p *diagnosticCredentialProvider) Get(context.Context, string) (storage.PluginCredential, error) {
	if p.candidate.PublicID == "" {
		return storage.PluginCredential{}, storage.ErrNotFound
	}
	return p.candidate, nil
}
func (p *diagnosticCredentialProvider) RecordHealth(_ context.Context, _ string, health, code, status string) error {
	p.healthStatus, p.healthCode, p.healthCred = health, code, status
	return nil
}

type diagnosticLayerPlugin struct {
	candidates []string
	empty      bool
	err        error
}

func (p *diagnosticLayerPlugin) Name() string             { return "managed" }
func (p *diagnosticLayerPlugin) Priority() int            { return 1 }
func (p *diagnosticLayerPlugin) SkipServiceFilter() bool  { return true }
func (p *diagnosticLayerPlugin) SetMainCacheKey(string)   {}
func (p *diagnosticLayerPlugin) SetCurrentKeyword(string) {}
func (p *diagnosticLayerPlugin) Search(string, map[string]interface{}) ([]model.SearchResult, error) {
	return nil, nil
}
func (p *diagnosticLayerPlugin) AsyncSearch(keyword string, fn func(*http.Client, string, map[string]interface{}) ([]model.SearchResult, error), _ string, ext map[string]interface{}) ([]model.SearchResult, error) {
	return fn(http.DefaultClient, keyword, ext)
}
func (p *diagnosticLayerPlugin) SearchCredentialLayer(_ context.Context, _ string, ext map[string]interface{}, candidates []storage.PluginCredential, _ credential.Access) ([]model.SearchResult, bool, error) {
	for _, candidate := range candidates {
		p.candidates = append(p.candidates, candidate.PublicID)
	}
	if ext["diagnostic"] != true || ext["refresh"] != true {
		return nil, false, errors.New("diagnostic cache bypass flags missing")
	}
	if p.err != nil {
		return nil, false, p.err
	}
	if p.empty {
		return nil, false, credential.ErrNoResults
	}
	return []model.SearchResult{{UniqueID: "managed-result", Title: "测试", Links: []model.Link{{Type: "quark", URL: "https://pan.quark.cn/s/test"}}}}, true, nil
}

func newDiagnosticService(t *testing.T, candidate storage.PluginCredential, instance *diagnosticLayerPlugin) (*SearchService, *diagnosticCredentialProvider) {
	t.Helper()
	manager := plugin.NewPluginManager()
	if err := manager.RegisterPluginChecked(instance); err != nil {
		t.Fatal(err)
	}
	provider := &diagnosticCredentialProvider{candidate: candidate}
	return &SearchService{pluginManager: manager, credentials: provider}, provider
}

func TestCredentialDiagnosticUsesOnlySelectedCredential(t *testing.T) {
	previous := config.AppConfig
	config.AppConfig = &config.Config{PluginTimeout: time.Second, SearchSchedulerEnabled: false}
	ResetSearchScheduler()
	defer func() { config.AppConfig = previous; ResetSearchScheduler() }()

	instance := &diagnosticLayerPlugin{}
	searchService, provider := newDiagnosticService(t, storage.PluginCredential{PublicID: "selected", PluginKey: "managed", Scope: storage.CredentialScopePublicShared}, instance)
	result, err := searchService.TestCredential(context.Background(), CredentialDiagnosticRequest{CredentialID: "selected", Keyword: "测试"})
	if err != nil {
		t.Fatal(err)
	}
	if len(instance.candidates) != 1 || instance.candidates[0] != "selected" {
		t.Fatalf("candidates = %#v", instance.candidates)
	}
	if result.Total != 1 || len(result.MergedByType["quark"]) != 1 || result.Completion != model.SearchCompletionComplete {
		t.Fatalf("result = %#v", result)
	}
	if provider.healthStatus != storage.CredentialHealthHealthy || provider.healthCred != storage.CredentialStatusActive {
		t.Fatalf("health = %s/%s", provider.healthStatus, provider.healthCred)
	}
}

func TestCredentialDiagnosticTreatsHealthyEmptyAsComplete(t *testing.T) {
	previous := config.AppConfig
	config.AppConfig = &config.Config{PluginTimeout: time.Second, SearchSchedulerEnabled: false}
	ResetSearchScheduler()
	defer func() { config.AppConfig = previous; ResetSearchScheduler() }()

	searchService, _ := newDiagnosticService(t, storage.PluginCredential{PublicID: "empty", PluginKey: "managed", Scope: storage.CredentialScopeAdminPrivate}, &diagnosticLayerPlugin{empty: true})
	result, err := searchService.TestCredential(context.Background(), CredentialDiagnosticRequest{CredentialID: "empty", Keyword: "没有结果"})
	if err != nil || result.Total != 0 || result.Completion != model.SearchCompletionComplete {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestCredentialDiagnosticRejectsUserPrivateCredential(t *testing.T) {
	instance := &diagnosticLayerPlugin{}
	searchService, _ := newDiagnosticService(t, storage.PluginCredential{PublicID: "user-private", PluginKey: "managed", Scope: storage.CredentialScopeUserPrivate}, instance)
	_, err := searchService.TestCredential(context.Background(), CredentialDiagnosticRequest{CredentialID: "user-private", Keyword: "测试"})
	if !errors.Is(err, ErrCredentialDiagnosticForbidden) || len(instance.candidates) != 0 {
		t.Fatalf("err=%v candidates=%#v", err, instance.candidates)
	}
}
