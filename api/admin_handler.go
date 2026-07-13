package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"pansou/collection"
	"pansou/credential"
	"pansou/keywordsync"
	"pansou/model"
	"pansou/sourceconfig"
	"pansou/storage"
)

type AdminHandler struct {
	store          *storage.Store
	runner         *collection.Runner
	sources        *sourceconfig.Service
	credentials    *credential.Service
	keywordSources *keywordsync.Service
	overviewCache  *adminOverviewCache
}

type adminOverviewResponse struct {
	storage.OverviewStats
	Trends      []storage.TrendPoint `json:"trends"`
	GeneratedAt time.Time            `json:"generated_at"`
	Stale       bool                 `json:"stale"`
	Refreshing  bool                 `json:"refreshing"`
}

func NewAdminHandler(store *storage.Store, runner *collection.Runner, sources ...*sourceconfig.Service) *AdminHandler {
	var runtime *sourceconfig.Service
	if len(sources) > 0 {
		runtime = sources[0]
	}
	handler := &AdminHandler{store: store, runner: runner, sources: runtime}
	if store != nil {
		handler.overviewCache = newAdminOverviewCache(store)
	}
	return handler
}

func (h *AdminHandler) Register(group *gin.RouterGroup) {
	group.GET("/overview", h.overview)
	group.GET("/trends", h.trends)
	group.GET("/resources", h.listResources)
	group.GET("/resources/:id", h.getResource)
	group.GET("/resources/:id/sources", h.listResourceSources)
	group.GET("/resources/:id/keywords", h.listResourceKeywords)
	group.GET("/keywords", h.listKeywords)
	group.POST("/keywords", h.createKeyword)
	group.PUT("/keywords/:id", h.updateKeyword)
	group.DELETE("/keywords/:id", h.deleteKeyword)
	group.POST("/keywords/:id/toggle", h.toggleKeyword)
	group.GET("/runs", h.listRuns)
	group.POST("/runs", h.createRun)
	group.GET("/runs/:id", h.getRun)
	group.GET("/runs/:id/items", h.listRunItems)
	group.GET("/runs/:id/items/:itemId/sources", h.listRunItemSources)
	group.GET("/users", h.listUsers)
	group.POST("/users", h.createUser)
	group.PUT("/users/:id", h.updateUser)
	group.DELETE("/users/:id", h.deleteUser)
	group.POST("/users/:id/toggle", h.toggleUser)
	group.POST("/users/:id/reset-password", h.resetUserPassword)
	group.POST("/users/:id/reset-api-key", h.resetUserAPIKey)
	group.POST("/users/:id/revoke-api-key", h.revokeUserAPIKey)
	group.GET("/usage/overview", h.adminUsageOverview)
	group.GET("/usage/trends", h.adminUsageTrends)
	group.GET("/usage/logs", h.adminUsageLogs)
	h.registerSourceRoutes(group)
	h.registerKeywordAPISourceRoutes(group)
}

func (h *AdminHandler) available(c *gin.Context) bool {
	if h == nil || h.store == nil {
		c.JSON(http.StatusServiceUnavailable, model.NewErrorResponse(http.StatusServiceUnavailable, "资源库未配置"))
		return false
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	if err := h.store.Ping(ctx); err != nil {
		c.JSON(http.StatusServiceUnavailable, model.NewErrorResponse(http.StatusServiceUnavailable, "资源库暂不可用"))
		return false
	}
	return true
}

func (h *AdminHandler) overview(c *gin.Context) {
	if !h.overviewAvailable(c) {
		return
	}
	days := normalizeOverviewDays(queryInt(c, "days", defaultAdminOverviewTrendDays))
	force := queryBool(c, "force", false)
	snapshot, err := h.overviewCache.dashboard(c.Request.Context(), days, force)
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(adminOverviewResponse{
		OverviewStats: snapshot.stats,
		Trends:        snapshot.trends,
		GeneratedAt:   snapshot.generatedAt,
		Stale:         snapshot.stale,
		Refreshing:    snapshot.refreshing,
	}))
}

func (h *AdminHandler) trends(c *gin.Context) {
	if !h.overviewAvailable(c) {
		return
	}
	days := normalizeOverviewDays(queryInt(c, "days", defaultAdminOverviewTrendDays))
	snapshot, err := h.overviewCache.snapshot(c.Request.Context(), days, false)
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(snapshot.trends))
}

