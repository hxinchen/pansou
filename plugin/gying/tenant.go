package gying

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"pansou/config"
	"pansou/credential"
	"pansou/model"
	"pansou/plugin"
	"pansou/storage"

	cloudscraper "github.com/Advik-B/cloudscraper/lib"
)

type tenantSecret struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Cookie   string `json:"cookie"`
}

var (
	errGyingAuthenticationRequired = errors.New("gying authentication required")
	errGyingCredentialsRejected    = errors.New("gying credentials rejected")
)

func (p *GyingPlugin) ParseLegacyCredential(data []byte) (credential.LoginMaterial, error) {
	var user User
	if err := json.Unmarshal(data, &user); err != nil {
		return credential.LoginMaterial{}, err
	}
	if strings.TrimSpace(user.Username) == "" || strings.TrimSpace(user.Cookie) == "" {
		return credential.LoginMaterial{}, errors.New("legacy gying credential is incomplete")
	}
	password := ""
	var err error
	if user.EncryptedPassword != "" {
		password, err = p.decryptPassword(user.EncryptedPassword)
		if err != nil {
			return credential.LoginMaterial{}, fmt.Errorf("decrypt legacy gying password: %w", err)
		}
	}
	secret, err := json.Marshal(tenantSecret{Username: user.Username, Password: password, Cookie: user.Cookie})
	if err != nil {
		return credential.LoginMaterial{}, err
	}
	return credential.LoginMaterial{Secret: secret, StableID: []byte(strings.ToLower(user.Username)), DisplayName: user.Username, PublicMetadata: map[string]any{"account_hint": user.Username}, ConfigBinding: []byte(p.getBaseURL()), Status: legacyGyingStatus(user.Status, user.ExpireAt), ExpiresAt: legacyGyingExpiry(user.ExpireAt)}, nil
}

func legacyGyingExpiry(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func legacyGyingStatus(status string, expires time.Time) string {
	if !expires.IsZero() && !expires.After(time.Now()) {
		return storage.CredentialStatusExpired
	}
	if strings.EqualFold(strings.TrimSpace(status), "active") || strings.TrimSpace(status) == "" {
		return storage.CredentialStatusActive
	}
	return storage.CredentialStatusInvalid
}

func (p *GyingPlugin) SetManagedCredentialMode(enabled bool) { p.managedCredentials = enabled }

func (p *GyingPlugin) ApplyRuntimeConfig(values map[string]any) error {
	baseURL := DefaultGyingBaseURL
	if raw, exists := values["base_url"]; exists {
		baseURL = strings.TrimSpace(fmt.Sprint(raw))
	}
	normalized, err := normalizeBaseURL(baseURL)
	if err != nil {
		return err
	}
	p.setBaseURL(normalized)
	return nil
}

func (p *GyingPlugin) LoginWithPassword(ctx context.Context, username, password string) (credential.LoginMaterial, error) {
	select {
	case <-ctx.Done():
		return credential.LoginMaterial{}, ctx.Err()
	default:
	}
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return credential.LoginMaterial{}, errors.New("username and password are required")
	}
	_, cookie, err := p.doLoginContext(ctx, username, password)
	if err != nil {
		return credential.LoginMaterial{}, err
	}
	secret, err := json.Marshal(tenantSecret{Username: username, Password: password, Cookie: cookie})
	if err != nil {
		return credential.LoginMaterial{}, err
	}
	expires := time.Now().AddDate(0, 4, 0)
	return credential.LoginMaterial{Secret: secret, StableID: []byte(strings.ToLower(username)), DisplayName: username, PublicMetadata: map[string]any{"account_hint": username}, ConfigBinding: []byte(p.getBaseURL()), ExpiresAt: &expires}, nil
}

type managedSession struct {
	scraper   *cloudscraper.Scraper
	revision  int64
	expiresAt time.Time
}

type managedSearchCacheEntry struct {
	outcome    gyingSearchOutcome
	freshUntil time.Time
	staleUntil time.Time
	lastUsed   int64
}

const (
	managedSearchCacheMaxEntries = 512
	managedStateCleanupInterval  = time.Minute
)

type managedSearchFlightValue struct {
	outcome gyingSearchOutcome
	err     error
}

type managedLoginValue struct {
	scraper *cloudscraper.Scraper
	cookie  string
}

type gyingPartialError struct {
	stats gyingSearchStats
	cause error
}

