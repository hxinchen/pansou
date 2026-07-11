package panlian

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

func (p *PanlianPlugin) ParseLegacyCredential(data []byte) (credential.LoginMaterial, error) {
	var user User
	if err := json.Unmarshal(data, &user); err != nil {
		return credential.LoginMaterial{}, err
	}
	if strings.TrimSpace(user.Username) == "" || strings.TrimSpace(user.Cookie) == "" {
		return credential.LoginMaterial{}, errors.New("legacy panlian credential is incomplete")
	}
	password := ""
	var err error
	if user.EncryptedPassword != "" {
		password, err = p.decryptPassword(user.EncryptedPassword)
		if err != nil {
			return credential.LoginMaterial{}, fmt.Errorf("decrypt legacy panlian password: %w", err)
		}
	}
	secret, err := json.Marshal(tenantSecret{Username: user.Username, Password: password, Cookie: user.Cookie})
	if err != nil {
		return credential.LoginMaterial{}, err
	}
	return credential.LoginMaterial{Secret: secret, StableID: []byte(strings.ToLower(user.Username)), DisplayName: user.Username, PublicMetadata: map[string]any{"account_hint": user.Username}, Status: legacyPanlianStatus(user.Status, user.ExpireAt), ExpiresAt: legacyPanlianExpiry(user.ExpireAt)}, nil
}

func legacyPanlianExpiry(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func legacyPanlianStatus(status string, expires time.Time) string {
	if !expires.IsZero() && !expires.After(time.Now()) {
		return storage.CredentialStatusExpired
	}
	if strings.EqualFold(strings.TrimSpace(status), "active") || strings.TrimSpace(status) == "" {
		return storage.CredentialStatusActive
	}
	return storage.CredentialStatusInvalid
}

func (p *PanlianPlugin) SetManagedCredentialMode(enabled bool) { p.managedCredentials = enabled }

func (p *PanlianPlugin) ApplyRuntimeConfig(values map[string]any) error {
	blocked := make([]string, 0)
	if raw, exists := values["blocked_pan_types"]; exists {
		switch items := raw.(type) {
		case []string:
			blocked = append(blocked, items...)
		case []any:
			for _, item := range items {
				if value := strings.TrimSpace(fmt.Sprint(item)); value != "" {
					blocked = append(blocked, value)
				}
			}
		default:
			return errors.New("blocked_pan_types must be an array")
		}
	}
	p.mu.Lock()
	p.config.BlockedPanTypes = blocked
	p.config.UpdatedAt = time.Now()
	p.mu.Unlock()
	return nil
}

func (p *PanlianPlugin) LoginWithPassword(ctx context.Context, username, password string) (credential.LoginMaterial, error) {
	select {
	case <-ctx.Done():
		return credential.LoginMaterial{}, ctx.Err()
	default:
	}
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return credential.LoginMaterial{}, errors.New("username and password are required")
	}
	cookie, response, err := p.doLogin(username, password, true)
	if err != nil {
		return credential.LoginMaterial{}, err
	}
	secret, err := json.Marshal(tenantSecret{Username: username, Password: password, Cookie: cookie})
	if err != nil {
		return credential.LoginMaterial{}, err
	}
	display := username
	if response != nil && strings.TrimSpace(response.User.Username) != "" {
		display = response.User.Username
	}
	expires := time.Now().Add(30 * 24 * time.Hour)
	return credential.LoginMaterial{Secret: secret, StableID: []byte(strings.ToLower(username)), DisplayName: display, PublicMetadata: map[string]any{"account_hint": username}, ExpiresAt: &expires}, nil
}

func (p *PanlianPlugin) SearchCredentialLayer(ctx context.Context, keyword string, _ map[string]interface{}, candidates []storage.PluginCredential, access credential.Access) ([]model.SearchResult, bool, error) {
	var lastErr error
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
		err = json.Unmarshal(plaintext, &secret)
		for index := range plaintext {
			plaintext[index] = 0
		}
		if err != nil {
			lastErr = err
			reportFailure(ctx, access, candidate.PublicID, storage.CredentialStatusInvalid, "credential_payload_invalid", nil)
			continue
		}
		user := &User{Hash: candidate.PublicID, Username: secret.Username, Cookie: secret.Cookie, Status: "active", LastAccessAt: time.Now()}
		results, err := p.searchOnce(nil, user, keyword)
		if errors.Is(err, errLoginRequired) && secret.Username != "" && secret.Password != "" {
			cookie, _, loginErr := p.doLogin(secret.Username, secret.Password, true)
			if loginErr == nil {
				user.Cookie = cookie
				results, err = p.searchOnce(nil, user, keyword)
			} else {
				err = loginErr
			}
		}
		if err == nil {
			if access.Success != nil {
				access.Success(ctx, candidate.PublicID)
			}
			return plugin.FilterResultsByKeyword(results, keyword), true, nil
		}
		lastErr = err
		status, code := storage.CredentialStatusActive, "upstream_error"
		if errors.Is(err, errLoginRequired) || strings.Contains(strings.ToLower(err.Error()), "登录") {
			status, code = storage.CredentialStatusInvalid, "auth_failed"
		}
		reportFailure(ctx, access, candidate.PublicID, status, code, nil)
	}
	return nil, false, lastErr
}

func reportFailure(ctx context.Context, access credential.Access, id, status, code string, cooldown *time.Time) {
	if access.Failure != nil {
		access.Failure(ctx, id, status, code, cooldown)
	}
}

var _ plugin.ManagedCredentialPlugin = (*PanlianPlugin)(nil)
var _ plugin.RuntimeConfigurablePlugin = (*PanlianPlugin)(nil)
var _ credential.PasswordLoginAdapter = (*PanlianPlugin)(nil)
var _ credential.LayerSearcher = (*PanlianPlugin)(nil)
var _ credential.LegacyCredentialParser = (*PanlianPlugin)(nil)
