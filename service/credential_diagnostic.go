package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"pansou/config"
	"pansou/credential"
	"pansou/model"
	"pansou/plugin"
	searchscheduler "pansou/service/scheduler"
	"pansou/storage"
)

var (
	ErrCredentialDiagnosticUnavailable = errors.New("credential diagnostic is unavailable")
	ErrCredentialDiagnosticUnsupported = errors.New("credential does not support diagnostic search")
	ErrCredentialDiagnosticForbidden   = errors.New("credential scope cannot be diagnosed by administrator")
)

type CredentialDiagnosticProvider interface {
	TestCredential(context.Context, CredentialDiagnosticRequest) (CredentialDiagnosticResult, error)
}

type credentialDiagnosticStore interface {
	Get(context.Context, string) (storage.PluginCredential, error)
	RecordHealth(context.Context, string, string, string, string) error
}

type CredentialDiagnosticRequest struct {
	CredentialID string
	Keyword      string
}

type CredentialDiagnosticResult struct {
	CredentialID string                 `json:"credential_id"`
	PluginKey    string                 `json:"plugin_key"`
	DurationMS   int64                  `json:"duration_ms"`
	Total        int                    `json:"total"`
	Results      []model.SearchResult   `json:"results,omitempty"`
	MergedByType model.MergedLinks      `json:"merged_by_type,omitempty"`
	Completion   model.SearchCompletion `json:"completion"`
}

func (s *SearchService) TestCredential(ctx context.Context, request CredentialDiagnosticRequest) (CredentialDiagnosticResult, error) {
	credentialID := strings.TrimSpace(request.CredentialID)
	keyword := strings.TrimSpace(request.Keyword)
	if s == nil || s.credentials == nil || credentialID == "" || keyword == "" {
		return CredentialDiagnosticResult{}, ErrCredentialDiagnosticUnavailable
	}
	diagnosticStore, ok := s.credentials.(credentialDiagnosticStore)
	if !ok {
		return CredentialDiagnosticResult{}, ErrCredentialDiagnosticUnavailable
	}
	candidate, err := diagnosticStore.Get(ctx, credentialID)
	if err != nil {
		return CredentialDiagnosticResult{}, err
	}
	if candidate.Scope != storage.CredentialScopeAdminPrivate && candidate.Scope != storage.CredentialScopePublicShared {
		return CredentialDiagnosticResult{}, ErrCredentialDiagnosticForbidden
	}

	manager, release, err := s.diagnosticPluginManager()
	if err != nil {
		return CredentialDiagnosticResult{}, err
	}
	defer release()
	var instance plugin.AsyncSearchPlugin
	for _, current := range manager.GetPlugins() {
		if strings.EqualFold(current.Name(), candidate.PluginKey) {
			instance = current
			break
		}
	}
	searcher, ok := instance.(credential.LayerSearcher)
	if instance == nil || !ok {
		return CredentialDiagnosticResult{}, ErrCredentialDiagnosticUnsupported
	}

	access := credential.Access{
		Open: s.credentials.OpenStored,
		Success: func(callbackCtx context.Context, publicID string) {
			_ = s.credentials.Success(callbackCtx, publicID)
		},
		Failure: func(callbackCtx context.Context, publicID, status, code string, cooldown *time.Time) {
			_ = s.credentials.Failure(callbackCtx, publicID, status, code, cooldown)
			if status == storage.CredentialStatusInvalid {
				_ = diagnosticStore.RecordHealth(callbackCtx, publicID, storage.CredentialHealthInvalid, code, storage.CredentialStatusInvalid)
			}
		},
	}
	if refresher, ok := any(s.credentials).(credentialRefresher); ok {
		access.Refresh = func(callbackCtx context.Context, publicID string, material credential.LoginMaterial) error {
			return refresher.Refresh(callbackCtx, publicID, material)
		}
	}

	admission, err := acquireLiveSearch(ctx, ContextSearchRequest{
		Keyword:  keyword,
		Identity: SearchIdentity{Actor: SearchActorAdmin, Role: "admin", AuthType: "web"},
	})
	if err != nil {
		return CredentialDiagnosticResult{}, err
	}
	defer admission.Release()

	timeout := 30 * time.Second
	if config.AppConfig != nil && config.AppConfig.PluginTimeout > 0 {
		timeout = config.AppConfig.PluginTimeout
	}
	startedAt := time.Now()
	ext := map[string]interface{}{
		"diagnostic":             true,
		"refresh":                true,
		"credential_cache_scope": "diagnostic:" + candidate.PublicID,
	}
	tasks := []searchscheduler.Task{{Source: "plugin:" + candidate.PluginKey, Run: func(taskCtx context.Context) searchscheduler.Result {
		results, succeeded, searchErr := searcher.SearchCredentialLayer(taskCtx, keyword, ext, []storage.PluginCredential{candidate}, access)
		if errors.Is(searchErr, credential.ErrNoResults) {
			return searchscheduler.Result{Value: []model.SearchResult{}, ResultCount: 0, UniqueCount: 0}
		}
		if searchErr != nil {
			return searchscheduler.Result{Err: searchErr, ResultCount: len(results), UniqueCount: len(results)}
		}
		if !succeeded {
			return searchscheduler.Result{Err: fmt.Errorf("%w: credential search did not complete", ErrCredentialDiagnosticUnsupported)}
		}
		return searchscheduler.Result{Value: results, ResultCount: len(results), UniqueCount: len(results)}
	}}}
	outcomes := GlobalSearchScheduler().Execute(ctx, searchscheduler.ClassCredential, 1, timeout, tasks)
	if len(outcomes) == 0 {
		if err := ctx.Err(); err != nil {
			return CredentialDiagnosticResult{}, err
		}
		return CredentialDiagnosticResult{}, context.DeadlineExceeded
	}
	if outcomes[0].Err != nil {
		return CredentialDiagnosticResult{}, outcomes[0].Err
	}
	results, _ := outcomes[0].Value.([]model.SearchResult)
	if results == nil {
		results = []model.SearchResult{}
	}
	results = rankSearchResults(results, keyword)
	_ = diagnosticStore.RecordHealth(ctx, candidate.PublicID, storage.CredentialHealthHealthy, "", storage.CredentialStatusActive)
	merged := mergeResultsByType(results, keyword, nil)
	total := 0
	for _, links := range merged {
		total += len(links)
	}
	return CredentialDiagnosticResult{
		CredentialID: candidate.PublicID,
		PluginKey:    candidate.PluginKey,
		DurationMS:   time.Since(startedAt).Milliseconds(),
		Total:        total,
		Results:      results,
		MergedByType: merged,
		Completion:   model.SearchCompletionComplete,
	}, nil
}

func (s *SearchService) diagnosticPluginManager() (*plugin.PluginManager, func(), error) {
	if s.snapshots == nil {
		if s.pluginManager == nil {
			return nil, func() {}, ErrCredentialDiagnosticUnavailable
		}
		return s.pluginManager, func() {}, nil
	}
	lease, err := s.snapshots.Acquire()
	if err != nil {
		return nil, func() {}, err
	}
	snapshot := lease.Snapshot()
	if snapshot == nil || snapshot.PluginManager == nil {
		lease.Release()
		return nil, func() {}, ErrCredentialDiagnosticUnavailable
	}
	return snapshot.PluginManager, lease.Release, nil
}