func (e *gyingPartialError) Error() string {
	message := fmt.Sprintf("gying 详情仅完成 %d/%d 条，失败 %d 条", e.stats.Succeeded, e.stats.Attempted, e.stats.Failed)
	if e.cause != nil {
		message += ": " + e.cause.Error()
	}
	return message
}

func (e *gyingPartialError) Unwrap() error { return e.cause }

func (e *gyingPartialError) SourceStatus() model.SourceStatus {
	return model.SourceStatus{
		Completion: model.SearchCompletionPartial,
		Candidates: e.stats.Candidates,
		Attempted:  e.stats.Attempted,
		Succeeded:  e.stats.Succeeded,
		Failed:     e.stats.Failed,
		Message:    e.Error(),
	}
}

func (p *GyingPlugin) SearchCredentialLayer(ctx context.Context, keyword string, ext map[string]interface{}, candidates []storage.PluginCredential, access credential.Access) ([]model.SearchResult, bool, error) {
	var lastErr error
	sawHealthyZero := false
	zeroSearches := 0
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		plaintext, err := access.Open(candidate)
		if err != nil {
			lastErr = err
			gyingFailure(ctx, access, candidate.PublicID, storage.CredentialStatusInvalid, "credential_decrypt_failed")
			continue
		}
		var secret tenantSecret
		err = json.Unmarshal(plaintext, &secret)
		for index := range plaintext {
			plaintext[index] = 0
		}
		if err != nil {
			lastErr = err
			gyingFailure(ctx, access, candidate.PublicID, storage.CredentialStatusInvalid, "credential_payload_invalid")
			continue
		}
		outcome, searchErr := p.searchManagedCredential(ctx, keyword, ext, candidate, secret, access)
		if searchErr == nil && outcome.Complete {
			if access.Success != nil {
				access.Success(ctx, candidate.PublicID)
			}
			if len(outcome.Results) > 0 {
				return p.deduplicateResults(outcome.Results), true, nil
			}
			sawHealthyZero = true
			zeroSearches++
			// A healthy empty response can be an upstream session anomaly. Try
			// one additional credential before asking the service for the shared
			// layer.
			if zeroSearches >= 2 {
				return nil, false, credential.ErrNoResults
			}
			continue
		}
		if len(outcome.Results) > 0 {
			partialErr := &gyingPartialError{stats: outcome.Stats, cause: searchErr}
			if access.Success != nil {
				access.Success(ctx, candidate.PublicID)
			}
			return p.deduplicateResults(outcome.Results), true, partialErr
		}
		lastErr = searchErr
		status, code := storage.CredentialStatusActive, "upstream_error"
		if isGyingCredentialRejected(searchErr) {
			status, code = storage.CredentialStatusInvalid, "auth_failed"
		} else if isGyingAuthError(searchErr) {
			code = "session_invalid"
		}
		gyingFailure(ctx, access, candidate.PublicID, status, code)
	}
	if sawHealthyZero {
		return nil, false, credential.ErrNoResults
	}
	return nil, false, lastErr
}

func (p *GyingPlugin) searchManagedCredential(ctx context.Context, keyword string, ext map[string]interface{}, candidate storage.PluginCredential, secret tenantSecret, access credential.Access) (gyingSearchOutcome, error) {
	forceRefresh := false
	cacheScope := "managed"
	if ext != nil {
		forceRefresh, _ = ext["refresh"].(bool)
		if value := strings.TrimSpace(fmt.Sprint(ext["credential_cache_scope"])); value != "" && value != "<nil>" {
			cacheScope = value
		}
	}
	key := p.managedSearchKey(cacheScope, candidate.PublicID, keyword)
	now := time.Now()
	p.scheduleManagedStateCleanup(now)
	var stale *managedSearchCacheEntry
	if !forceRefresh {
		if value, ok := p.managedSearchCache.Load(key); ok {
			entry := value.(*managedSearchCacheEntry)
			switch {
			case now.Before(entry.freshUntil):
				atomic.StoreInt64(&entry.lastUsed, now.UnixNano())
				return cloneGyingOutcome(entry.outcome), nil
			case now.Before(entry.staleUntil):
				stale = entry
				atomic.StoreInt64(&entry.lastUsed, now.UnixNano())
				go p.refreshManagedSearch(key, keyword, candidate, secret, access)
				return cloneGyingOutcome(entry.outcome), nil
			default:
				if p.managedSearchCache.CompareAndDelete(key, entry) {
					p.decrementManagedCacheCount()
				}
			}
		}
	}

	result := p.managedSearchGroup.DoChan(key, func() (interface{}, error) {
		searchCtx, cancel := context.WithTimeout(context.Background(), p.managedSearchTimeout())
		defer cancel()
		outcome, searchErr := p.searchManagedCredentialLive(searchCtx, keyword, candidate, secret, access)
		if searchErr == nil && outcome.Complete {
			p.storeManagedSearchCache(key, outcome)
		}
		return managedSearchFlightValue{outcome: outcome, err: searchErr}, nil
	})
	select {
	case <-ctx.Done():
		return gyingSearchOutcome{}, ctx.Err()
	case shared := <-result:
		if shared.Err != nil {
			return gyingSearchOutcome{}, shared.Err
		}
		value := shared.Val.(managedSearchFlightValue)
		if value.err != nil && stale != nil {
			return cloneGyingOutcome(stale.outcome), nil
		}
		return value.outcome, value.err
	}
}

