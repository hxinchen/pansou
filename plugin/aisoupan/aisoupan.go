package aisoupan

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"pansou/config"
	"pansou/credential"
	"pansou/model"
	"pansou/plugin"
	"pansou/storage"
	"pansou/util"
	jsonutil "pansou/util/json"
)

const (
	PluginName        = "aisoupan"
	defaultEndpoint   = "https://api.aisoupan.vip/api/search"
	defaultPriority   = 3
	requestTimeout    = 15 * time.Second
	credentialTTL     = time.Minute
	responseBodyLimit = 16 << 20
	tokenProbeKeyword = "__pansou_token_probe__"
)

var errAuthentication = errors.New("aisoupan authentication failed")

type upstreamError struct {
	status     int
	code       string
	retryAfter time.Duration
}

func (e *upstreamError) Error() string {
	if e == nil {
		return "aisoupan upstream error"
	}
	if e.code != "" {
		return fmt.Sprintf("aisoupan upstream error: http=%d code=%s", e.status, e.code)
	}
	return fmt.Sprintf("aisoupan upstream error: http=%d", e.status)
}

type tenantSecret struct {
	Token string `json:"token"`
}

type tokenClaims struct {
	Username string `json:"username"`
	Issuer   string `json:"iss"`
	Expires  int64  `json:"exp"`
}

type apiEnvelope struct {
	Code    int                  `json:"code"`
	Message string               `json:"message"`
	Data    model.SearchResponse `json:"data"`
}

type cacheEntry struct {
	Results   []model.SearchResult
	ExpiresAt time.Time
}

type AisoupanPlugin struct {
	*plugin.BaseAsyncPlugin
	client      *http.Client
	endpoint    string
	managed     bool
	cacheMu     sync.Mutex
	cache       map[string]cacheEntry
	searchGroup singleflight.Group
}

func init() {
	factory := func() plugin.AsyncSearchPlugin { return NewAisoupanPlugin() }
	plugin.RegisterGlobalPluginFactory(PluginName, factory)
	plugin.RegisterGlobalPlugin(factory())
}

func NewAisoupanPlugin() *AisoupanPlugin {
	proxyURL := ""
	if config.AppConfig != nil {
		proxyURL = config.AppConfig.ProxyURL
	}
	client, err := util.NewHTTPClient(proxyURL)
	if err != nil {
		client, _ = util.NewHTTPClient("")
	}
	if client == nil {
		client = &http.Client{}
	}
	client.Timeout = requestTimeout
	return &AisoupanPlugin{
		BaseAsyncPlugin: plugin.NewBaseAsyncPluginWithFilter(PluginName, defaultPriority, true),
		client:          client,
		endpoint:        defaultEndpoint,
		cache:           make(map[string]cacheEntry),
	}
}

func (p *AisoupanPlugin) SetManagedCredentialMode(enabled bool) { p.managed = enabled }

func (p *AisoupanPlugin) SupportsCredentialScope(scope string) bool {
	return scope == storage.CredentialScopeAdminPrivate || scope == storage.CredentialScopePublicShared
}

func (p *AisoupanPlugin) Search(keyword string, ext map[string]interface{}) ([]model.SearchResult, error) {
	result, err := p.SearchWithResult(keyword, ext)
	if err != nil {
		return nil, err
	}
	return result.Results, nil
}

func (p *AisoupanPlugin) SearchWithResult(keyword string, ext map[string]interface{}) (model.PluginSearchResult, error) {
	return p.AsyncSearchWithResult(keyword, p.searchImpl, p.MainCacheKey, ext)
}

func (p *AisoupanPlugin) searchImpl(_ *http.Client, _ string, _ map[string]interface{}) ([]model.SearchResult, error) {
	// Aisoupan is intentionally credential-only. Database-backed searches use
	// SearchCredentialLayer; legacy mode has no safe place to obtain a token.
	return []model.SearchResult{}, nil
}

func (p *AisoupanPlugin) LoginWithToken(ctx context.Context, token string) (credential.LoginMaterial, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return credential.LoginMaterial{}, storage.ErrInvalid
	}
	claims, _ := parseTokenClaims(token)
	if claims.Expires > 0 && time.Unix(claims.Expires, 0).Before(time.Now()) {
		return credential.LoginMaterial{}, fmt.Errorf("%w: TOKEN 已过期", storage.ErrInvalid)
	}
	if _, err := p.searchRemote(ctx, token, tokenProbeKeyword); err != nil {
		if errors.Is(err, errAuthentication) {
			return credential.LoginMaterial{}, fmt.Errorf("%w: TOKEN 无效或已过期", storage.ErrInvalid)
		}
		return credential.LoginMaterial{}, err
	}
	secret, err := jsonutil.Marshal(tenantSecret{Token: token})
	if err != nil {
		return credential.LoginMaterial{}, err
	}
	stableID := []byte(token)
	displayName := "Aisoupan TOKEN"
	metadata := map[string]any{"provider": PluginName, "account_hint": displayName}
	if username := strings.TrimSpace(claims.Username); username != "" {
		issuer := strings.ToLower(strings.TrimSpace(claims.Issuer))
		stableID = []byte(issuer + ":" + strings.ToLower(username))
		displayName = username
		metadata["account_hint"] = username
	}
	var expiresAt *time.Time
	if claims.Expires > 0 {
		value := time.Unix(claims.Expires, 0)
		expiresAt = &value
	}
	return credential.LoginMaterial{
		Secret: secret, StableID: stableID, DisplayName: displayName,
		PublicMetadata: metadata, Status: storage.CredentialStatusActive, ExpiresAt: expiresAt,
	}, nil
}

