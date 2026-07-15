package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	accountauth "pansou/auth"
	"pansou/model"
	"pansou/storage"
)

type UserHandler struct {
	store *storage.Store
}

func NewUserHandler(store *storage.Store) *UserHandler { return &UserHandler{store: store} }

func (h *UserHandler) Register(group *gin.RouterGroup) {
	group.GET("/me", CurrentUserHandler)
	group.GET("/api-key", h.getAPIKey)
	group.POST("/api-key/reset", h.resetAPIKey)
	group.GET("/usage/overview", h.usageOverview)
	group.GET("/usage/trends", h.usageTrends)
	group.GET("/usage/logs", h.usageLogs)
}

func (h *UserHandler) available(c *gin.Context) bool {
	if h == nil || h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "数据库账号系统未启用", "code": "AUTH_DATABASE_DISABLED"})
		return false
	}
	return true
}

func (h *UserHandler) getAPIKey(c *gin.Context) {
	if !h.available(c) {
		return
	}
	principal, ok := currentPrincipal(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未授权", "code": "AUTH_TOKEN_MISSING"})
		return
	}
	key, err := h.store.GetAPIKeyForUser(c.Request.Context(), principal.UserID)
	if errors.Is(err, storage.ErrNotFound) {
		c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{"configured": false}))
		return
	}
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(apiKeyStatus(key)))
}

func (h *UserHandler) resetAPIKey(c *gin.Context) {
	if !h.available(c) {
		return
	}
	principal, ok := currentPrincipal(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未授权", "code": "AUTH_TOKEN_MISSING"})
		return
	}
	plain, prefix, hash, err := accountauth.GenerateAPIKey()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成 API Key 失败", "code": "API_KEY_GENERATION_FAILED"})
		return
	}
	key, err := h.store.ResetAPIKey(c.Request.Context(), principal.UserID, storage.APIKeyInput{KeyPrefix: prefix, KeyHash: hash}, time.Now())
	if err != nil {
		respondAdminError(c, err)
		return
	}
	response := apiKeyStatus(key)
	response["api_key"] = plain
	c.JSON(http.StatusOK, model.NewSuccessResponse(response))
}

func (h *UserHandler) usageOverview(c *gin.Context) {
	if !h.available(c) {
		return
	}
	principal, ok := currentPrincipal(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未授权", "code": "AUTH_TOKEN_MISSING"})
		return
	}
	days := boundedDays(c.Query("days"), 1)
	now := time.Now()
	stats, err := h.store.UserUsageOverview(c.Request.Context(), principal.UserID, storage.UsageStatsFilter{From: now.Add(-time.Duration(days) * 24 * time.Hour), To: now})
	if err != nil {
		respondAdminError(c, err)
		return
	}
	today, err := h.store.UserUsageOverview(c.Request.Context(), principal.UserID, storage.UsageStatsFilter{From: now.Add(-24 * time.Hour), To: now})
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{
		"total":               stats.TotalRequests,
		"success_count":       stats.SuccessfulRequests,
		"error_count":         stats.FailedRequests,
		"rate_limited_count":  stats.RateLimitedRequests,
		"average_duration_ms": stats.AvgDurationMS,
		"p95_duration_ms":     stats.P95DurationMS,
		"cache_hit_rate":      stats.CacheHitRate,
		"today_count":         today.TotalRequests,
		"status_counts":       stats.StatusCounts,
		"error_counts":        stats.ErrorCounts,
	}))
}

func (h *UserHandler) usageTrends(c *gin.Context) {
	if !h.available(c) {
		return
	}
	principal, ok := currentPrincipal(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未授权", "code": "AUTH_TOKEN_MISSING"})
		return
	}
	days := boundedDays(c.Query("days"), 7)
	now := time.Now()
	points, err := h.store.UserUsageTrends(c.Request.Context(), principal.UserID, storage.UsageStatsFilter{From: now.Add(-time.Duration(days) * 24 * time.Hour), To: now})
	if err != nil {
		respondAdminError(c, err)
		return
	}
	result := make([]gin.H, 0, len(points))
	for _, point := range points {
		result = append(result, gin.H{
			"time":         point.Bucket,
			"total":        point.RequestCount,
			"success":      point.SuccessfulRequests,
			"errors":       point.FailedRequests,
			"rate_limited": point.RateLimitedRequests,
		})
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(result))
}

func (h *UserHandler) usageLogs(c *gin.Context) {
	if !h.available(c) {
		return
	}
	principal, ok := currentPrincipal(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未授权", "code": "AUTH_TOKEN_MISSING"})
		return
	}
	filter := storage.APIRequestLogFilter{
		AuthTypes:      queryList(c, "auth_type", "auth_types"),
		StatusFamilies: queryList(c, "status", "status_family", "status_families"),
		CacheStatuses:  queryList(c, "cache_status", "cache_statuses"),
		Query:          strings.TrimSpace(c.Query("q")),
		Page:           queryInt(c, "page", 1),
		PageSize:       queryInt(c, "page_size", 20),
	}
	page, err := h.store.ListUserAPIRequestLogs(c.Request.Context(), principal.UserID, filter)
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(page))
}

func apiKeyStatus(key storage.APIKey) gin.H {
	return gin.H{
		"configured":   key.RevokedAt == nil,
		"prefix":       key.KeyPrefix,
		"created_at":   key.CreatedAt,
		"last_used_at": key.LastUsedAt,
	}
}

func boundedDays(value string, fallback int) int {
	days, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || days < 1 {
		days = fallback
	}
	if days > 30 {
		days = 30
	}
	return days
}
