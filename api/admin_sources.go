package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"pansou/model"
	"pansou/sourceconfig"
	"pansou/storage"
)

func (h *AdminHandler) registerSourceRoutes(group *gin.RouterGroup) {
	group.GET("/search-sources/catalog", h.sourceCatalog)
	group.GET("/search-sources/config", h.sourceConfig)
	group.POST("/search-sources/validate", h.validateSourceConfig)
	group.PUT("/search-sources/config", h.updateSourceConfig)
	group.GET("/search-sources/events", h.sourceEvents)
	group.GET("/user-plugin-credentials", h.userPluginCredentials)
}

func (h *AdminHandler) sourceCatalog(c *gin.Context) {
	if h.sources == nil || h.sources.Catalog == nil {
		c.JSON(http.StatusServiceUnavailable, model.NewErrorResponse(http.StatusServiceUnavailable, "搜索来源运行时未启用"))
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{"items": h.sources.Catalog.Plugins()}))
}

func (h *AdminHandler) sourceConfig(c *gin.Context) {
	if h.sources == nil {
		c.JSON(http.StatusServiceUnavailable, model.NewErrorResponse(http.StatusServiceUnavailable, "搜索来源运行时未启用"))
		return
	}
	state, err := h.sources.Current(c.Request.Context())
	if err != nil {
		respondSourceError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(state))
}

type sourceConfigRequest struct {
	ExpectedVersion int64               `json:"expected_version"`
	Config          sourceconfig.Config `json:"config" binding:"required"`
}

func (h *AdminHandler) validateSourceConfig(c *gin.Context) {
	if h.sources == nil {
		c.JSON(http.StatusServiceUnavailable, model.NewErrorResponse(http.StatusServiceUnavailable, "搜索来源运行时未启用"))
		return
	}
	var request sourceConfigRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "来源配置参数无效"))
		return
	}
	config, err := h.sources.Validate(request.Config)
	if err != nil {
		respondSourceError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{"valid": true, "config": config}))
}

func (h *AdminHandler) updateSourceConfig(c *gin.Context) {
	if h.sources == nil {
		c.JSON(http.StatusServiceUnavailable, model.NewErrorResponse(http.StatusServiceUnavailable, "搜索来源运行时未启用"))
		return
	}
	var request sourceConfigRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.ExpectedVersion <= 0 {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "来源配置参数无效"))
		return
	}
	principal, ok := currentPrincipal(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, model.NewErrorResponse(http.StatusUnauthorized, "未登录"))
		return
	}
	if h.credentials == nil && enablesAccountPlugin(request.Config) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "插件凭据主密钥未配置", "code": "credential_key_unavailable"})
		return
	}
	actor := principal.UserID
	state, err := h.sources.Update(c.Request.Context(), request.ExpectedVersion, request.Config, &actor)
	if err != nil {
		respondSourceError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(state))
}

func enablesAccountPlugin(config sourceconfig.Config) bool {
	if !config.AsyncPluginsEnabled {
		return false
	}
	for _, key := range []string{"aisoupan", "qqpd", "gying", "panlian", "weibo"} {
		if config.Plugins[key].Enabled {
			return true
		}
	}
	return false
}

func (h *AdminHandler) sourceEvents(c *gin.Context) {
	if !h.available(c) {
		return
	}
	page, err := h.store.ListSearchSourceConfigEvents(c.Request.Context(), storage.SearchSourceConfigEventFilter{Results: queryList(c, "result"), Page: queryInt(c, "page", 1), PageSize: queryInt(c, "page_size", 20)})
	if err != nil {
		respondSourceError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(page))
}

func (h *AdminHandler) adminPluginCredentials(c *gin.Context) {
	if !h.available(c) {
		return
	}
	filter := storage.PluginCredentialFilter{PluginKeys: queryList(c, "plugin_key"), Scopes: []string{storage.CredentialScopeAdminPrivate, storage.CredentialScopePublicShared}, Statuses: queryList(c, "status"), Page: queryInt(c, "page", 1), PageSize: queryInt(c, "page_size", 50)}
	page, err := h.store.ListAdminPluginCredentials(c.Request.Context(), filter)
	if err != nil {
		respondSourceError(c, err)
		return
	}
	page.Items = redactCredentialItems(page.Items)
	c.JSON(http.StatusOK, model.NewSuccessResponse(page))
}

func (h *AdminHandler) userPluginCredentials(c *gin.Context) {
	if !h.available(c) {
		return
	}
	filter := storage.PluginCredentialFilter{PluginKeys: queryList(c, "plugin_key"), Scopes: []string{storage.CredentialScopeUserPrivate}, Statuses: queryList(c, "status"), Page: queryInt(c, "page", 1), PageSize: queryInt(c, "page_size", 50)}
	page, err := h.store.ListAdminPluginCredentials(c.Request.Context(), filter)
	if err != nil {
		respondSourceError(c, err)
		return
	}
	page.Items = redactCredentialItems(page.Items)
	c.JSON(http.StatusOK, model.NewSuccessResponse(page))
}

func redactCredentialItems(items []storage.PluginCredential) []storage.PluginCredential {
	result := append([]storage.PluginCredential(nil), items...)
	for index := range result {
		result[index].Ciphertext = nil
		result[index].Nonce = nil
		result[index].CredentialFingerprint = nil
		result[index].BindingFingerprint = nil
		result[index].PublicMetadata = redactMetadata(result[index].PublicMetadata)
	}
	return result
}

func redactMetadata(metadata map[string]any) map[string]any {
	result := make(map[string]any, len(metadata))
	for key, value := range metadata {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "password") || strings.Contains(lower, "cookie") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") {
			continue
		}
		result[key] = value
	}
	return result
}

func respondSourceError(c *gin.Context, err error) {
	status := http.StatusServiceUnavailable
	switch {
	case errors.Is(err, storage.ErrConflict):
		status = http.StatusConflict
	case errors.Is(err, storage.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, storage.ErrInvalid), errors.Is(err, sourceconfig.ErrInvalidConfig):
		status = http.StatusBadRequest
	case errors.Is(err, sourceconfig.ErrClosed):
		status = http.StatusServiceUnavailable
	default:
		if strings.Contains(err.Error(), "initialization failed") || strings.Contains(err.Error(), "build source snapshot") {
			status = http.StatusUnprocessableEntity
		}
	}
	c.JSON(status, model.NewErrorResponse(status, err.Error()))
}
