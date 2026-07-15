package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	accountauth "pansou/auth"
	"pansou/config"
	"pansou/util"
)

func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := strings.TrimSpace(c.GetHeader("X-Request-ID"))
		if requestID == "" {
			buffer := make([]byte, 12)
			if _, err := rand.Read(buffer); err == nil {
				requestID = hex.EncodeToString(buffer)
			} else {
				requestID = fmt.Sprintf("%d", time.Now().UnixNano())
			}
		}
		c.Request.Header.Set("X-Request-ID", requestID)
		c.Header("X-Request-ID", requestID)
		c.Next()
	}
}

// CORSMiddleware 跨域中间件
func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Origin, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, X-API-Key, X-Request-ID")
		c.Writer.Header().Set("Access-Control-Expose-Headers", "RateLimit-Limit, RateLimit-Remaining, RateLimit-Reset, Retry-After, X-Request-ID, X-PanSou-Cache-Status")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

// LoggerMiddleware 日志中间件
func LoggerMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 开始时间
		startTime := time.Now()

		// 处理请求
		c.Next()

		// 结束时间
		endTime := time.Now()

		// 执行时间
		latencyTime := endTime.Sub(startTime)

		// 请求方式
		reqMethod := c.Request.Method

		// 请求路由
		reqURI := c.Request.RequestURI

		// 对于搜索API，尝试解码关键词以便更好地显示
		displayURI := reqURI
		if strings.Contains(reqURI, "/api/search") && strings.Contains(reqURI, "kw=") {
			if parsedURL, err := url.Parse(reqURI); err == nil {
				if keyword := parsedURL.Query().Get("kw"); keyword != "" {
					if decodedKeyword, err := url.QueryUnescape(keyword); err == nil {
						// 替换原始URI中的编码关键词为解码后的关键词
						displayURI = strings.Replace(reqURI, "kw="+keyword, "kw="+decodedKeyword, 1)
					}
				}
			}
		}

		// 状态码
		statusCode := c.Writer.Status()

		// 请求IP
		clientIP := c.ClientIP()

		// 日志格式
		gin.DefaultWriter.Write([]byte(
			fmt.Sprintf("| %s | %s | %s | %d | %s\n",
				clientIP, reqMethod, displayURI, statusCode, latencyTime.String())))
	}
}

// AuthMiddleware JWT认证中间件
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if databaseAuthService != nil {
			databaseAuthMiddleware(c)
			return
		}

		// 如果未启用认证，直接放行
		if !config.AppConfig.AuthEnabled {
			c.Next()
			return
		}

		// 定义公开接口（不需要认证）
		publicPaths := []string{
			"/api/auth/login",
			"/api/auth/logout",
			"/api/health", // 健康检查接口可选择是否需要认证
			"/admin",      // 管理端登录页面和静态资源本身公开，数据接口仍强制认证
		}

		// 检查当前路径是否是公开接口
		path := c.Request.URL.Path
		for _, p := range publicPaths {
			if strings.HasPrefix(path, p) {
				c.Next()
				return
			}
		}

		// 获取Authorization头
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(401, gin.H{
				"error": "未授权：缺少认证令牌",
				"code":  "AUTH_TOKEN_MISSING",
			})
			c.Abort()
			return
		}

		// 解析Bearer token
		const bearerPrefix = "Bearer "
		if !strings.HasPrefix(authHeader, bearerPrefix) {
			c.JSON(401, gin.H{
				"error": "未授权：令牌格式错误",
				"code":  "AUTH_TOKEN_INVALID_FORMAT",
			})
			c.Abort()
			return
		}

		tokenString := strings.TrimPrefix(authHeader, bearerPrefix)

		// 验证token
		claims, err := util.ValidateToken(tokenString, config.AppConfig.AuthJWTSecret)
		if err != nil {
			c.JSON(401, gin.H{
				"error": "未授权：令牌无效或已过期",
				"code":  "AUTH_TOKEN_INVALID",
			})
			c.Abort()
			return
		}

		// 将用户信息存入上下文，供后续处理使用
		c.Set("username", claims.Username)
		c.Next()
	}
}