func (p *GyingPlugin) refreshManagedSearch(key, keyword string, candidate storage.PluginCredential, secret tenantSecret, access credential.Access) {
	result := p.managedSearchGroup.DoChan(key, func() (interface{}, error) {
		ctx, cancel := context.WithTimeout(context.Background(), p.managedSearchTimeout())
		defer cancel()
		outcome, err := p.searchManagedCredentialLive(ctx, keyword, candidate, secret, access)
		if err == nil && outcome.Complete {
			p.storeManagedSearchCache(key, outcome)
		}
		return managedSearchFlightValue{outcome: outcome, err: err}, nil
	})
	<-result
}

func (p *GyingPlugin) searchManagedCredentialLive(ctx context.Context, keyword string, candidate storage.PluginCredential, secret tenantSecret, access credential.Access) (gyingSearchOutcome, error) {
	scraper, err := p.managedScraper(candidate, secret.Cookie)
	if err != nil {
		return gyingSearchOutcome{}, err
	}
	outcome, searchErr := p.searchWithScraperContext(ctx, keyword, scraper)
	if searchErr != nil && isGyingAuthError(searchErr) && secret.Username != "" && secret.Password != "" {
		scraper, err = p.reloginManagedCredential(ctx, candidate, secret, access)
		if err != nil {
			return outcome, err
		}
		outcome, searchErr = p.searchWithScraperContext(ctx, keyword, scraper)
	}
	return outcome, searchErr
}

func (p *GyingPlugin) managedScraper(candidate storage.PluginCredential, cookie string) (*cloudscraper.Scraper, error) {
	key := p.managedSessionKey(candidate.PublicID)
	now := time.Now()
	if value, ok := p.managedSessions.Load(key); ok {
		entry := value.(*managedSession)
		if entry.revision >= candidate.Revision && now.Before(entry.expiresAt) && entry.scraper != nil {
			return entry.scraper, nil
		}
		p.managedSessions.CompareAndDelete(key, entry)
	}
	scraper, err := p.createScraperWithCookies(cookie)
	if err != nil {
		return nil, err
	}
	p.managedSessions.Store(key, &managedSession{scraper: scraper, revision: candidate.Revision, expiresAt: now.Add(managedSessionTTL)})
	return scraper, nil
}

