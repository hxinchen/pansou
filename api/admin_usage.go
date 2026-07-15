package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"pansou/model"
	"pansou/storage"
)

func (h *AdminHandler) adminUsageOverview(c *gin.Context) {
	if !h.available(c) {
		return
	}
	filter := adminUsageFilter(c, 7)
	stats, err := h.store.AdminUsageOverview(c.Request.Context(), filter)
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{
		"from":                  stats.From,
		"to":                    stats.To,
		"total_requests":        stats.TotalRequests,
		"successful_requests":   stats.SuccessfulRequests,
		"failed_requests":       stats.FailedRequests,
		"success_rate":          stats.SuccessRate,
		"rate_limited_requests": stats.RateLimitedRequests,
		"rate_limited_count":    stats.RateLimitedRequests,
		"active_users":          stats.ActiveUsers,
		"avg_duration_ms":       stats.AvgDurationMS,
		"p95_duration_ms":       stats.P95DurationMS,
		"cache_hit_rate":        stats.CacheHitRate,
		"status_counts":         stats.StatusCounts,
		"error_counts":          stats.ErrorCounts,
		"top_users":             stats.TopUsers,
		"recent_requests":       stats.RecentRequests,
	}))
}

func (h *AdminHandler) adminUsageTrends(c *gin.Context) {
	if !h.available(c) {
		return
	}
	points, err := h.store.AdminUsageTrends(c.Request.Context(), adminUsageFilter(c, 7))
	if err != nil {
		respondAdminError(c, err)
		return
	}
	result := make([]gin.H, 0, len(points))
	for _, point := range points {
		result = append(result, gin.H{
			"bucket":             point.Bucket,
			"request_count":      point.RequestCount,
			"success_count":      point.SuccessfulRequests,
			"failed_count":       point.FailedRequests,
			"rate_limited_count": point.RateLimitedRequests,
			"active_users":       point.ActiveUsers,
			"avg_duration_ms":    point.AvgDurationMS,
			"p95_duration_ms":    point.P95DurationMS,
		})
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(result))
}

func (h *AdminHandler) adminUsageLogs(c *gin.Context) {
	if !h.available(c) {
		return
	}
	filter := storage.APIRequestLogFilter{
		AuthTypes:      queryList(c, "auth_type", "auth_types"),
		StatusFamilies: queryList(c, "status", "status_family", "status_families"),
		CacheStatuses:  queryList(c, "cache_status", "cache_statuses"),
		Query:          strings.TrimSpace(c.Query("q")),
		Page:           queryInt(c, "page", 1),
		PageSize:       queryInt(c, "page_size", 30),
		SortBy:         strings.TrimSpace(c.Query("sort_by")),
		SortDir:        strings.TrimSpace(c.Query("sort_dir")),
	}
	if value := strings.TrimSpace(c.Query("user_id")); value != "" {
		userID, err := strconv.ParseInt(value, 10, 64)
		if err != nil || userID <= 0 {
			c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "user_id 必须是正整数"))
			return
		}
		filter.UserID = &userID
	}
	if value := strings.TrimSpace(c.Query("status_code")); value != "" {
		status, err := strconv.Atoi(value)
		if err != nil {
			c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "status_code 无效"))
			return
		}
		filter.StatusCodes = []int{status}
	}
	page, err := h.store.ListAdminAPIRequestLogs(c.Request.Context(), filter)
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(page))
}

func adminUsageFilter(c *gin.Context, fallbackDays int) storage.UsageStatsFilter {
	days := boundedDays(c.Query("days"), fallbackDays)
	now := time.Now()
	filter := storage.UsageStatsFilter{
		From:            now.Add(-time.Duration(days) * 24 * time.Hour),
		To:              now,
		SlowThresholdMS: int64(queryInt(c, "slow_threshold_ms", 1000)),
		RecentLimit:     queryInt(c, "recent_limit", 10),
		TopUserLimit:    queryInt(c, "top_user_limit", 10),
	}
	if value := strings.TrimSpace(c.Query("user_id")); value != "" {
		if userID, err := strconv.ParseInt(value, 10, 64); err == nil && userID > 0 {
			filter.UserID = &userID
		}
	}
	return filter
}
