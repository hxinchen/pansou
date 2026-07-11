package weibo

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"pansou/credential"
	"pansou/model"
	"pansou/plugin"
	"pansou/storage"
)

const (
	weiboErrorAuth     = "auth"
	weiboErrorRate     = "rate"
	weiboErrorUpstream = "upstream"
)

type tenantSecret struct {
	Cookie      string    `json:"cookie"`
	UserIDs     []string  `json:"user_ids,omitempty"`
	Status      string    `json:"status,omitempty"`
	LastRefresh time.Time `json:"last_refresh,omitempty"`
}

type qrState struct {
	Signature string
}

type tenantAccount struct {
	publicID string
	cookie   string
	userIDs  []string
}

type tenantUserTask struct {
	userID   string
	accounts []*tenantAccount
}

type tenantTaskResult struct {
	results   []model.SearchResult
	succeeded bool
	err       error
}

type weiboUpstreamError struct {
	kind       string
	statusCode int
}

func (e *weiboUpstreamError) Error() string {
	switch e.kind {
	case weiboErrorAuth:
		return "weibo authentication failed"
	case weiboErrorRate:
		return "weibo upstream rate limited"
	default:
		if e.statusCode > 0 {
			return fmt.Sprintf("weibo upstream returned HTTP %d", e.statusCode)
		}
		return "weibo upstream request failed"
	}
}

func (p *WeiboPlugin) SetManagedCredentialMode(enabled bool) { p.managedCredentials = enabled }

func (p *WeiboPlugin) BeginQRLogin(ctx context.Context) (credential.QRBeginResult, error) {
	if err := ctx.Err(); err != nil {
		return credential.QRBeginResult{}, err
	}
	data, signature, err := p.generateQRCodeWithSig()
	if err != nil {
		return credential.QRBeginResult{}, err
	}
	expiresAt := time.Now().Add(2 * time.Minute)
	return credential.QRBeginResult{
		State:      qrState{Signature: signature},
		QRCodeData: "data:image/png;base64," + base64.StdEncoding.EncodeToString(data),
		ExpiresAt:  expiresAt,
	}, nil
}

func (p *WeiboPlugin) PollQRLogin(ctx context.Context, value any) (credential.QRPollResult, error) {
	state, ok := value.(qrState)
	if !ok || strings.TrimSpace(state.Signature) == "" {
		return credential.QRPollResult{}, errors.New("invalid weibo login state")
	}
	if err := ctx.Err(); err != nil {
		return credential.QRPollResult{}, err
	}
	result, err := p.checkQRLoginStatus(state.Signature)
	if err != nil {
		return credential.QRPollResult{Status: "failed", Message: "微博登录状态检查失败"}, nil
	}
	switch result.Status {
	case "success":
		if strings.TrimSpace(result.Cookie) == "" {
			return credential.QRPollResult{Status: "failed", Message: "微博登录未返回有效凭证"}, nil
		}
		now := time.Now()
		secret, err := json.Marshal(tenantSecret{Cookie: result.Cookie, Status: "active", LastRefresh: now})
		if err != nil {
			return credential.QRPollResult{}, err
		}
		expiresAt := now.Add(30 * 24 * time.Hour)
		material := &credential.LoginMaterial{
			Secret:         secret,
			StableID:       weiboStableID(result.Cookie),
			DisplayName:    "Weibo account",
			PublicMetadata: map[string]any{"account_hint": "Weibo account", "user_ids": []string{}},
			ExpiresAt:      &expiresAt,
		}
		return credential.QRPollResult{Status: "success", Message: "登录成功", Material: material}, nil
	case "expired":
		return credential.QRPollResult{Status: "expired", Message: "二维码已过期"}, nil
	default:
		message := strings.TrimSpace(result.Message)
		if message == "" {
			message = "等待扫码"
		}
		return credential.QRPollResult{Status: "waiting_scan", Message: message}, nil
	}
}