func (p *AisoupanPlugin) SearchCredentialLayer(ctx context.Context, keyword string, ext map[string]interface{}, candidates []storage.PluginCredential, access credential.Access) ([]model.SearchResult, bool, error) {
	var lastErr error
	healthyEmpty := false
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		plaintext, err := access.Open(candidate)
		if err != nil {
			lastErr = err
			reportFailure(ctx, access, candidate.PublicID, storage.CredentialStatusInvalid, "credential_decrypt_failed", nil)
			continue
		}
		var secret tenantSecret
		err = jsonutil.Unmarshal(plaintext, &secret)
		clear(plaintext)
		if err != nil || strings.TrimSpace(secret.Token) == "" {
			lastErr = errors.New("aisoupan credential payload is invalid")
			reportFailure(ctx, access, candidate.PublicID, storage.CredentialStatusInvalid, "credential_payload_invalid", nil)
			continue
		}
		results, searchErr := p.searchCredential(ctx, candidate.PublicID, secret.Token, keyword, ext)
		secret.Token = ""
		if searchErr == nil {
			if access.Success != nil {
				access.Success(ctx, candidate.PublicID)
			}
			if len(results) == 0 {
				healthyEmpty = true
				continue
			}
			return results, true, nil
		}
		lastErr = searchErr
		status, code := storage.CredentialStatusActive, "upstream_error"
		var cooldown *time.Time
		if errors.Is(searchErr, errAuthentication) {
			status, code = storage.CredentialStatusInvalid, "auth_failed"
		} else {
			var upstream *upstreamError
			if errors.As(searchErr, &upstream) && upstream.status == http.StatusTooManyRequests {
				code = "rate_limited"
				delay := upstream.retryAfter
				if delay <= 0 {
					delay = 5 * time.Minute
				}
				value := time.Now().Add(delay)
				cooldown = &value
			}
		}
		reportFailure(ctx, access, candidate.PublicID, status, code, cooldown)
	}
	if healthyEmpty {
		return nil, false, credential.ErrNoResults
	}
	return nil, false, lastErr
}

func (p *AisoupanPlugin) CheckCredentialHealth(ctx context.Context, candidate storage.PluginCredential, access credential.Access) (credential.HealthCheckResult, error) {
	plaintext, err := access.Open(candidate)
	if err != nil {
		return credential.HealthCheckResult{HealthStatus: storage.CredentialHealthInvalid, CredentialStatus: storage.CredentialStatusInvalid, ErrorCode: "credential_decrypt_failed"}, nil
	}
	var secret tenantSecret
	err = jsonutil.Unmarshal(plaintext, &secret)
	clear(plaintext)
	if err != nil || strings.TrimSpace(secret.Token) == "" {
		return credential.HealthCheckResult{HealthStatus: storage.CredentialHealthInvalid, CredentialStatus: storage.CredentialStatusInvalid, ErrorCode: "credential_payload_invalid"}, nil
	}
	_, err = p.searchRemote(ctx, secret.Token, tokenProbeKeyword)
	secret.Token = ""
	if err == nil {
		return credential.HealthCheckResult{HealthStatus: storage.CredentialHealthHealthy, CredentialStatus: storage.CredentialStatusActive}, nil
	}
	if errors.Is(err, errAuthentication) {
		return credential.HealthCheckResult{HealthStatus: storage.CredentialHealthInvalid, CredentialStatus: storage.CredentialStatusInvalid, ErrorCode: "auth_failed"}, nil
	}
	return credential.HealthCheckResult{HealthStatus: storage.CredentialHealthError, ErrorCode: "health_check_upstream_error"}, err
}

func (p *AisoupanPlugin) searchCredential(ctx context.Context, publicID, token, keyword string, ext map[string]interface{}) ([]model.SearchResult, error) {
	keyword = strings.TrimSpace(keyword)
	refresh := boolExt(ext, "refresh") || boolExt(ext, "diagnostic") || boolExt(ext, "deep")
	cacheKey := publicID + "\x00" + strings.ToLower(keyword)
	if !refresh {
		if results, ok := p.cached(cacheKey); ok {
			return results, nil
		}
	}
	value, err, _ := p.searchGroup.Do(cacheKey, func() (any, error) {
		if !refresh {
			if results, ok := p.cached(cacheKey); ok {
				return results, nil
			}
		}
		results, requestErr := p.searchRemote(ctx, token, keyword)
		if requestErr != nil {
			return nil, requestErr
		}
		p.storeCache(cacheKey, results)
		return cloneResults(results), nil
	})
	if err != nil {
		return nil, err
	}
	return cloneResults(value.([]model.SearchResult)), nil
}

