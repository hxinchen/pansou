package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"pansou/keywordsource"
	"pansou/model"
	"pansou/storage"
)

type keywordAPISourceRequest struct {
	Name                           string            `json:"name"`
	Enabled                        bool              `json:"enabled"`
	RequestExecutor                string            `json:"request_executor"`
	RequestMethod                  string            `json:"request_method"`
	RequestURL                     string            `json:"request_url"`
	RequestHeaders                 flexibleStringMap `json:"request_headers"`
	QueryParams                    flexibleStringMap `json:"query_params"`
	BodyType                       string            `json:"body_type"`
	RequestBody                    any               `json:"request_body"`
	ProxyURL                       string            `json:"proxy_url"`
	TimeoutSeconds                 int               `json:"timeout_seconds"`
	ResponsePath                   string            `json:"response_path"`
	SyncIntervalSeconds            int64             `json:"sync_interval_seconds"`
	DefaultKeywordType             string            `json:"default_keyword_type"`
	DefaultPriority                int               `json:"default_priority"`
	DefaultCooldownSeconds         *int64            `json:"default_cooldown_seconds"`
	DefaultEnabled                 *bool             `json:"default_enabled"`
	IterationEnabled               bool              `json:"iteration_enabled"`
	IterationLocation              string            `json:"iteration_location"`
	IterationPath                  string            `json:"iteration_path"`
	IterationStart                 int64             `json:"iteration_start"`
	IterationStep                  int64             `json:"iteration_step"`
	IterationCount                 int               `json:"iteration_count"`
	IterationDelaySeconds          int               `json:"iteration_delay_seconds"`
	IterationUnlimited             bool              `json:"iteration_unlimited"`
	IterationNoKeywordStopCount    int               `json:"iteration_no_keyword_stop_count"`
	IterationRandomDelayMinSeconds int               `json:"iteration_random_delay_min_seconds"`
	IterationRandomDelayMaxSeconds int               `json:"iteration_random_delay_max_seconds"`
}

// flexibleStringMap lets the JSON editor use natural scalar values such as
// {"start": 0, "enabled": true}; outbound HTTP parameters remain strings.
type flexibleStringMap map[string]string

func (m *flexibleStringMap) UnmarshalJSON(data []byte) error {
	if string(data) == "null" || len(data) == 0 {
		*m = flexibleStringMap{}
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("必须是 JSON 对象")
	}
	result := make(flexibleStringMap, len(raw))
	for key, item := range raw {
		var text string
		if err := json.Unmarshal(item, &text); err == nil {
			result[key] = text
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(string(item)))
		decoder.UseNumber()
		var scalar any
		if err := decoder.Decode(&scalar); err != nil {
			return fmt.Errorf("字段 %s 的值无效", key)
		}
		switch value := scalar.(type) {
		case json.Number:
			result[key] = value.String()
		case bool:
			result[key] = strconv.FormatBool(value)
		case nil:
			result[key] = ""
		default:
			return fmt.Errorf("字段 %s 只能使用字符串、数字、布尔值或 null", key)
		}
	}
	*m = result
	return nil
}

