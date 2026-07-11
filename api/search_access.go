package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"pansou/usage"
)

const (
	usageKeywordContextKey     = "usage_keyword"
	usageResultCountContextKey = "usage_result_count"
	usageCacheStatusContextKey = "usage_cache_status"
	usageErrorCodeContextKey   = "usage_error_code"
)

var (
	searchLimiter *usage.Limiter
	usageRecorder *usage.Recorder
)

func SetUsageServices(limiter *usage.Limiter, recorder *usage.Recorder) {
	searchLimiter = limiter
	usageRecorder = recorder
}

func SearchAccessMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, authenticated := currentPrincipal(c)
		if !authenticated {
			c.Next()
			return
		}

		started := time.Now()
		if principal.MustChangePassword {
			c.Set(usageErrorCodeContextKey, "AUTH_PASSWORD_CHANGE_REQUIRED")
			c.JSON(http.StatusForbidden, gin.H{"error": "请先修改初始密码", "code": "AUTH_PASSWORD_CHANGE_REQUIRED"})
			recordSearchUsage(c, principal.UserID, started)
			c.Abort()
			return
		}

		if searchLimiter != nil {
			decision := searchLimiter.AllowWithConfig(strconv.FormatInt(principal.UserID, 10), usage.LimitConfig{
				RequestsPerSecond: principal.RPSLimit,
				RequestsPerMinute: principal.RPMLimit,
				Unlimited:         principal.RateLimitDisabled,
			})
			setRateLimitHeaders(c, decision)
			if !decision.Allowed {
				c.Set(usageErrorCodeContextKey, "RATE_LIMIT_EXCEEDED")
				c.JSON(http.StatusTooManyRequests, gin.H{
					"error":       "请求过于频繁，请稍后重试",
					"code":        "RATE_LIMIT_EXCEEDED",
					"retry_after": int(decision.RetryAfter.Round(time.Second) / time.Second),
				})
				recordSearchUsage(c, principal.UserID, started)
				c.Abort()
				return
			}
		}

		c.Next()
		recordSearchUsage(c, principal.UserID, started)
	}
}

func setRateLimitHeaders(c *gin.Context, decision usage.LimitDecision) {
	if decision.Unlimited {
		c.Header("RateLimit-Limit", "unlimited")
		c.Header("RateLimit-Remaining", "unlimited")
		return
	}
	c.Header("RateLimit-Limit", fmt.Sprintf("%d;w=1, %d;w=60", decision.Second.Limit, decision.Minute.Limit))
	c.Header("RateLimit-Remaining", strconv.Itoa(decision.Remaining))
	if !decision.ResetAt.IsZero() {
		c.Header("RateLimit-Reset", strconv.FormatInt(decision.ResetAt.Unix(), 10))
	}
	if decision.RetryAfter > 0 {
		seconds := int(decision.RetryAfter.Round(time.Second) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		c.Header("Retry-After", strconv.Itoa(seconds))
	}
}

func recordSearchUsage(c *gin.Context, userID int64, started time.Time) {
	if usageRecorder == nil {
		return
	}
	authType, _ := c.Get(authTypeContextKey)
	metadata := map[string]interface{}{
		"auth_type":  authType,
		"keyword":    c.GetString(usageKeywordContextKey),
		"source_ip":  c.ClientIP(),
		"user_agent": c.Request.UserAgent(),
	}
	if value, ok := c.Get(usageResultCountContextKey); ok {
		metadata["result_count"] = value
	}
	if value, ok := c.Get(usageCacheStatusContextKey); ok {
		metadata["cache_status"] = value
	}
	if value, ok := c.Get(usageErrorCodeContextKey); ok {
		metadata["error_code"] = value
	}
	usageRecorder.Record(usage.UsageEvent{
		UserID:     strconv.FormatInt(userID, 10),
		RequestID:  c.GetHeader("X-Request-ID"),
		Method:     c.Request.Method,
		Route:      "/api/search",
		StatusCode: c.Writer.Status(),
		OccurredAt: started.UTC(),
		Duration:   time.Since(started),
		Metadata:   metadata,
	})
}