func (h *AdminHandler) overviewAvailable(c *gin.Context) bool {
	if h == nil || h.store == nil || h.overviewCache == nil {
		c.JSON(http.StatusServiceUnavailable, model.NewErrorResponse(http.StatusServiceUnavailable, "资源库未配置"))
		return false
	}
	return true
}

func (h *AdminHandler) listResources(c *gin.Context) {
	if !h.available(c) {
		return
	}
	filter := storage.ResourceFilter{
		Keyword:        strings.TrimSpace(c.Query("keyword")),
		KeywordType:    strings.TrimSpace(c.Query("keyword_type")),
		Query:          strings.TrimSpace(c.Query("q")),
		Platforms:      queryList(c, "platform", "platforms", "disk_type"),
		CheckStatuses:  queryList(c, "status", "check_status"),
		SourceTypes:    queryList(c, "source_type", "source_types"),
		SourceKeys:     queryList(c, "source", "source_key"),
		IncludeInvalid: queryBool(c, "include_invalid", false),
		Page:           queryInt(c, "page", 1),
		PageSize:       queryInt(c, "page_size", 50),
		Sort:           strings.TrimSpace(c.Query("sort")),
	}
	if len(filter.SourceTypes) == 0 {
		legacySource := strings.TrimSpace(c.Query("source"))
		if legacySource == "tg" || legacySource == "plugin" || legacySource == "external" {
			filter.SourceTypes = []string{legacySource}
			filter.SourceKeys = nil
		}
	}
	fromValue := c.Query("from")
	if fromValue == "" {
		fromValue = c.Query("date_from")
	}
	from, err := queryTimeBoundary(fromValue, false)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "from 必须是 RFC3339 时间"))
		return
	}
	toValue := c.Query("to")
	if toValue == "" {
		toValue = c.Query("date_to")
	}
	to, err := queryTimeBoundary(toValue, true)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "to 必须是 RFC3339 时间"))
		return
	}
	filter.From, filter.To = from, to
	page, err := h.store.ListResourceSummaries(c.Request.Context(), filter)
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(page))
}

func (h *AdminHandler) getResource(c *gin.Context) {
	if !h.available(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	resource, err := h.store.GetResource(c.Request.Context(), id)
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(resource))
}

func (h *AdminHandler) listResourceSources(c *gin.Context) {
	if !h.available(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	page, err := h.store.ListResourceSources(c.Request.Context(), id, storage.ResourceAssociationFilter{
		Page: queryInt(c, "page", 1), PageSize: queryInt(c, "page_size", 50),
	})
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(page))
}

func (h *AdminHandler) listResourceKeywords(c *gin.Context) {
	if !h.available(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	page, err := h.store.ListResourceKeywords(c.Request.Context(), id, storage.ResourceAssociationFilter{
		Page: queryInt(c, "page", 1), PageSize: queryInt(c, "page_size", 50),
	})
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(page))
}

func (h *AdminHandler) listKeywords(c *gin.Context) {
	if !h.available(c) {
		return
	}
	filter := storage.KeywordFilter{
		Query:       strings.TrimSpace(c.Query("q")),
		KeywordType: strings.TrimSpace(c.Query("keyword_type")),
		SourceType:  strings.TrimSpace(c.Query("source_type")),
		Page:        queryInt(c, "page", 1),
		PageSize:    queryInt(c, "page_size", 50),
	}
	if value, exists := c.GetQuery("enabled"); exists {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "enabled 必须是布尔值"))
			return
		}
		filter.Enabled = &enabled
	}
	page, err := h.store.ListKeywords(c.Request.Context(), filter)
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(page))
}

type createKeywordRequest struct {
	Keyword         string         `json:"keyword" binding:"required"`
	KeywordType     string         `json:"keyword_type"`
	SourceType      string         `json:"source_type"`
	SourceKey       string         `json:"source_key"`
	ExternalID      string         `json:"external_id"`
	SourceMetadata  map[string]any `json:"source_metadata"`
	Enabled         *bool          `json:"enabled"`
	Priority        int            `json:"priority"`
	CooldownSeconds *int64         `json:"cooldown_seconds"`
	CooldownDays    *float64       `json:"cooldown_days"`
}