func stringMap(value flexibleStringMap) map[string]string {
	result := make(map[string]string, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

type keywordAPISourceListItem struct {
	ID                        int64                      `json:"id"`
	Name                      string                     `json:"name"`
	Enabled                   bool                       `json:"enabled"`
	RequestExecutor           string                     `json:"request_executor"`
	RequestMethod             string                     `json:"request_method"`
	RequestURL                string                     `json:"request_url"`
	SyncIntervalSeconds       int64                      `json:"sync_interval_seconds"`
	NextSyncAt                *time.Time                 `json:"next_sync_at,omitempty"`
	LastSyncedAt              *time.Time                 `json:"last_synced_at,omitempty"`
	LastStatus                string                     `json:"last_status"`
	LastError                 string                     `json:"last_error,omitempty"`
	LastItemCount             int                        `json:"last_item_count"`
	LastRequestCount          int                        `json:"last_request_count"`
	LastSuccessCount          int                        `json:"last_success_count"`
	LastFailureCount          int                        `json:"last_failure_count"`
	SyncConfigRevision        int64                      `json:"sync_config_revision"`
	LastAppliedConfigRevision int64                      `json:"last_applied_config_revision"`
	ResultStale               bool                       `json:"result_stale"`
	ActiveRun                 *storage.KeywordAPISyncRun `json:"active_run"`
	LatestRun                 *storage.KeywordAPISyncRun `json:"latest_run"`
}

type keywordAPISyncRequest struct {
	Trigger string `json:"trigger"`
}

func (h *AdminHandler) registerKeywordAPISourceRoutes(group *gin.RouterGroup) {
	group.GET("/keyword-api-sources", h.listKeywordAPISources)
	group.GET("/keyword-api-sources/:id", h.getKeywordAPISource)
	group.POST("/keyword-api-sources", h.createKeywordAPISource)
	group.PUT("/keyword-api-sources/:id", h.updateKeywordAPISource)
	group.DELETE("/keyword-api-sources/:id", h.deleteKeywordAPISource)
	group.POST("/keyword-api-sources/test", h.testKeywordAPISource)
	group.POST("/keyword-api-sources/:id/sync", h.syncKeywordAPISource)
	group.POST("/keyword-api-sources/:id/copy", h.copyKeywordAPISource)
	group.GET("/keyword-api-sync-runs", h.listKeywordAPISyncRuns)
	group.GET("/keyword-api-sync-runs/:id", h.getKeywordAPISyncRun)
	group.GET("/keyword-api-sync-runs/:id/iterations", h.listKeywordAPISyncRunIterations)
}

func (h *AdminHandler) listKeywordAPISources(c *gin.Context) {
	if !h.available(c) {
		return
	}
	filter := storage.KeywordAPISourceFilter{
		Query: strings.TrimSpace(c.Query("q")), Page: queryInt(c, "page", 1), PageSize: queryInt(c, "page_size", 50),
		Statuses: queryList(c, "status", "statuses"),
		SortBy:   strings.TrimSpace(c.Query("sort_by")), SortDir: strings.TrimSpace(c.Query("sort_dir")),
	}
	if value := strings.TrimSpace(c.Query("enabled")); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "enabled 必须是布尔值"))
			return
		}
		filter.Enabled = &parsed
	}
	page, err := h.store.ListKeywordAPISources(c.Request.Context(), filter)
	if err != nil {
		respondAdminError(c, err)
		return
	}
	items := make([]keywordAPISourceListItem, 0, len(page.Items))
	for _, source := range page.Items {
		items = append(items, keywordAPISourceListItemFrom(source))
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{"items": items, "total": page.Total, "page": page.Page, "page_size": page.PageSize}))
}

func keywordAPISourceListItemFrom(source storage.KeywordAPISource) keywordAPISourceListItem {
	return keywordAPISourceListItem{
		ID: source.ID, Name: source.Name, Enabled: source.Enabled, RequestExecutor: source.RequestExecutor, RequestMethod: source.RequestMethod,
		RequestURL: redactKeywordSourceListURL(source.RequestURL), SyncIntervalSeconds: source.SyncIntervalSeconds,
		NextSyncAt: source.NextSyncAt, LastSyncedAt: source.LastSyncedAt, LastStatus: source.LastStatus,
		LastError: source.LastError, LastItemCount: source.LastItemCount,
		LastRequestCount: source.LastRequestCount, LastSuccessCount: source.LastSuccessCount,
		LastFailureCount: source.LastFailureCount, SyncConfigRevision: source.SyncConfigRevision,
		LastAppliedConfigRevision: source.LastAppliedConfigRevision, ResultStale: source.ResultStale,
		ActiveRun: source.ActiveRun, LatestRun: source.LatestRun,
	}
}

func (h *AdminHandler) getKeywordAPISource(c *gin.Context) {
	if !h.available(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	source, err := h.store.GetKeywordAPISource(c.Request.Context(), id)
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(keywordAPISourceDetail(source)))
}