func (p *WeiboPlugin) ParseLegacyCredential(document []byte) (credential.LoginMaterial, error) {
	if len(document) == 0 {
		return credential.LoginMaterial{}, errors.New("legacy weibo credential is empty")
	}
	var user User
	if err := json.Unmarshal(document, &user); err != nil {
		return credential.LoginMaterial{}, fmt.Errorf("decode legacy weibo credential: %w", err)
	}
	user.Cookie = strings.TrimSpace(user.Cookie)
	if user.Cookie == "" {
		return credential.LoginMaterial{}, errors.New("legacy weibo credential cookie is empty")
	}
	userIDs := normalizeTenantUserIDs(user.UserIDs)
	status := strings.TrimSpace(user.Status)
	if status == "" {
		status = "active"
	}
	secret, err := json.Marshal(tenantSecret{
		Cookie:      user.Cookie,
		UserIDs:     userIDs,
		Status:      status,
		LastRefresh: user.LastRefresh,
	})
	if err != nil {
		return credential.LoginMaterial{}, err
	}
	stableID := []byte(strings.TrimSpace(user.Hash))
	if len(stableID) == 0 {
		stableID = weiboStableID(user.Cookie)
	}
	displayName := "Weibo account"
	if user.Hash != "" {
		displayName += " " + shortTenantID(user.Hash)
	}
	metadata := map[string]any{
		"account_hint":  displayName,
		"user_ids":      userIDs,
		"legacy_status": status,
	}
	material := credential.LoginMaterial{
		Secret:         secret,
		StableID:       stableID,
		DisplayName:    displayName,
		PublicMetadata: metadata,
		Status:         legacyWeiboStatus(status, user.ExpireAt),
	}
	if !user.ExpireAt.IsZero() {
		expiresAt := user.ExpireAt
		material.ExpiresAt = &expiresAt
	}
	return material, nil
}

func legacyWeiboStatus(status string, expiresAt time.Time) string {
	if !expiresAt.IsZero() && !expiresAt.After(time.Now()) {
		return storage.CredentialStatusExpired
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "active":
		return storage.CredentialStatusActive
	case "expired":
		return storage.CredentialStatusExpired
	default:
		return storage.CredentialStatusInvalid
	}
}

func (p *WeiboPlugin) SearchCredentialLayer(ctx context.Context, keyword string, _ map[string]interface{}, candidates []storage.PluginCredential, access credential.Access) ([]model.SearchResult, bool, error) {
	if access.Open == nil {
		return nil, false, errors.New("weibo credential opener is unavailable")
	}
	accounts := make([]*tenantAccount, 0, len(candidates))
	var lastErr error
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		plaintext, err := access.Open(candidate)
		if err != nil {
			lastErr = err
			weiboFailure(ctx, access, candidate.PublicID, storage.CredentialStatusInvalid, "credential_decrypt_failed", nil)
			continue
		}
		var secret tenantSecret
		err = json.Unmarshal(plaintext, &secret)
		for index := range plaintext {
			plaintext[index] = 0
		}
		if err != nil || strings.TrimSpace(secret.Cookie) == "" {
			if err == nil {
				err = errors.New("weibo credential cookie is empty")
			}
			lastErr = err
			weiboFailure(ctx, access, candidate.PublicID, storage.CredentialStatusInvalid, "credential_payload_invalid", nil)
			continue
		}
		if status := strings.ToLower(strings.TrimSpace(secret.Status)); status != "" && status != "active" {
			lastErr = fmt.Errorf("weibo credential status is %s", status)
			if status == "expired" {
				weiboFailure(ctx, access, candidate.PublicID, storage.CredentialStatusExpired, "credential_expired", nil)
			} else {
				weiboFailure(ctx, access, candidate.PublicID, storage.CredentialStatusInvalid, "credential_unavailable", nil)
			}
			continue
		}
		userIDs := append([]string(nil), secret.UserIDs...)
		userIDs = append(userIDs, metadataTenantUserIDs(candidate.PublicMetadata)...)
		userIDs = normalizeTenantUserIDs(userIDs)
		if len(userIDs) == 0 {
			lastErr = errors.New("weibo credential has no configured user ids")
			weiboFailure(ctx, access, candidate.PublicID, storage.CredentialStatusActive, "credential_unavailable", nil)
			continue
		}
		cookie := secret.Cookie
		if !secret.LastRefresh.IsZero() && time.Since(secret.LastRefresh) > time.Hour {
			cookie = p.refreshCookie(cookie)
		}
		accounts = append(accounts, &tenantAccount{publicID: candidate.PublicID, cookie: cookie, userIDs: userIDs})
		if len(accounts) >= MaxConcurrentUsers {
			break
		}
	}
	if len(accounts) == 0 {
		if lastErr == nil {
			lastErr = errors.New("no usable weibo credentials")
		}
		return nil, false, lastErr
	}

	tasks := buildTenantUserTasks(accounts)
	if len(tasks) == 0 {
		return nil, false, errors.New("weibo credentials have no searchable user ids")
	}
	return p.executeTenantUserTasks(ctx, keyword, tasks, access)
}

