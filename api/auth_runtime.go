package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	accountauth "pansou/auth"
)

const (
	principalContextKey = "auth_principal"
	authTypeContextKey  = "auth_type"
)

var databaseAuthService *accountauth.Service

func SetAuthService(service *accountauth.Service) {
	databaseAuthService = service
}

func currentPrincipal(c *gin.Context) (accountauth.Principal, bool) {
	value, ok := c.Get(principalContextKey)
	if !ok {
		return accountauth.Principal{}, false
	}
	principal, ok := value.(accountauth.Principal)
	return principal, ok
}

func setPrincipal(c *gin.Context, principal accountauth.Principal, authType string) {
	c.Set(principalContextKey, principal)
	c.Set(authTypeContextKey, authType)
	c.Set("username", principal.Username)
	c.Set("user_id", principal.UserID)
	c.Set("role", principal.Role)
}

func respondAuthError(c *gin.Context, err error) {
	status := http.StatusUnauthorized
	code := "AUTH_INVALID"
	message := "认证凭证无效或已过期"
	switch {
	case errors.Is(err, accountauth.ErrRepositoryUnavailable):
		status, code, message = http.StatusServiceUnavailable, "AUTH_SERVICE_UNAVAILABLE", "账号服务暂不可用"
	case errors.Is(err, accountauth.ErrUserDisabled):
		code, message = "AUTH_USER_DISABLED", "账号已停用"
	case errors.Is(err, accountauth.ErrUserExpired):
		code, message = "AUTH_USER_EXPIRED", "账号已到期"
	case errors.Is(err, accountauth.ErrUserDeleted):
		code, message = "AUTH_USER_DELETED", "账号不存在"
	case errors.Is(err, accountauth.ErrPasswordChangeRequired):
		status, code, message = http.StatusForbidden, "AUTH_PASSWORD_CHANGE_REQUIRED", "请先修改初始密码"
	case errors.Is(err, accountauth.ErrPasswordPolicyViolation):
		status, code, message = http.StatusBadRequest, "AUTH_PASSWORD_POLICY", "新密码至少需要 8 个字符"
	case errors.Is(err, accountauth.ErrTokenStale):
		code, message = "AUTH_TOKEN_STALE", "登录状态已失效，请重新登录"
	case errors.Is(err, accountauth.ErrInvalidAPIKey):
		code, message = "AUTH_API_KEY_INVALID", "API Key 无效"
	case errors.Is(err, accountauth.ErrInvalidCredentials):
		code, message = "AUTH_INVALID_CREDENTIALS", "用户名或密码错误"
	}
	c.JSON(status, gin.H{"error": message, "code": code})
}
