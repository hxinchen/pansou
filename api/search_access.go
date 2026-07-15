package api

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"pansou/config"
	"pansou/service"
	"pansou/usage"
)

const (
	usageKeywordContextKey     = "usage_keyword"
	usageResultCountContextKey = "usage_result_count"
	usageCacheStatusContextKey = "usage_cache_status"
	usageErrorCodeContextKey   = "usage_error_code"
	usageSourceIPContextKey    = "usage_source_ip"
)

const searchCacheStatusHeader = "X-PanSou-Cache-Status"

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
		prepareSearchMonitoring(c)
		principal, authenticated := currentPrincipal(c)
		if !authenticated {
			c.Next()
			finalizeSearchCacheStatus(c)
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
		finalizeSearchCacheStatus(c)
		recordSearchUsage(c, principal.UserID, started)
	}
}

func prepareSearchMonitoring(c *gin.Context) {
	trace := service.NewSearchTrace()
	c.Request = c.Request.WithContext(service.ContextWithSearchTrace(c.Request.Context(), trace))
	c.Set(usageSourceIPContextKey, normalizeUsageSourceIP(c.ClientIP()))
	c.Set(usageCacheStatusContextKey, string(service.SearchCacheNotApplicable))
	c.Header(searchCacheStatusHeader, string(service.SearchCacheNotApplicable))
}

func finalizeSearchCacheStatus(c *gin.Context) string {
	status := service.SearchCacheNotApplicable
	if trace := service.SearchTraceFromContext(c.Request.Context()); trace != nil {
		status = trace.Status()
	}
	value := string(status)
	c.Set(usageCacheStatusContextKey, value)
	c.Header(searchCacheStatusHeader, value)
	return value
}

func normalizeUsageSourceIP(value string) string {
	trustedProxies := []string(nil)
	if config.AppConfig != nil {
		trustedProxies = config.AppConfig.TrustedProxies
	}
	return normalizeUsageSourceIPWithTrustedProxies(value, trustedProxies)
}

func normalizeUsageSourceIPWithTrustedProxies(value string, trustedProxies []string) string {
	value = strings.TrimSpace(value)
	ip := net.ParseIP(value)
	if ip == nil {
		return value
	}
	if ip.IsLoopback() || matchesTrustedProxy(ip, trustedProxies) {
		return "internal"
	}
	return value
}

func matchesTrustedProxy(ip net.IP, trustedProxies []string) bool {
	for _, value := range trustedProxies {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if candidate := net.ParseIP(value); candidate != nil {
			if candidate.Equal(ip) {
				return true
			}
			continue
		}
		_, network, err := net.ParseCIDR(value)
		if err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
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
	sourceIP := c.GetString(usageSourceIPContextKey)
	if sourceIP == "" {
		sourceIP = normalizeUsageSourceIP(c.ClientIP())
	}
	metadata := map[string]interface{}{
		"auth_type":  authType,
		"keyword":    c.GetString(usageKeywordContextKey),
		"source_ip":  sourceIP,
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