func (h *AdminHandler) createKeywordAPISource(c *gin.Context) {
	if !h.available(c) {
		return
	}
	request, body, ok := bindKeywordAPISource(c)
	if !ok || !validateKeywordAPISourceRequest(c, request, body, request.Enabled) {
		return
	}
	created, err := h.store.CreateKeywordAPISource(c.Request.Context(), storage.CreateKeywordAPISourceInput{
		Name: request.Name, Enabled: request.Enabled, RequestExecutor: request.RequestExecutor,
		RequestMethod: request.RequestMethod, RequestURL: request.RequestURL,
		RequestHeaders: stringMap(request.RequestHeaders), QueryParams: stringMap(request.QueryParams), BodyType: request.BodyType, RequestBody: body,
		ProxyURL: request.ProxyURL, TimeoutSeconds: request.TimeoutSeconds, ResponsePath: request.ResponsePath,
		SyncIntervalSeconds: request.SyncIntervalSeconds, DefaultKeywordType: request.DefaultKeywordType,
		DefaultKeywordEnabled: request.DefaultEnabled, DefaultPriority: request.DefaultPriority,
		DefaultCooldownSeconds: request.DefaultCooldownSeconds,
		IterationEnabled:       request.IterationEnabled, IterationLocation: request.IterationLocation,
		IterationPath: request.IterationPath, IterationStart: request.IterationStart, IterationStep: request.IterationStep,
		IterationCount: request.IterationCount, IterationDelaySeconds: request.IterationDelaySeconds,
		IterationUnlimited: request.IterationUnlimited, IterationNoKeywordStopCount: request.IterationNoKeywordStopCount,
		IterationRandomDelayMinSeconds: request.IterationRandomDelayMinSeconds,
		IterationRandomDelayMaxSeconds: request.IterationRandomDelayMaxSeconds,
	})
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusCreated, model.NewSuccessResponse(keywordAPISourceDetail(created)))
}

func (h *AdminHandler) updateKeywordAPISource(c *gin.Context) {
	if !h.available(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	request, body, ok := bindKeywordAPISource(c)
	if !ok || !validateKeywordAPISourceRequest(c, request, body, request.Enabled) {
		return
	}
	defaultEnabled := request.DefaultEnabled
	if defaultEnabled == nil {
		value := true
		defaultEnabled = &value
	}
	updated, err := h.store.UpdateKeywordAPISource(c.Request.Context(), id, storage.UpdateKeywordAPISourceInput{
		Name: &request.Name, Enabled: &request.Enabled, RequestExecutor: &request.RequestExecutor,
		RequestMethod: &request.RequestMethod, RequestURL: &request.RequestURL,
		RequestHeaders: mapPointer(stringMap(request.RequestHeaders)), QueryParams: mapPointer(stringMap(request.QueryParams)), BodyType: &request.BodyType, RequestBody: &body,
		ProxyURL: &request.ProxyURL, TimeoutSeconds: &request.TimeoutSeconds, ResponsePath: &request.ResponsePath,
		SyncIntervalSeconds: &request.SyncIntervalSeconds, DefaultKeywordType: &request.DefaultKeywordType,
		DefaultKeywordEnabled: defaultEnabled, DefaultPriority: &request.DefaultPriority,
		DefaultCooldownSeconds: &request.DefaultCooldownSeconds,
		IterationEnabled:       &request.IterationEnabled, IterationLocation: &request.IterationLocation,
		IterationPath: &request.IterationPath, IterationStart: &request.IterationStart, IterationStep: &request.IterationStep,
		IterationCount: &request.IterationCount, IterationDelaySeconds: &request.IterationDelaySeconds,
		IterationUnlimited: &request.IterationUnlimited, IterationNoKeywordStopCount: &request.IterationNoKeywordStopCount,
		IterationRandomDelayMinSeconds: &request.IterationRandomDelayMinSeconds,
		IterationRandomDelayMaxSeconds: &request.IterationRandomDelayMaxSeconds,
	})
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(keywordAPISourceDetail(updated)))
}

func (h *AdminHandler) deleteKeywordAPISource(c *gin.Context) {
	if !h.available(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	if err := h.store.DeleteKeywordAPISource(c.Request.Context(), id); err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{"deleted": true}))
}

func (h *AdminHandler) copyKeywordAPISource(c *gin.Context) {
	if !h.available(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	copied, err := h.store.CopyKeywordAPISource(c.Request.Context(), id)
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusCreated, model.NewSuccessResponse(keywordAPISourceDetail(copied)))
}

func (h *AdminHandler) testKeywordAPISource(c *gin.Context) {
	if !h.available(c) {
		return
	}
	request, body, ok := bindKeywordAPISource(c)
	if !ok || !validateKeywordAPISourceRequest(c, request, body, false) {
		return
	}
	config, err := keywordAPISourceRequestConfig(request, body)
	if err != nil {
		respondKeywordAPISourceError(c, err, config)
		return
	}
	iteration := keywordAPISourceIterationConfig(request)
	config, iterationValue, err := keywordsource.DeriveRequest(config, iteration, 0)
	if err != nil {
		respondKeywordAPISourceError(c, err, config)
		return
	}
	result, err := keywordsource.Test(c.Request.Context(), config, request.ResponsePath)
	if err != nil {
		respondKeywordAPISourceError(c, err, config)
		return
	}
	candidates, candidatesTotal := keywordAPITestCandidates(result.Candidates)
	extraction := keywordAPITestExtraction(result.Extraction)
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{
		"status_code": result.StatusCode, "duration_ms": result.Duration.Milliseconds(), "response_size": result.SizeBytes,
		"content_type": result.ContentType, "candidates": candidates, "candidates_total": candidatesTotal,
		"candidates_truncated": candidatesTotal > len(candidates), "extraction": extraction,
		"iteration_value": iterationValue,
	}))
}