func buildTenantUserTasks(accounts []*tenantAccount) []tenantUserTask {
	ownersByUserID := make(map[string][]*tenantAccount)
	for _, account := range accounts {
		for _, userID := range account.userIDs {
			ownersByUserID[userID] = append(ownersByUserID[userID], account)
		}
	}
	userIDs := make([]string, 0, len(ownersByUserID))
	for userID := range ownersByUserID {
		userIDs = append(userIDs, userID)
	}
	sort.Strings(userIDs)
	assigned := make(map[string]int, len(accounts))
	tasks := make([]tenantUserTask, 0, len(userIDs))
	for _, userID := range userIDs {
		owners := ownersByUserID[userID]
		primary := 0
		for index := 1; index < len(owners); index++ {
			if assigned[owners[index].publicID] < assigned[owners[primary].publicID] {
				primary = index
			}
		}
		ordered := make([]*tenantAccount, 0, len(owners))
		ordered = append(ordered, owners[primary])
		ordered = append(ordered, owners[:primary]...)
		ordered = append(ordered, owners[primary+1:]...)
		assigned[owners[primary].publicID]++
		tasks = append(tasks, tenantUserTask{userID: userID, accounts: ordered})
	}
	return tasks
}

func (p *WeiboPlugin) executeTenantUserTasks(ctx context.Context, keyword string, tasks []tenantUserTask, access credential.Access) ([]model.SearchResult, bool, error) {
	semaphore := make(chan struct{}, MaxConcurrentUsers)
	outcomes := make(chan tenantTaskResult, len(tasks))
	var wg sync.WaitGroup
	for _, task := range tasks {
		task := task
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				outcomes <- tenantTaskResult{err: ctx.Err()}
				return
			}
			var lastErr error
			for _, account := range task.accounts {
				results, err := p.searchTenantUser(ctx, task.userID, account.cookie, keyword)
				if err == nil {
					if access.Success != nil {
						access.Success(ctx, account.publicID)
					}
					outcomes <- tenantTaskResult{results: results, succeeded: true}
					return
				}
				lastErr = err
				status, code, cooldown := classifyWeiboFailure(err)
				weiboFailure(ctx, access, account.publicID, status, code, cooldown)
			}
			outcomes <- tenantTaskResult{err: lastErr}
		}()
	}
	wg.Wait()
	close(outcomes)

	allResults := make([]model.SearchResult, 0)
	anySucceeded := false
	errorsByTask := make([]error, 0)
	for outcome := range outcomes {
		allResults = append(allResults, outcome.results...)
		anySucceeded = anySucceeded || outcome.succeeded
		if outcome.err != nil {
			errorsByTask = append(errorsByTask, outcome.err)
		}
	}
	if anySucceeded {
		return allResults, true, nil
	}
	return nil, false, errors.Join(errorsByTask...)
}

