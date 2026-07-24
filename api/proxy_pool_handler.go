package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"pansou/model"
	"pansou/proxypool"
	"pansou/storage"
)

type ProxyPoolHandler struct {
	pool *proxypool.Service
}

func NewProxyPoolHandler(pool *proxypool.Service) *ProxyPoolHandler {
	return &ProxyPoolHandler{pool: pool}
}

func (h *ProxyPoolHandler) Register(group *gin.RouterGroup) {
	group.GET("/proxy-pool/summary", h.summary)
	group.GET("/proxy-pool/nodes", h.nodes)
	group.POST("/proxy-pool/import", h.importNodes)
	group.POST("/proxy-pool/probe", h.probe)
	group.PATCH("/proxy-pool/nodes/:id", h.patchNode)
	group.DELETE("/proxy-pool/nodes/:id", h.deleteNode)
	group.GET("/proxy-pool/batches", h.batches)
	group.PATCH("/proxy-pool/batches/:id", h.patchBatch)
	group.GET("/proxy-pool/policies", h.policies)
	group.PUT("/proxy-pool/policies", h.replacePolicies)
}

func (h *ProxyPoolHandler) ready(c *gin.Context) bool {
	if h == nil || h.pool == nil {
		c.JSON(http.StatusServiceUnavailable, model.NewErrorResponse(http.StatusServiceUnavailable, "代理池未配置密钥或服务未启用"))
		return false
	}
	return true
}

func (h *ProxyPoolHandler) summary(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	value, err := h.pool.Summary(c.Request.Context())
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(value))
}

func (h *ProxyPoolHandler) nodes(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	var batchID *int64
	if raw := strings.TrimSpace(c.Query("batch_id")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "batch_id 无效"))
			return
		}
		batchID = &parsed
	}
	page, err := h.pool.Nodes(c.Request.Context(), storage.ProxyNodeFilter{Status: c.Query("status"), Query: c.Query("q"), BatchID: batchID, Page: queryInt(c, "page", 1), PageSize: queryInt(c, "page_size", 50)})
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(page))
}

func (h *ProxyPoolHandler) importNodes(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "请选择代理 TXT 文件"))
		return
	}
	defer file.Close()
	expiresAt := time.Time{}
	if raw := strings.TrimSpace(c.PostForm("expires_at")); raw != "" {
		expiresAt, err = parseProxyTime(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "expires_at 必须是 RFC3339 或日期"))
			return
		}
	}
	result, errorsList, err := h.pool.Import(c.Request.Context(), proxypool.ImportRequest{Name: strings.TrimSpace(c.PostForm("batch_name")), SourceFilename: header.Filename, ExpiresAt: expiresAt}, file)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, err.Error()))
		return
	}
	result.Errors = errorsList
	c.JSON(http.StatusCreated, model.NewSuccessResponse(result))
}

func parseProxyTime(raw string) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed, nil
	}
	parsed, err := time.ParseInLocation("2006-01-02", raw, time.Local)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.Add(24 * time.Hour).UTC(), nil
}

func (h *ProxyPoolHandler) probe(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	var request struct {
		IDs   []int64 `json:"ids"`
		Limit int     `json:"limit"`
	}
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "探测参数无效"))
			return
		}
	}
	count, err := h.pool.Probe(c.Request.Context(), request.IDs, request.Limit)
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{"probed": count}))
}

func (h *ProxyPoolHandler) patchNode(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	var request struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || request.Enabled == nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "enabled 必须是布尔值"))
		return
	}
	if err := h.pool.SetNodeEnabled(c.Request.Context(), id, *request.Enabled); err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{"updated": true}))
}

func (h *ProxyPoolHandler) deleteNode(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	if err := h.pool.DeleteNode(c.Request.Context(), id); err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{"deleted": true}))
}

func (h *ProxyPoolHandler) batches(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	items, total, err := h.pool.Batches(c.Request.Context(), queryInt(c, "page", 1), queryInt(c, "page_size", 20))
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{"items": items, "total": total, "page": queryInt(c, "page", 1), "page_size": queryInt(c, "page_size", 20)}))
}

func (h *ProxyPoolHandler) patchBatch(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	var request struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || request.Enabled == nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "enabled 必须是布尔值"))
		return
	}
	if err := h.pool.SetBatchEnabled(c.Request.Context(), id, *request.Enabled); err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{"updated": true}))
}

func (h *ProxyPoolHandler) policies(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	items, err := h.pool.Policies(c.Request.Context())
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{"items": items, "modes": []string{proxypool.ModeBaselineOnly, proxypool.ModeBaselineFirst, proxypool.ModeProxyFirst, proxypool.ModeProxyOnly, proxypool.ModeStickyProxy}}))
}

func (h *ProxyPoolHandler) replacePolicies(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	var request struct {
		Policies []storage.ProxyPolicy `json:"policies"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "代理路由策略无效"))
		return
	}
	if err := h.pool.ReplacePolicies(c.Request.Context(), request.Policies); err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{"updated": true}))
}