func keywordAPITestCandidates(values []keywordsource.FieldCandidate) ([]keywordsource.FieldCandidate, int) {
	const maxCandidates = 100
	count := len(values)
	if len(values) > maxCandidates {
		values = values[:maxCandidates]
	}
	result := make([]keywordsource.FieldCandidate, len(values))
	for index, candidate := range values {
		candidate.Path = truncateRunText(candidate.Path, 300)
		candidate.Samples = append([]string(nil), candidate.Samples...)
		for sampleIndex := range candidate.Samples {
			candidate.Samples[sampleIndex] = truncateRunText(candidate.Samples[sampleIndex], 160)
		}
		result[index] = candidate
	}
	return result, count
}

func keywordAPITestExtraction(value *keywordsource.ExtractionResult) *keywordsource.ExtractionResult {
	if value == nil {
		return nil
	}
	const maxValues = 20
	result := *value
	result.Path = truncateRunText(result.Path, 300)
	result.Values = append([]keywordsource.KeywordValue(nil), value.Values...)
	if len(result.Values) > maxValues {
		result.Values = result.Values[:maxValues]
	}
	for index := range result.Values {
		result.Values[index].Value = truncateRunText(result.Values[index].Value, 200)
		result.Values[index].Normalized = truncateRunText(result.Values[index].Normalized, 200)
	}
	return &result
}

func truncateRunText(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}

func redactKeywordSourceListURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	query := parsed.Query()
	for key := range query {
		query.Set(key, "[REDACTED]")
	}
	parsed.RawQuery = query.Encode()
	if parsed.User != nil {
		parsed.User = url.UserPassword(parsed.User.Username(), "[REDACTED]")
	}
	return parsed.String()
}

func (h *AdminHandler) syncKeywordAPISource(c *gin.Context) {
	if !h.available(c) {
		return
	}
	if h.keywordSources == nil {
		c.JSON(http.StatusServiceUnavailable, model.NewErrorResponse(http.StatusServiceUnavailable, "API 关键词同步服务未启动"))
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	trigger, ok := bindKeywordAPISyncTrigger(c)
	if !ok {
		return
	}
	run, alreadyActive, err := h.keywordSources.TriggerNow(c.Request.Context(), id, trigger)
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, model.NewSuccessResponse(keywordAPISyncAcceptedResponse(id, run, alreadyActive)))
}

func keywordAPISyncAcceptedResponse(sourceID int64, run storage.KeywordAPISyncRun, alreadyActive bool) gin.H {
	return gin.H{
		"id": sourceID, "status": storage.KeywordAPISourceStatusRunning, "accepted": true,
		"run_status": run.Status,
		"run_id":     run.ID, "run": run, "already_active": alreadyActive,
	}
}

func (h *AdminHandler) listKeywordAPISyncRuns(c *gin.Context) {
	if !h.available(c) {
		return
	}
	filter, err := keywordAPISyncRunFilter(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, err.Error()))
		return
	}
	page, err := h.store.ListKeywordAPISyncRuns(c.Request.Context(), filter)
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(page))
}

func (h *AdminHandler) getKeywordAPISyncRun(c *gin.Context) {
	if !h.available(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	run, err := h.store.GetKeywordAPISyncRunSummary(c.Request.Context(), id)
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(run))
}

func (h *AdminHandler) listKeywordAPISyncRunIterations(c *gin.Context) {
	if !h.available(c) {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	page, err := h.store.ListKeywordAPISyncRunIterations(c.Request.Context(), id, storage.KeywordAPISyncIterationFilter{
		Page: queryInt(c, "page", 1), PageSize: queryInt(c, "page_size", 50),
		SortBy: strings.TrimSpace(c.Query("sort_by")), SortDir: strings.TrimSpace(c.Query("sort_dir")),
	})
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(page))
}