func (p *WeiboPlugin) searchTenantUser(ctx context.Context, userID, cookie, keyword string) ([]model.SearchResult, error) {
	if p.credentialSearch != nil {
		return p.credentialSearch(ctx, userID, cookie, keyword)
	}
	return p.searchUserWeiboContext(ctx, userID, cookie, keyword)
}

func newWeiboUpstreamError(statusCode int, _ string) error {
	kind := weiboErrorUpstream
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		kind = weiboErrorAuth
	} else if statusCode == http.StatusTooManyRequests {
		kind = weiboErrorRate
	}
	return &weiboUpstreamError{kind: kind, statusCode: statusCode}
}

func weiboAPIResponseError(message string) error {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" || strings.Contains(lower, "暂无") || strings.Contains(lower, "没有") || strings.Contains(lower, "empty") || strings.Contains(lower, "not found") {
		return nil
	}
	if strings.Contains(lower, "登录") || strings.Contains(lower, "login") || strings.Contains(lower, "身份") || strings.Contains(lower, "cookie") {
		return &weiboUpstreamError{kind: weiboErrorAuth, statusCode: http.StatusOK}
	}
	if strings.Contains(lower, "频繁") || strings.Contains(lower, "限流") || strings.Contains(lower, "rate") {
		return &weiboUpstreamError{kind: weiboErrorRate, statusCode: http.StatusOK}
	}
	return &weiboUpstreamError{kind: weiboErrorUpstream, statusCode: http.StatusOK}
}

func classifyWeiboFailure(err error) (string, string, *time.Time) {
	status, code := storage.CredentialStatusActive, "upstream_error"
	var upstream *weiboUpstreamError
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		code = "upstream_timeout"
	case errors.As(err, &upstream) && upstream.kind == weiboErrorAuth:
		status, code = storage.CredentialStatusInvalid, "auth_failed"
	case errors.As(err, &upstream) && upstream.kind == weiboErrorRate:
		code = "rate_limited"
		cooldown := time.Now().Add(5 * time.Minute)
		return status, code, &cooldown
	}
	return status, code, nil
}

func weiboFailure(ctx context.Context, access credential.Access, id, status, code string, cooldown *time.Time) {
	if access.Failure != nil {
		access.Failure(ctx, id, status, code, cooldown)
	}
}

func metadataTenantUserIDs(metadata map[string]any) []string {
	if len(metadata) == 0 {
		return nil
	}
	value, exists := metadata["user_ids"]
	if !exists {
		return nil
	}
	switch items := value.(type) {
	case []string:
		return append([]string(nil), items...)
	case []any:
		result := make([]string, 0, len(items))
		for _, item := range items {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	case string:
		return strings.Split(items, ",")
	default:
		return nil
	}
}

func normalizeTenantUserIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func weiboStableID(cookie string) []byte {
	values := make(map[string]string)
	for _, part := range strings.Split(cookie, ";") {
		key, value, exists := strings.Cut(strings.TrimSpace(part), "=")
		if exists && key != "" && value != "" {
			values[key] = value
		}
	}
	for _, key := range []string{"SUB", "SCF", "SSOLoginState"} {
		if value := values[key]; value != "" {
			digest := sha256.Sum256([]byte(key + ":" + value))
			encoded := make([]byte, hex.EncodedLen(len(digest)))
			hex.Encode(encoded, digest[:])
			return encoded
		}
	}
	digest := sha256.Sum256([]byte(cookie))
	encoded := make([]byte, hex.EncodedLen(len(digest)))
	hex.Encode(encoded, digest[:])
	return encoded
}

func shortTenantID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 8 {
		return value[:8]
	}
	return value
}

var _ plugin.ManagedCredentialPlugin = (*WeiboPlugin)(nil)
var _ credential.QRLoginAdapter = (*WeiboPlugin)(nil)
var _ credential.LayerSearcher = (*WeiboPlugin)(nil)
var _ credential.LegacyCredentialParser = (*WeiboPlugin)(nil)