func (p *GyingPlugin) reloginManagedCredential(ctx context.Context, candidate storage.PluginCredential, secret tenantSecret, access credential.Access) (*cloudscraper.Scraper, error) {
	key := p.managedSessionKey(candidate.PublicID)
	result := p.managedLoginGroup.DoChan(key, func() (interface{}, error) {
		if err := ctx.Err(); err != nil {
			return managedLoginValue{}, err
		}
		scraper, cookie, err := p.doLoginContext(ctx, secret.Username, secret.Password)
		if err != nil {
			return managedLoginValue{}, err
		}
		sessionRevision := candidate.Revision
		if access.Refresh != nil {
			payload, marshalErr := json.Marshal(tenantSecret{Username: secret.Username, Password: secret.Password, Cookie: cookie})
			if marshalErr != nil {
				return managedLoginValue{}, marshalErr
			}
			expires := time.Now().AddDate(0, 4, 0)
			refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			refreshErr := access.Refresh(refreshCtx, candidate.PublicID, credential.LoginMaterial{
				Secret: payload, StableID: []byte(strings.ToLower(secret.Username)), DisplayName: candidate.DisplayName,
				PublicMetadata: candidate.PublicMetadata, ConfigBinding: []byte(p.getBaseURL()),
				Status: storage.CredentialStatusActive, ExpiresAt: &expires,
			})
			cancel()
			if refreshErr != nil {
				return managedLoginValue{}, fmt.Errorf("持久化刷新Cookie失败: %w", refreshErr)
			}
			sessionRevision++
		}
		p.managedSessions.Store(key, &managedSession{scraper: scraper, revision: sessionRevision, expiresAt: time.Now().Add(managedSessionTTL)})
		return managedLoginValue{scraper: scraper, cookie: cookie}, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case shared := <-result:
		if shared.Err != nil {
			return nil, shared.Err
		}
		return shared.Val.(managedLoginValue).scraper, nil
	}
}

func (p *GyingPlugin) managedSessionKey(publicID string) string {
	return p.getBaseURL() + "\x00" + strings.TrimSpace(publicID)
}

func (p *GyingPlugin) managedSearchTimeout() time.Duration {
	if config.AppConfig != nil && config.AppConfig.PluginTimeout > 0 {
		return config.AppConfig.PluginTimeout
	}
	return 45 * time.Second
}

func (p *GyingPlugin) managedSearchKey(scope, publicID, keyword string) string {
	return strings.Join([]string{
		strings.TrimSpace(scope), p.getBaseURL(), strings.TrimSpace(publicID), strings.ToLower(strings.TrimSpace(keyword)),
	}, "\x00")
}

func (p *GyingPlugin) storeManagedSearchCache(key string, outcome gyingSearchOutcome) {
	now := time.Now()
	entry := &managedSearchCacheEntry{
		outcome: cloneGyingOutcome(outcome), freshUntil: now.Add(managedCacheFreshTTL), staleUntil: now.Add(managedCacheStaleTTL), lastUsed: now.UnixNano(),
	}
	if _, loaded := p.managedSearchCache.LoadOrStore(key, entry); loaded {
		p.managedSearchCache.Store(key, entry)
	} else {
		atomic.AddInt64(&p.managedCacheCount, 1)
	}
	if atomic.LoadInt64(&p.managedCacheCount) > managedSearchCacheMaxEntries {
		p.trimManagedSearchCache(now)
	}
}

// scheduleManagedStateCleanup keeps request-path work constant. The actual
// sync.Map traversal happens at most once per interval in a background task.
func (p *GyingPlugin) scheduleManagedStateCleanup(now time.Time) {
	p.managedCacheMu.Lock()
	if now.Sub(p.managedCleanupAt) < managedStateCleanupInterval {
		p.managedCacheMu.Unlock()
		return
	}
	p.managedCleanupAt = now
	p.managedCacheMu.Unlock()
	go p.cleanupManagedState(now)
}

func (p *GyingPlugin) cleanupManagedState(now time.Time) {
	p.managedSearchCache.Range(func(key, value interface{}) bool {
		entry, ok := value.(*managedSearchCacheEntry)
		if !ok || !now.Before(entry.staleUntil) {
			if p.managedSearchCache.CompareAndDelete(key, value) {
				p.decrementManagedCacheCount()
			}
		}
		return true
	})
	p.managedSessions.Range(func(key, value interface{}) bool {
		entry, ok := value.(*managedSession)
		if !ok || !now.Before(entry.expiresAt) {
			p.managedSessions.Delete(key)
		}
		return true
	})
}

func (p *GyingPlugin) trimManagedSearchCache(now time.Time) {
	p.managedCacheMu.Lock()
	defer p.managedCacheMu.Unlock()
	type candidate struct {
		key      any
		value    any
		lastUsed int64
	}
	candidates := make([]candidate, 0, atomic.LoadInt64(&p.managedCacheCount))
	p.managedSearchCache.Range(func(key, value any) bool {
		entry, ok := value.(*managedSearchCacheEntry)
		if !ok || !now.Before(entry.staleUntil) {
			if p.managedSearchCache.CompareAndDelete(key, value) {
				p.decrementManagedCacheCount()
			}
			return true
		}
		candidates = append(candidates, candidate{key: key, value: value, lastUsed: atomic.LoadInt64(&entry.lastUsed)})
		return true
	})

	target := managedSearchCacheMaxEntries * 9 / 10
	excess := int(atomic.LoadInt64(&p.managedCacheCount)) - target
	if excess <= 0 {
		return
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].lastUsed < candidates[j].lastUsed })
	for _, entry := range candidates {
		if excess <= 0 {
			break
		}
		if p.managedSearchCache.CompareAndDelete(entry.key, entry.value) {
			p.decrementManagedCacheCount()
			excess--
		}
	}
}

