package qqpd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"pansou/credential"
	"pansou/model"
	"pansou/plugin"
	"pansou/storage"
)

type tenantSecret struct {
	Cookie          string            `json:"cookie"`
	QQMasked        string            `json:"qq_masked"`
	Channels        []string          `json:"channels,omitempty"`
	ChannelGuildIDs map[string]string `json:"channel_guild_ids,omitempty"`
}

type qrState struct{ Signature string }

func (p *QQPDPlugin) ParseLegacyCredential(data []byte) (credential.LoginMaterial, error) {
	var user User
	if err := json.Unmarshal(data, &user); err != nil {
		return credential.LoginMaterial{}, err
	}
	if strings.TrimSpace(user.Cookie) == "" {
		return credential.LoginMaterial{}, errors.New("legacy qq credential has no cookie")
	}
	stable := strings.TrimSpace(user.QQMasked)
	if stable == "" {
		stable = strings.TrimSpace(user.Hash)
	}
	if stable == "" {
		return credential.LoginMaterial{}, errors.New("legacy qq credential has no stable identity")
	}
	secret, err := json.Marshal(tenantSecret{Cookie: user.Cookie, QQMasked: user.QQMasked, Channels: user.Channels, ChannelGuildIDs: user.ChannelGuildIDs})
	if err != nil {
		return credential.LoginMaterial{}, err
	}
	metadata := map[string]any{"account_hint": user.QQMasked, "channels": append([]string(nil), user.Channels...)}
	return credential.LoginMaterial{Secret: secret, StableID: []byte(stable), DisplayName: "QQ " + user.QQMasked, PublicMetadata: metadata, Status: legacyQQStatus(user.Status, user.ExpireAt), ExpiresAt: legacyQQExpiry(user.ExpireAt)}, nil
}

func legacyQQExpiry(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func legacyQQStatus(status string, expires time.Time) string {
	if !expires.IsZero() && !expires.After(time.Now()) {
		return storage.CredentialStatusExpired
	}
	if strings.EqualFold(strings.TrimSpace(status), "active") || strings.TrimSpace(status) == "" {
		return storage.CredentialStatusActive
	}
	return storage.CredentialStatusInvalid
}

func (p *QQPDPlugin) SetManagedCredentialMode(enabled bool) { p.managedCredentials = enabled }

func (p *QQPDPlugin) BeginQRLogin(ctx context.Context) (credential.QRBeginResult, error) {
	select {
	case <-ctx.Done():
		return credential.QRBeginResult{}, ctx.Err()
	default:
	}
	data, signature, err := p.generateQRCodeWithSig()
	if err != nil {
		return credential.QRBeginResult{}, err
	}
	expires := time.Now().Add(2 * time.Minute)
	return credential.QRBeginResult{State: qrState{Signature: signature}, QRCodeData: "data:image/png;base64," + base64.StdEncoding.EncodeToString(data), ExpiresAt: expires}, nil
}

func (p *QQPDPlugin) PollQRLogin(ctx context.Context, value any) (credential.QRPollResult, error) {
	state, ok := value.(qrState)
	if !ok || state.Signature == "" {
		return credential.QRPollResult{}, errors.New("invalid qq login state")
	}
	select {
	case <-ctx.Done():
		return credential.QRPollResult{}, ctx.Err()
	default:
	}
	result, err := p.checkQRLoginStatus(state.Signature)
	if err != nil {
		return credential.QRPollResult{Status: "failed", Message: err.Error()}, nil
	}
	switch result.Status {
	case "waiting":
		return credential.QRPollResult{Status: "waiting_scan", Message: "等待扫码"}, nil
	case "expired":
		return credential.QRPollResult{Status: "expired", Message: "二维码已过期"}, nil
	case "success":
		secret, err := json.Marshal(tenantSecret{Cookie: result.Cookie, QQMasked: result.QQMasked})
		if err != nil {
			return credential.QRPollResult{}, err
		}
		stable := result.QQMasked
		if cookies := parseCookieString(result.Cookie); cookies["uin"] != "" {
			stable = cookies["uin"]
		}
		expires := time.Now().Add(48 * time.Hour)
		material := &credential.LoginMaterial{Secret: secret, StableID: []byte(stable), DisplayName: "QQ " + result.QQMasked, PublicMetadata: map[string]any{"account_hint": result.QQMasked, "channels": []string{}}, ExpiresAt: &expires}
		return credential.QRPollResult{Status: "success", Message: "登录成功", Material: material}, nil
	default:
		return credential.QRPollResult{Status: "waiting_scan", Message: "等待扫码"}, nil
	}
}

func (p *QQPDPlugin) SearchCredentialLayer(ctx context.Context, keyword string, _ map[string]interface{}, candidates []storage.PluginCredential, access credential.Access) ([]model.SearchResult, bool, error) {
	users := make([]*User, 0, len(candidates))
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		plaintext, err := access.Open(candidate)
		if err != nil {
			qqFailure(ctx, access, candidate.PublicID, storage.CredentialStatusInvalid, "credential_decrypt_failed")
			continue
		}
		var secret tenantSecret
		err = json.Unmarshal(plaintext, &secret)
		for index := range plaintext {
			plaintext[index] = 0
		}
		if err != nil || secret.Cookie == "" {
			qqFailure(ctx, access, candidate.PublicID, storage.CredentialStatusInvalid, "credential_payload_invalid")
			continue
		}
		channels := append([]string(nil), secret.Channels...)
		if raw, ok := candidate.PublicMetadata["channels"].([]any); ok {
			for _, item := range raw {
				value, stringValue := item.(string)
				if stringValue && strings.TrimSpace(value) != "" {
					value = strings.TrimSpace(value)
					channels = append(channels, value)
				}
			}
		}
		if raw, ok := candidate.PublicMetadata["channels"].([]string); ok {
			channels = append(channels, raw...)
		}
		users = append(users, &User{Hash: candidate.PublicID, QQMasked: secret.QQMasked, Cookie: secret.Cookie, Status: "active", Channels: channels, ChannelGuildIDs: secret.ChannelGuildIDs, LastAccessAt: time.Now()})
		ids = append(ids, candidate.PublicID)
	}
	if len(users) == 0 {
		return nil, false, errors.New("no usable qq credentials")
	}
	tasks := p.buildChannelTasks(users)
	if len(tasks) == 0 {
		return nil, false, errors.New("qq credentials have no configured channels")
	}
	results := p.executeTasks(tasks, keyword)
	for _, id := range ids {
		if access.Success != nil {
			access.Success(ctx, id)
		}
	}
	return results, true, nil
}

func qqFailure(ctx context.Context, access credential.Access, id, status, code string) {
	if access.Failure != nil {
		access.Failure(ctx, id, status, code, nil)
	}
}

var _ plugin.ManagedCredentialPlugin = (*QQPDPlugin)(nil)
var _ credential.QRLoginAdapter = (*QQPDPlugin)(nil)
var _ credential.LayerSearcher = (*QQPDPlugin)(nil)
var _ credential.LegacyCredentialParser = (*QQPDPlugin)(nil)
