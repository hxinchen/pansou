package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"pansou/model"
	"pansou/sourceconfig"
	"pansou/storage"
)

type applyTierSuggestionsRequest struct {
	ExpectedVersion int64 `json:"expected_version"`
	Days            int   `json:"days"`
}

func (h *AdminHandler) searchSchedulerSuggestions(c *gin.Context) {
	if !h.available(c) {
		return
	}
	days := queryInt(c, "days", 14)
	if days < 1 {
		days = 14
	}
	items, err := h.store.SearchSourceTierSuggestions(c.Request.Context(), days)
	if err != nil {
		respondAdminError(c, err)
		return
	}
	current := make(map[string]string)
	if h.sources != nil {
		if state, currentErr := h.sources.Current(c.Request.Context()); currentErr == nil {
			for _, channel := range state.Config.Channels {
				current["tg:"+strings.ToLower(channel.Key)] = channel.Tier
			}
			for name, settings := range state.Config.Plugins {
				current["plugin:"+strings.ToLower(name)] = settings.Tier
			}
		}
	}
	for index := range items {
		items[index].CurrentTier = current[strings.ToLower(items[index].Source)]
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{"days": days, "items": items}))
}

func (h *AdminHandler) applySearchSchedulerSuggestions(c *gin.Context) {
	if !h.available(c) {
		return
	}
	if h.sources == nil {
		c.JSON(http.StatusServiceUnavailable, model.NewErrorResponse(http.StatusServiceUnavailable, "搜索来源运行时未启用"))
		return
	}
	var request applyTierSuggestionsRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.ExpectedVersion <= 0 {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "分层应用参数无效"))
		return
	}
	if request.Days <= 0 {
		request.Days = 14
	}
	principal, ok := currentPrincipal(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, model.NewErrorResponse(http.StatusUnauthorized, "未登录"))
		return
	}
	current, err := h.sources.Current(c.Request.Context())
	if err != nil {
		respondSourceError(c, err)
		return
	}
	if current.Version != request.ExpectedVersion {
		c.JSON(http.StatusConflict, model.NewErrorResponse(http.StatusConflict, "来源配置已更新，请刷新后重试"))
		return
	}
	suggestions, err := h.store.SearchSourceTierSuggestions(c.Request.Context(), request.Days)
	if err != nil {
		respondAdminError(c, err)
		return
	}
	config, applied, eligible := applySuggestedTiers(current.Config, suggestions)
	if applied == 0 {
		c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{"applied": 0, "eligible": eligible, "state": current}))
		return
	}
	actor := principal.UserID
	updated, err := h.sources.Update(c.Request.Context(), request.ExpectedVersion, config, &actor)
	if err != nil {
		respondSourceError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(gin.H{"applied": applied, "eligible": eligible, "state": updated}))
}

func applySuggestedTiers(config sourceconfig.Config, suggestions []storage.SourceTierSuggestion) (sourceconfig.Config, int, int) {
	bySource := make(map[string]string, len(suggestions))
	eligible := 0
	for _, suggestion := range suggestions {
		if !suggestion.Eligible || suggestion.SuggestedTier == "" {
			continue
		}
		bySource[strings.ToLower(suggestion.Source)] = suggestion.SuggestedTier
		eligible++
	}
	applied := 0
	for index := range config.Channels {
		if tier := bySource["tg:"+strings.ToLower(config.Channels[index].Key)]; tier != "" && config.Channels[index].Tier != tier {
			config.Channels[index].Tier = tier
			applied++
		}
	}
	for name, settings := range config.Plugins {
		if tier := bySource["plugin:"+strings.ToLower(name)]; tier != "" && settings.Tier != tier {
			settings.Tier = tier
			config.Plugins[name] = settings
			applied++
		}
	}
	return config, applied, eligible
}