func (h *AdminHandler) createKeyword(c *gin.Context) {
	if !h.available(c) {
		return
	}
	var request createKeywordRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "关键词参数无效: "+err.Error()))
		return
	}
	cooldownSeconds := request.CooldownSeconds
	if cooldownSeconds == nil && request.CooldownDays != nil {
		value := int64(*request.CooldownDays * 24 * 60 * 60)
		cooldownSeconds = &value
	}
	keyword, err := h.store.CreateKeyword(c.Request.Context(), storage.CreateKeywordInput{
		Keyword: request.Keyword, KeywordType: request.KeywordType, SourceType: request.SourceType,
		SourceKey: request.SourceKey, ExternalID: request.ExternalID, SourceMetadata: request.SourceMetadata,
		Enabled: request.Enabled, Priority: request.Priority, CooldownSeconds: cooldownSeconds,
	})
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusCreated, model.NewSuccessResponse(keyword))
}

type updateKeywordRequest struct {
	Keyword         *string
	KeywordType     *string
	SourceType      *string
	SourceKey       *string
	ExternalID      *string
	SourceMetadata  *map[string]any
	Enabled         *bool
	Priority        *int
	CooldownSeconds **int64
}

func (r *updateKeywordRequest) UnmarshalJSON(data []byte) error {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	decode := func(name string, target any) error {
		raw, exists := values[name]
		if !exists {
			return nil
		}
		return json.Unmarshal(raw, target)
	}
	if err := decode("keyword", &r.Keyword); err != nil {
		return err
	}
	if err := decode("keyword_type", &r.KeywordType); err != nil {
		return err
	}
	if err := decode("source_type", &r.SourceType); err != nil {
		return err
	}
	if err := decode("source_key", &r.SourceKey); err != nil {
		return err
	}
	if err := decode("external_id", &r.ExternalID); err != nil {
		return err
	}
	if err := decode("source_metadata", &r.SourceMetadata); err != nil {
		return err
	}
	if err := decode("enabled", &r.Enabled); err != nil {
		return err
	}
	if err := decode("priority", &r.Priority); err != nil {
		return err
	}
	if raw, exists := values["cooldown_seconds"]; exists {
		var value *int64
		if string(raw) != "null" {
			var parsed int64
			if err := json.Unmarshal(raw, &parsed); err != nil {
				return err
			}
			value = &parsed
		}
		r.CooldownSeconds = &value
	} else if raw, exists := values["cooldown_days"]; exists {
		var days *float64
		if string(raw) != "null" {
			var parsed float64
			if err := json.Unmarshal(raw, &parsed); err != nil {
				return err
			}
			days = &parsed
		}
		var value *int64
		if days != nil {
			seconds := int64(*days * 24 * 60 * 60)
			value = &seconds
		}
		r.CooldownSeconds = &value
	}
	return nil
}

func (h *AdminHandler) updateKeyword(c *gin.Context) {
	if !h.available(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	var request updateKeywordRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "关键词参数无效: "+err.Error()))
		return
	}
	keyword, err := h.store.UpdateKeyword(c.Request.Context(), id, storage.UpdateKeywordInput{
		Keyword: request.Keyword, KeywordType: request.KeywordType, SourceType: request.SourceType,
		SourceKey: request.SourceKey, ExternalID: request.ExternalID, SourceMetadata: request.SourceMetadata,
		Enabled: request.Enabled, Priority: request.Priority, CooldownSeconds: request.CooldownSeconds,
	})
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(keyword))
}

func (h *AdminHandler) deleteKeyword(c *gin.Context) {
	if !h.available(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	if err := h.store.DeleteKeyword(c.Request.Context(), id); err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{"deleted": true}))
}

func (h *AdminHandler) toggleKeyword(c *gin.Context) {
	if !h.available(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	keyword, err := h.store.GetKeyword(c.Request.Context(), id)
	if err != nil {
		respondAdminError(c, err)
		return
	}
	enabled := !keyword.Enabled
	var payload struct {
		Enabled *bool `json:"enabled"`
	}
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&payload); err != nil {
			c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "enabled 参数无效"))
			return
		}
		if payload.Enabled != nil {
			enabled = *payload.Enabled
		}
	}
	keyword, err = h.store.UpdateKeyword(c.Request.Context(), id, storage.UpdateKeywordInput{Enabled: &enabled})
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(keyword))
}