func databaseAuthMiddleware(c *gin.Context) {
	path := c.Request.URL.Path
	if isPublicPath(path) {
		c.Next()
		return
	}

	if path == "/api/search" {
		if apiKey := c.GetHeader("X-API-Key"); apiKey != "" {
			principal, err := databaseAuthService.AuthenticateAPIKey(c.Request.Context(), apiKey)
			if err != nil {
				respondAuthError(c, err)
				c.Abort()
				return
			}
			setPrincipal(c, principal, "api_key")
			c.Next()
			return
		}
	}

	authHeader := c.GetHeader("Authorization")
	const bearerPrefix = "Bearer "
	if !strings.HasPrefix(authHeader, bearerPrefix) {
		c.JSON(401, gin.H{"error": "未授权：缺少认证令牌", "code": "AUTH_TOKEN_MISSING"})
		c.Abort()
		return
	}
	principal, err := databaseAuthService.AuthenticateToken(c.Request.Context(), strings.TrimSpace(strings.TrimPrefix(authHeader, bearerPrefix)))
	if err != nil {
		respondAuthError(c, err)
		c.Abort()
		return
	}
	setPrincipal(c, principal, "web")
	if principal.MustChangePassword && path != "/api/auth/verify" && path != "/api/auth/change-password" && path != "/api/auth/logout" {
		respondAuthError(c, accountauth.ErrPasswordChangeRequired)
		c.Abort()
		return
	}
	c.Next()
}

func isPublicPath(path string) bool {
	if path == "/api/auth/login" || path == "/api/auth/logout" || path == "/api/health" || path == "/admin" || path == "/admin/" {
		return true
	}
	return strings.HasPrefix(path, "/admin/assets/")
}

// RequireAdminAuth ensures that management APIs are protected even when the
// legacy public search API is running with AUTH_ENABLED=false. This reuses the
// same JWT configuration and claims as AuthMiddleware instead of introducing a
// second permission system.
func RequireAdminAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if databaseAuthService != nil {
			principal, ok := currentPrincipal(c)
			if !ok {
				c.JSON(401, gin.H{"error": "未授权：缺少认证令牌", "code": "AUTH_TOKEN_MISSING"})
				c.Abort()
				return
			}
			if !principal.IsAdmin() {
				c.JSON(403, gin.H{"error": "需要管理员权限", "code": "AUTH_ADMIN_REQUIRED"})
				c.Abort()
				return
			}
			c.Next()
			return
		}

		if config.AppConfig == nil || !config.AppConfig.AuthEnabled || len(config.AppConfig.AuthUsers) == 0 {
			c.JSON(503, gin.H{
				"error": "管理员认证未配置，请设置 AUTH_ENABLED、AUTH_USERS 和 AUTH_JWT_SECRET",
				"code":  "ADMIN_AUTH_NOT_CONFIGURED",
			})
			c.Abort()
			return
		}

		// The global middleware has already validated authenticated requests.
		if _, ok := c.Get("username"); ok {
			c.Next()
			return
		}

		authHeader := c.GetHeader("Authorization")
		const bearerPrefix = "Bearer "
		if !strings.HasPrefix(authHeader, bearerPrefix) {
			c.JSON(401, gin.H{"error": "未授权：缺少认证令牌", "code": "AUTH_TOKEN_MISSING"})
			c.Abort()
			return
		}

		claims, err := util.ValidateToken(strings.TrimPrefix(authHeader, bearerPrefix), config.AppConfig.AuthJWTSecret)
		if err != nil {
			c.JSON(401, gin.H{"error": "未授权：令牌无效或已过期", "code": "AUTH_TOKEN_INVALID"})
			c.Abort()
			return
		}
		c.Set("username", claims.Username)
		c.Next()
	}
}
