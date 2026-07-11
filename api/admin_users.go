package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	accountauth "pansou/auth"
	"pansou/config"
	"pansou/model"
	"pansou/storage"
)

type createUserRequest struct {
	Username          string     `json:"username" binding:"required"`
	Role              string     `json:"role"`
	Enabled           *bool      `json:"enabled"`
	ExpiresAt         *time.Time `json:"expires_at"`
	RPSLimit          int        `json:"rps_limit"`
	RPMLimit          int        `json:"rpm_limit"`
	RateLimitDisabled bool       `json:"rate_limit_disabled"`
}

type adminUserResponse struct {
	storage.User
	HasAPIKey       bool       `json:"has_api_key"`
	APIKeyPrefix    string     `json:"api_key_prefix,omitempty"`
	APIKeyLastUsed  *time.Time `json:"api_key_last_used_at,omitempty"`
	APIKeyRevokedAt *time.Time `json:"api_key_revoked_at,omitempty"`
}

func (h *AdminHandler) listUsers(c *gin.Context) {
	if !h.available(c) {
		return
	}
	var enabled *bool
	if value := strings.TrimSpace(c.Query("enabled")); value != "" {
		parsed := value == "true" || value == "1"
		enabled = &parsed
	}
	page, err := h.store.ListUsers(c.Request.Context(), storage.UserFilter{
		Query:          strings.TrimSpace(c.Query("q")),
		Roles:          queryList(c, "role", "roles"),
		Enabled:        enabled,
		IncludeDeleted: queryBool(c, "include_deleted", false),
		Page:           queryInt(c, "page", 1),
		PageSize:       queryInt(c, "page_size", 30),
	})
	if err != nil {
		respondAdminError(c, err)
		return
	}
	items := make([]adminUserResponse, 0, len(page.Items))
	for _, user := range page.Items {
		item := adminUserResponse{User: user}
		if key, keyErr := h.store.GetAPIKeyForUser(c.Request.Context(), user.ID); keyErr == nil {
			item.HasAPIKey = key.RevokedAt == nil
			item.APIKeyPrefix = key.KeyPrefix
			item.APIKeyLastUsed = key.LastUsedAt
			item.APIKeyRevokedAt = key.RevokedAt
		} else if !errors.Is(keyErr, storage.ErrNotFound) {
			respondAdminError(c, keyErr)
			return
		}
		items = append(items, item)
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{
		"items": items, "total": page.Total, "page": page.Page, "page_size": page.PageSize,
	}))
}

func (h *AdminHandler) createUser(c *gin.Context) {
	if !h.available(c) {
		return
	}
	var request createUserRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "用户名不能为空"))
		return
	}
	if request.Role == "" {
		request.Role = storage.UserRoleUser
	}
	if request.RPSLimit <= 0 {
		request.RPSLimit = config.AppConfig.DefaultUserRPS
	}
	if request.RPMLimit <= 0 {
		request.RPMLimit = config.AppConfig.DefaultUserRPM
	}
	password, err := accountauth.GenerateTemporaryPassword()
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(http.StatusInternalServerError, "生成临时密码失败"))
		return
	}
	passwordHash, err := accountauth.HashPassword(password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(http.StatusInternalServerError, "生成密码摘要失败"))
		return
	}
	plainKey, keyPrefix, keyHash, err := accountauth.GenerateAPIKey()
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(http.StatusInternalServerError, "生成 API Key 失败"))
		return
	}
	mustChange := true
	user, key, err := h.store.CreateUserWithAPIKey(c.Request.Context(), storage.CreateUserInput{
		Username:           request.Username,
		PasswordHash:       passwordHash,
		Role:               request.Role,
		Enabled:            request.Enabled,
		ExpiresAt:          request.ExpiresAt,
		MustChangePassword: &mustChange,
		RPSLimit:           request.RPSLimit,
		RPMLimit:           request.RPMLimit,
		RateLimitDisabled:  request.RateLimitDisabled,
	}, storage.APIKeyInput{KeyPrefix: keyPrefix, KeyHash: keyHash, CreatedAt: time.Now()})
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusCreated, model.NewSuccessResponse(gin.H{
		"user":               user,
		"temporary_password": password,
		"api_key":            plainKey,
		"api_key_prefix":     key.KeyPrefix,
	}))
}

type updateUserRequest struct {
	Username          *string         `json:"username"`
	Role              *string         `json:"role"`
	Enabled           *bool           `json:"enabled"`
	ExpiresAt         json.RawMessage `json:"expires_at"`
	RPSLimit          *int            `json:"rps_limit"`
	RPMLimit          *int            `json:"rpm_limit"`
	RateLimitDisabled *bool           `json:"rate_limit_disabled"`
}

func (h *AdminHandler) updateUser(c *gin.Context) {
	if !h.available(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	var request updateUserRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "用户参数无效"))
		return
	}
	input := storage.UpdateUserInput{
		Username:          request.Username,
		Role:              request.Role,
		Enabled:           request.Enabled,
		RPSLimit:          request.RPSLimit,
		RPMLimit:          request.RPMLimit,
		RateLimitDisabled: request.RateLimitDisabled,
	}
	if len(request.ExpiresAt) > 0 {
		var expiresAt *time.Time
		if string(request.ExpiresAt) != "null" && string(request.ExpiresAt) != `""` {
			var parsed time.Time
			if err := json.Unmarshal(request.ExpiresAt, &parsed); err != nil {
				c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "expires_at 必须是 RFC3339 时间或 null"))
				return
			}
			expiresAt = &parsed
		}
		input.ExpiresAt = &expiresAt
	}
	user, err := h.store.UpdateUser(c.Request.Context(), id, input)
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(user))
}

func (h *AdminHandler) toggleUser(c *gin.Context) {
	if !h.available(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	current, err := h.store.GetUserByID(c.Request.Context(), id)
	if err != nil {
		respondAdminError(c, err)
		return
	}
	enabled := !current.Enabled
	var request struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&request); err == nil && request.Enabled != nil {
		enabled = *request.Enabled
	}
	user, err := h.store.UpdateUser(c.Request.Context(), id, storage.UpdateUserInput{Enabled: &enabled})
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(user))
}

func (h *AdminHandler) deleteUser(c *gin.Context) {
	if !h.available(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	user, err := h.store.SoftDeleteUser(c.Request.Context(), id, time.Now())
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(user))
}

func (h *AdminHandler) resetUserPassword(c *gin.Context) {
	if !h.available(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	password, err := accountauth.GenerateTemporaryPassword()
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(http.StatusInternalServerError, "生成临时密码失败"))
		return
	}
	hash, err := accountauth.HashPassword(password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(http.StatusInternalServerError, "生成密码摘要失败"))
		return
	}
	user, err := h.store.SetUserPassword(c.Request.Context(), id, hash, true, time.Now())
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{"user": user, "temporary_password": password}))
}

func (h *AdminHandler) resetUserAPIKey(c *gin.Context) {
	if !h.available(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	plain, prefix, hash, err := accountauth.GenerateAPIKey()
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(http.StatusInternalServerError, "生成 API Key 失败"))
		return
	}
	key, err := h.store.ResetAPIKey(c.Request.Context(), id, storage.APIKeyInput{KeyPrefix: prefix, KeyHash: hash}, time.Now())
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{"api_key": plain, "api_key_status": apiKeyStatus(key)}))
}

func (h *AdminHandler) revokeUserAPIKey(c *gin.Context) {
	if !h.available(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	if err := h.store.RevokeAPIKey(c.Request.Context(), id, time.Now()); err != nil && !errors.Is(err, storage.ErrNotFound) {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{"revoked": true}))
}