func (h *AdminHandler) listRuns(c *gin.Context) {
	if !h.available(c) {
		return
	}
	filter := storage.RunFilter{
		Trigger:  strings.TrimSpace(c.Query("trigger")),
		Statuses: queryList(c, "status", "statuses"),
		Page:     queryInt(c, "page", 1),
		PageSize: queryInt(c, "page_size", 30),
	}
	from, err := queryTime(c.Query("from"))
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "from 必须是 RFC3339 时间"))
		return
	}
	to, err := queryTime(c.Query("to"))
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "to 必须是 RFC3339 时间"))
		return
	}
	filter.From, filter.To = from, to
	page, err := h.store.ListRuns(c.Request.Context(), filter)
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(page))
}

func (h *AdminHandler) createRun(c *gin.Context) {
	if !h.available(c) {
		return
	}
	if h.runner == nil {
		c.JSON(http.StatusServiceUnavailable, model.NewErrorResponse(http.StatusServiceUnavailable, "采集调度器暂不可用"))
		return
	}
	var request struct {
		KeywordIDs []int64 `json:"keyword_ids" binding:"required,min=1"`
		Force      bool    `json:"force"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "请选择至少一个关键词"))
		return
	}
	batch, err := h.runner.StartManual(c.Request.Context(), request.KeywordIDs, request.Force)
	if err != nil {
		switch {
		case errors.Is(err, collection.ErrBatchRunning):
			c.JSON(http.StatusConflict, model.NewErrorResponse(http.StatusConflict, err.Error()))
		case errors.Is(err, collection.ErrNoEligibleKeyword):
			c.JSON(http.StatusUnprocessableEntity, model.NewErrorResponse(http.StatusUnprocessableEntity, err.Error()))
		default:
			respondAdminError(c, err)
		}
		return
	}
	c.JSON(http.StatusAccepted, model.NewSuccessResponse(batch))
}

func (h *AdminHandler) getRun(c *gin.Context) {
	if !h.available(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	run, err := h.store.GetRunSummary(c.Request.Context(), id)
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(run))
}

func (h *AdminHandler) listRunItems(c *gin.Context) {
	if !h.available(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	page, err := h.store.ListRunItems(c.Request.Context(), id, storage.RunItemFilter{
		Query: strings.TrimSpace(c.Query("q")), Statuses: queryList(c, "status", "statuses"),
		Page: queryInt(c, "page", 1), PageSize: queryInt(c, "page_size", 30),
	})
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(page))
}

func (h *AdminHandler) listRunItemSources(c *gin.Context) {
	if !h.available(c) {
		return
	}
	runID, ok := pathParamID(c, "id")
	if !ok {
		return
	}
	itemID, ok := pathParamID(c, "itemId")
	if !ok {
		return
	}
	page, err := h.store.ListRunItemSources(c.Request.Context(), runID, itemID, storage.RunSourceFilter{
		Types: queryList(c, "type", "types", "source_type"), Statuses: queryList(c, "status", "statuses"),
		Page: queryInt(c, "page", 1), PageSize: queryInt(c, "page_size", 50),
	})
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(page))
}

func respondAdminError(c *gin.Context, err error) {
	status := http.StatusServiceUnavailable
	switch {
	case errors.Is(err, storage.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, storage.ErrConflict):
		status = http.StatusConflict
	case errors.Is(err, storage.ErrInvalid):
		status = http.StatusBadRequest
	}
	c.JSON(status, model.NewErrorResponse(status, err.Error()))
}

func pathID(c *gin.Context) (int64, bool) {
	return pathParamID(c, "id")
}

func pathParamID(c *gin.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, name+" 必须是正整数"))
		return 0, false
	}
	return id, true
}

func queryInt(c *gin.Context, name string, fallback int) int {
	value := strings.TrimSpace(c.Query(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func queryBool(c *gin.Context, name string, fallback bool) bool {
	value := strings.TrimSpace(c.Query(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func queryList(c *gin.Context, names ...string) []string {
	for _, name := range names {
		values := c.QueryArray(name)
		if len(values) == 0 {
			continue
		}
		var result []string
		for _, value := range values {
			for _, part := range strings.Split(value, ",") {
				if trimmed := strings.TrimSpace(part); trimmed != "" {
					result = append(result, trimmed)
				}
			}
		}
		return result
	}
	return nil
}

func queryTime(value string) (*time.Time, error) {
	return queryTimeBoundary(value, false)
}

func queryTimeBoundary(value string, endOfDate bool) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		parsed, err = time.Parse("2006-01-02", value)
		if err == nil && endOfDate {
			parsed = parsed.AddDate(0, 0, 1)
		}
	}
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
