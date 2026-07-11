package gying

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"pansou/credential"
	"pansou/model"
	"pansou/plugin"
	"pansou/storage"
)

type tenantSecret struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Cookie   string `json:"cookie"`
}

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
	_, cookie, err := p.doLogin(username, password)
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

func (p *GyingPlugin) SearchCredentialLayer(ctx context.Context, keyword string, _ map[string]interface{}, candidates []storage.PluginCredential, access credential.Access) ([]model.SearchResult, bool, error) {
	var lastErr error
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
		var scraperErr error
		scraper, scraperErr := p.createScraperWithCookies(secret.Cookie)
		if scraperErr != nil && secret.Username != "" && secret.Password != "" {
			scraper, secret.Cookie, scraperErr = p.doLogin(secret.Username, secret.Password)
		}
		if scraperErr != nil {
			lastErr = scraperErr
			gyingFailure(ctx, access, candidate.PublicID, storage.CredentialStatusInvalid, "auth_failed")
			continue
		}
		results, searchErr := p.searchWithScraper(keyword, scraper)
		if searchErr != nil && strings.Contains(searchErr.Error(), "403") && secret.Password != "" {
			scraper, _, scraperErr = p.doLogin(secret.Username, secret.Password)
			if scraperErr == nil {
				results, searchErr = p.searchWithScraper(keyword, scraper)
			} else {
				searchErr = scraperErr
			}
		}
		if searchErr == nil {
			if access.Success != nil {
				access.Success(ctx, candidate.PublicID)
			}
			return p.deduplicateResults(results), true, nil
		}
		lastErr = searchErr
		status, code := storage.CredentialStatusActive, "upstream_error"
		if strings.Contains(searchErr.Error(), "403") || strings.Contains(strings.ToLower(searchErr.Error()), "登录") {
			status, code = storage.CredentialStatusInvalid, "auth_failed"
		}
		gyingFailure(ctx, access, candidate.PublicID, status, code)
	}
	return nil, false, lastErr
}

func gyingFailure(ctx context.Context, access credential.Access, id, status, code string) {
	if access.Failure != nil {
		access.Failure(ctx, id, status, code, nil)
	}
}

var _ plugin.ManagedCredentialPlugin = (*GyingPlugin)(nil)
var _ plugin.RuntimeConfigurablePlugin = (*GyingPlugin)(nil)
var _ credential.PasswordLoginAdapter = (*GyingPlugin)(nil)
var _ credential.LayerSearcher = (*GyingPlugin)(nil)
var _ credential.LegacyCredentialParser = (*GyingPlugin)(nil)