func bindKeywordAPISyncTrigger(c *gin.Context) (string, bool) {
	request := keywordAPISyncRequest{Trigger: "manual"}
	if err := c.ShouldBindJSON(&request); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "请求 JSON 无效"))
		return "", false
	}
	request.Trigger = strings.ToLower(strings.TrimSpace(request.Trigger))
	if request.Trigger == "" {
		request.Trigger = "manual"
	}
	if request.Trigger != "manual" && request.Trigger != "save" {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "trigger 仅支持 manual 或 save"))
		return "", false
	}
	return request.Trigger, true
}

func keywordAPISyncRunFilter(c *gin.Context) (storage.KeywordAPISyncRunFilter, error) {
	filter := storage.KeywordAPISyncRunFilter{
		Statuses: queryList(c, "status", "statuses"), Triggers: queryList(c, "trigger", "triggers"),
		Page: queryInt(c, "page", 1), PageSize: queryInt(c, "page_size", 20),
		SortBy: strings.TrimSpace(c.Query("sort_by")), SortDir: strings.TrimSpace(c.Query("sort_dir")),
	}
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.PageSize < 1 {
		filter.PageSize = 20
	} else if filter.PageSize > 200 {
		filter.PageSize = 200
	}
	if raw := strings.TrimSpace(c.Query("source_id")); raw != "" {
		sourceID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || sourceID <= 0 {
			return filter, fmt.Errorf("source_id 必须是正整数")
		}
		filter.SourceID = &sourceID
	}
	fromValue := c.Query("from")
	if fromValue == "" {
		fromValue = c.Query("date_from")
	}
	from, err := queryTimeBoundary(fromValue, false)
	if err != nil {
		return filter, fmt.Errorf("from 必须是 RFC3339 时间或日期")
	}
	toValue := c.Query("to")
	if toValue == "" {
		toValue = c.Query("date_to")
	}
	to, err := queryTimeBoundary(toValue, true)
	if err != nil {
		return filter, fmt.Errorf("to 必须是 RFC3339 时间或日期")
	}
	if from != nil && to != nil && !from.Before(*to) {
		return filter, fmt.Errorf("from 必须早于 to")
	}
	filter.From, filter.To = from, to
	return filter, nil
}

func bindKeywordAPISource(c *gin.Context) (keywordAPISourceRequest, string, bool) {
	var request keywordAPISourceRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "请求 JSON 无效"))
		return request, "", false
	}
	body, err := encodeKeywordAPISourceBody(request.BodyType, request.RequestBody)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, err.Error()))
		return request, "", false
	}
	return request, body, true
}

func encodeKeywordAPISourceBody(bodyType string, value any) (string, error) {
	if value == nil {
		return "", nil
	}
	if text, ok := value.(string); ok {
		if strings.EqualFold(bodyType, "form") && strings.TrimSpace(text) != "" {
			var fields flexibleStringMap
			if err := json.Unmarshal([]byte(text), &fields); err != nil {
				return "", fmt.Errorf("Form 请求体必须是 JSON 标量键值对象: %v", err)
			}
			normalized, _ := json.Marshal(stringMap(fields))
			return string(normalized), nil
		}
		return text, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("请求体无法编码")
	}
	if strings.EqualFold(bodyType, "form") {
		var fields flexibleStringMap
		if err := json.Unmarshal(data, &fields); err != nil {
			return "", fmt.Errorf("Form 请求体必须是 JSON 标量键值对象: %v", err)
		}
		normalized, _ := json.Marshal(stringMap(fields))
		return string(normalized), nil
	}
	if strings.EqualFold(bodyType, "raw") {
		return string(data), nil
	}
	return string(data), nil
}

func validateKeywordAPISourceRequest(c *gin.Context, request keywordAPISourceRequest, body string, requirePath bool) bool {
	config, err := keywordAPISourceRequestConfig(request, body)
	if err == nil && strings.TrimSpace(request.RequestURL) != "" {
		err = keywordsource.ValidateRequestConfig(config)
	}
	if err == nil && strings.TrimSpace(request.ResponsePath) != "" {
		_, err = keywordsource.ParsePath(request.ResponsePath)
	}
	if err == nil {
		err = keywordsource.ValidateIterationConfig(config, keywordAPISourceIterationConfig(request))
	}
	if err == nil && requirePath && strings.TrimSpace(request.ResponsePath) == "" {
		err = fmt.Errorf("启用 API 来源前必须选择响应字段路径")
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, err.Error()))
		return false
	}
	return true
}