func (p *GyingPlugin) decrementManagedCacheCount() {
	for {
		current := atomic.LoadInt64(&p.managedCacheCount)
		if current <= 0 || atomic.CompareAndSwapInt64(&p.managedCacheCount, current, current-1) {
			return
		}
	}
}

func cloneGyingOutcome(outcome gyingSearchOutcome) gyingSearchOutcome {
	cloned := outcome
	cloned.Results = make([]model.SearchResult, len(outcome.Results))
	for index, result := range outcome.Results {
		cloned.Results[index] = result
		cloned.Results[index].Links = append([]model.Link(nil), result.Links...)
		cloned.Results[index].Tags = append([]string(nil), result.Tags...)
		cloned.Results[index].Images = append([]string(nil), result.Images...)
	}
	return cloned
}

func isGyingAuthError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errGyingAuthenticationRequired) || errors.Is(err, errGyingCredentialsRejected) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "403") || strings.Contains(message, "登录")
}

func isGyingCredentialRejected(err error) bool {
	return err != nil && errors.Is(err, errGyingCredentialsRejected)
}

func (p *GyingPlugin) CheckCredentialHealth(ctx context.Context, candidate storage.PluginCredential, access credential.Access) (credential.HealthCheckResult, error) {
	invalid := func(code string) (credential.HealthCheckResult, error) {
		return credential.HealthCheckResult{
			HealthStatus: storage.CredentialHealthInvalid, CredentialStatus: storage.CredentialStatusInvalid, ErrorCode: code,
		}, nil
	}
	plaintext, err := access.Open(candidate)
	if err != nil {
		return invalid("credential_decrypt_failed")
	}
	var secret tenantSecret
	err = json.Unmarshal(plaintext, &secret)
	for index := range plaintext {
		plaintext[index] = 0
	}
	if err != nil || strings.TrimSpace(secret.Username) == "" {
		return invalid("credential_payload_invalid")
	}

	needsRelogin := candidate.ExpiresAt != nil && !candidate.ExpiresAt.After(time.Now())
	if !needsRelogin {
		scraper, scraperErr := p.managedScraper(candidate, secret.Cookie)
		if scraperErr != nil {
			return credential.HealthCheckResult{HealthStatus: storage.CredentialHealthError, ErrorCode: "health_check_upstream_error"}, scraperErr
		}
		_, probeErr := p.fetchSearchSuggestionsContext(ctx, "电影", scraper)
		if probeErr == nil {
			return credential.HealthCheckResult{HealthStatus: storage.CredentialHealthHealthy, CredentialStatus: storage.CredentialStatusActive}, nil
		}
		if !isGyingAuthError(probeErr) {
			return credential.HealthCheckResult{HealthStatus: storage.CredentialHealthError, ErrorCode: "health_check_upstream_error"}, probeErr
		}
		needsRelogin = true
	}

	if !needsRelogin {
		return credential.HealthCheckResult{HealthStatus: storage.CredentialHealthHealthy, CredentialStatus: storage.CredentialStatusActive}, nil
	}
	if strings.TrimSpace(secret.Password) == "" {
		return invalid("auth_failed")
	}
	scraper, loginErr := p.reloginManagedCredential(ctx, candidate, secret, access)
	if loginErr != nil {
		if isGyingCredentialRejected(loginErr) {
			return invalid("auth_failed")
		}
		return credential.HealthCheckResult{HealthStatus: storage.CredentialHealthError, ErrorCode: "health_check_upstream_error"}, loginErr
	}
	if _, probeErr := p.fetchSearchSuggestionsContext(ctx, "电影", scraper); probeErr != nil {
		return credential.HealthCheckResult{HealthStatus: storage.CredentialHealthError, ErrorCode: "health_check_upstream_error"}, probeErr
	}
	return credential.HealthCheckResult{HealthStatus: storage.CredentialHealthHealthy, CredentialStatus: storage.CredentialStatusActive}, nil
}

func gyingFailure(ctx context.Context, access credential.Access, id, status, code string) {
	if access.Failure != nil {
		access.Failure(ctx, id, status, code, nil)
	}
}

var _ plugin.ManagedCredentialPlugin = (*GyingPlugin)(nil)
var _ plugin.RuntimeConfigurablePlugin = (*GyingPlugin)(nil)
var _ credential.PasswordLoginAdapter = (*GyingPlugin)(nil)
var _ credential.HealthCheckAdapter = (*GyingPlugin)(nil)
var _ credential.LayerSearcher = (*GyingPlugin)(nil)
var _ credential.LegacyCredentialParser = (*GyingPlugin)(nil)