func (p *AisoupanPlugin) searchRemote(ctx context.Context, token, keyword string) ([]model.SearchResult, error) {
	requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	endpoint, err := url.Parse(p.endpoint)
	if err != nil {
		return nil, err
	}
	query := endpoint.Query()
	query.Set("kw", strings.TrimSpace(keyword))
	query.Set("src", "all")
	query.Set("res", "all")
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, responseBodyLimit))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: http=%d", errAuthentication, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &upstreamError{status: resp.StatusCode, retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	}
	var envelope apiEnvelope
	if err := jsonutil.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("parse aisoupan response: %w", err)
	}
	if envelope.Code != 0 {
		code := strings.TrimSpace(envelope.Message)
		if strings.Contains(strings.ToLower(code), "token") || strings.Contains(code, "授权") {
			return nil, fmt.Errorf("%w: code=%d", errAuthentication, envelope.Code)
		}
		return nil, &upstreamError{status: resp.StatusCode, code: strconv.Itoa(envelope.Code)}
	}
	return normalizeResponse(envelope.Data), nil
}

func normalizeResponse(response model.SearchResponse) []model.SearchResult {
	results := cloneResults(response.Results)
	if len(results) == 0 {
		for panType, links := range response.MergedByType {
			for _, link := range links {
				linkType := strings.TrimSpace(panType)
				if linkType == "" {
					linkType = util.GetLinkType(link.URL)
				}
				results = append(results, model.SearchResult{
					UniqueID:  resultID(link.Note, link.URL),
					SubSource: strings.TrimSpace(link.Source),
					Datetime:  link.Datetime,
					Title:     strings.TrimSpace(link.Note),
					Content:   strings.TrimSpace(link.Note),
					Links:     []model.Link{{Type: linkType, URL: strings.TrimSpace(link.URL), Password: link.Password, Datetime: link.Datetime}},
					Images:    append([]string(nil), link.Images...),
				})
			}
		}
	}
	normalized := make([]model.SearchResult, 0, len(results))
	for _, result := range results {
		if result.SubSource == "" && result.Channel != "" {
			result.SubSource = result.Channel
		}
		result.Channel = ""
		links := make([]model.Link, 0, len(result.Links))
		for _, link := range result.Links {
			link.URL = strings.TrimSpace(link.URL)
			if link.URL == "" {
				continue
			}
			if strings.TrimSpace(link.Type) == "" {
				link.Type = util.GetLinkType(link.URL)
			}
			links = append(links, link)
		}
		if len(links) == 0 {
			continue
		}
		result.Links = links
		result.UniqueID = resultID(result.UniqueID, result.Title, links[0].URL)
		normalized = append(normalized, result)
	}
	return normalized
}

func resultID(parts ...string) string {
	joined := strings.Join(parts, "\x00")
	sum := sha256.Sum256([]byte(joined))
	return PluginName + ":" + hex.EncodeToString(sum[:12])
}

func parseTokenClaims(token string) (tokenClaims, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return tokenClaims{}, errors.New("opaque token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return tokenClaims{}, err
	}
	var claims tokenClaims
	if err := jsonutil.Unmarshal(payload, &claims); err != nil {
		return tokenClaims{}, err
	}
	return claims, nil
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
	}
	return 0
}

func reportFailure(ctx context.Context, access credential.Access, publicID, status, code string, cooldown *time.Time) {
	if access.Failure != nil {
		access.Failure(ctx, publicID, status, code, cooldown)
	}
}

func boolExt(ext map[string]interface{}, key string) bool {
	if ext == nil {
		return false
	}
	value, _ := ext[key].(bool)
	return value
}

func (p *AisoupanPlugin) cached(key string) ([]model.SearchResult, bool) {
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	entry, ok := p.cache[key]
	if !ok || time.Now().After(entry.ExpiresAt) {
		delete(p.cache, key)
		return nil, false
	}
	return cloneResults(entry.Results), true
}

func (p *AisoupanPlugin) storeCache(key string, results []model.SearchResult) {
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	now := time.Now()
	for cachedKey, entry := range p.cache {
		if now.After(entry.ExpiresAt) {
			delete(p.cache, cachedKey)
		}
	}
	p.cache[key] = cacheEntry{Results: cloneResults(results), ExpiresAt: now.Add(credentialTTL)}
}

func cloneResults(values []model.SearchResult) []model.SearchResult {
	result := make([]model.SearchResult, len(values))
	for index, value := range values {
		value.Links = append([]model.Link(nil), value.Links...)
		value.Tags = append([]string(nil), value.Tags...)
		value.Images = append([]string(nil), value.Images...)
		result[index] = value
	}
	return result
}