func keywordAPISourceIterationConfig(request keywordAPISourceRequest) keywordsource.IterationConfig {
	return keywordsource.IterationConfig{
		Enabled: request.IterationEnabled, Location: keywordsource.IterationLocation(request.IterationLocation),
		Path: request.IterationPath, Start: request.IterationStart, Step: request.IterationStep,
		Count: request.IterationCount, DelaySeconds: request.IterationDelaySeconds,
		Unlimited: request.IterationUnlimited, NoKeywordStopCount: request.IterationNoKeywordStopCount,
		RandomDelayMinSeconds: request.IterationRandomDelayMinSeconds,
		RandomDelayMaxSeconds: request.IterationRandomDelayMaxSeconds,
	}
}

func keywordAPISourceRequestConfig(request keywordAPISourceRequest, body string) (keywordsource.RequestConfig, error) {
	config := keywordsource.RequestConfig{
		Executor: keywordsource.RequestExecutor(request.RequestExecutor),
		Method:   request.RequestMethod, URL: request.RequestURL, Headers: stringMap(request.RequestHeaders), Query: stringMap(request.QueryParams),
		BodyType: keywordsource.BodyType(request.BodyType), Body: body, ProxyURL: request.ProxyURL, TimeoutSeconds: request.TimeoutSeconds,
	}
	if strings.EqualFold(request.BodyType, "form") && strings.TrimSpace(body) != "" {
		var fields flexibleStringMap
		if err := json.Unmarshal([]byte(body), &fields); err != nil {
			return config, fmt.Errorf("Form 请求体必须是字符串键值对象")
		}
		config.Form = stringMap(fields)
	}
	return config, nil
}

func mapPointer(value map[string]string) *map[string]string { return &value }

func keywordAPISourceDetail(source storage.KeywordAPISource) gin.H {
	var requestBody any = source.RequestBody
	if source.BodyType == "json" || source.BodyType == "form" {
		var decoded any
		if json.Unmarshal([]byte(source.RequestBody), &decoded) == nil {
			requestBody = decoded
		}
	}
	return gin.H{
		"id": source.ID, "name": source.Name, "enabled": source.Enabled, "request_executor": source.RequestExecutor, "request_method": source.RequestMethod,
		"request_url": source.RequestURL, "request_headers": source.RequestHeaders, "query_params": source.QueryParams,
		"body_type": source.BodyType, "request_body": requestBody, "proxy_url": source.ProxyURL,
		"timeout_seconds": source.TimeoutSeconds, "response_path": source.ResponsePath,
		"sync_interval_seconds": source.SyncIntervalSeconds, "default_keyword_type": source.DefaultKeywordType,
		"default_enabled": source.DefaultKeywordEnabled, "default_priority": source.DefaultPriority,
		"default_cooldown_seconds": source.DefaultCooldownSeconds, "next_sync_at": source.NextSyncAt,
		"last_synced_at": source.LastSyncedAt, "last_status": source.LastStatus, "last_error": source.LastError,
		"last_item_count": source.LastItemCount, "created_at": source.CreatedAt, "updated_at": source.UpdatedAt,
		"iteration_enabled": source.IterationEnabled, "iteration_location": source.IterationLocation,
		"iteration_path": source.IterationPath, "iteration_start": source.IterationStart, "iteration_step": source.IterationStep,
		"iteration_count": source.IterationCount, "iteration_delay_seconds": source.IterationDelaySeconds,
		"iteration_unlimited":                source.IterationUnlimited,
		"iteration_no_keyword_stop_count":    source.IterationNoKeywordStopCount,
		"iteration_random_delay_min_seconds": source.IterationRandomDelayMinSeconds,
		"iteration_random_delay_max_seconds": source.IterationRandomDelayMaxSeconds,
		"last_request_count":                 source.LastRequestCount, "last_success_count": source.LastSuccessCount,
		"last_failure_count":   source.LastFailureCount,
		"sync_config_revision": source.SyncConfigRevision, "last_applied_config_revision": source.LastAppliedConfigRevision,
		"result_stale": source.ResultStale, "active_run": source.ActiveRun, "latest_run": source.LatestRun,
	}
}

func respondKeywordAPISourceError(c *gin.Context, err error, config keywordsource.RequestConfig) {
	err = keywordsource.RedactError(err, config)
	c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, err.Error()))
}
