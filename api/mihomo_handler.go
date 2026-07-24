package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"pansou/mihomo"
	"pansou/model"
)

type mihomoRuntime interface {
	Overview(context.Context, bool) (mihomo.Overview, error)
	Select(context.Context, string, string) (mihomo.Overview, error)
	TestLatency(context.Context, string) (mihomo.LatencyTestResponse, error)
	ListSubscriptions(context.Context) ([]mihomo.Subscription, error)
	CreateSubscription(context.Context, mihomo.SubscriptionInput) (mihomo.Subscription, error)
	UpdateSubscription(context.Context, string, mihomo.SubscriptionPatch) (mihomo.Subscription, error)
	DeleteSubscription(context.Context, string) error
	UpdateSubscriptionNow(context.Context, string) (mihomo.Subscription, error)
}

type MihomoHandler struct {
	runtime mihomoRuntime
}

func NewMihomoHandler(runtime mihomoRuntime) *MihomoHandler {
	return &MihomoHandler{runtime: runtime}
}

func (h *MihomoHandler) Register(group *gin.RouterGroup) {
	group.GET("/mihomo/overview", h.overview)
	group.PUT("/mihomo/selection", h.selectNode)
	group.POST("/mihomo/latency-test", h.testLatency)
	group.GET("/mihomo/subscriptions", h.listSubscriptions)
	group.POST("/mihomo/subscriptions", h.createSubscription)
	group.PATCH("/mihomo/subscriptions/:id", h.updateSubscription)
	group.DELETE("/mihomo/subscriptions/:id", h.deleteSubscription)
	group.POST("/mihomo/subscriptions/:id/update", h.updateSubscriptionNow)
}

func (h *MihomoHandler) listSubscriptions(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	items, err := h.runtime.ListSubscriptions(c.Request.Context())
	if err != nil {
		h.respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(items))
}

func (h *MihomoHandler) createSubscription(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	var request struct {
		Name            string `json:"name"`
		URL             string `json:"url"`
		IntervalSeconds int    `json:"interval_seconds"`
		FetchVia        string `json:"fetch_via"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "订阅参数无效"))
		return
	}
	value, err := h.runtime.CreateSubscription(c.Request.Context(), mihomo.SubscriptionInput{
		Name: request.Name, URL: request.URL, IntervalSeconds: request.IntervalSeconds, FetchVia: request.FetchVia,
	})
	if err != nil {
		h.respondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, model.NewSuccessResponse(value))
}

func (h *MihomoHandler) updateSubscription(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	var request struct {
		Name            *string `json:"name"`
		URL             *string `json:"url"`
		IntervalSeconds *int    `json:"interval_seconds"`
		FetchVia        *string `json:"fetch_via"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "订阅参数无效"))
		return
	}
	value, err := h.runtime.UpdateSubscription(c.Request.Context(), c.Param("id"), mihomo.SubscriptionPatch{
		Name: request.Name, URL: request.URL, IntervalSeconds: request.IntervalSeconds, FetchVia: request.FetchVia,
	})
	if err != nil {
		h.respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(value))
}

func (h *MihomoHandler) deleteSubscription(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	if err := h.runtime.DeleteSubscription(c.Request.Context(), c.Param("id")); err != nil {
		h.respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *MihomoHandler) updateSubscriptionNow(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	value, err := h.runtime.UpdateSubscriptionNow(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(value))
}

func (h *MihomoHandler) testLatency(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	var request struct {
		Group string `json:"group"`
	}
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "测速参数无效"))
			return
		}
	}
	value, err := h.runtime.TestLatency(c.Request.Context(), request.Group)
	if err != nil {
		h.respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(value))
}

func (h *MihomoHandler) overview(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	value, err := h.runtime.Overview(c.Request.Context(), queryBool(c, "refresh_exit", false))
	if err != nil {
		h.respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(value))
}

func (h *MihomoHandler) selectNode(c *gin.Context) {
	if !h.ready(c) {
		return
	}
	var request struct {
		Group string `json:"group"`
		Name  string `json:"name"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.Group) == "" || strings.TrimSpace(request.Name) == "" {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "代理组和节点名称不能为空"))
		return
	}
	value, err := h.runtime.Select(c.Request.Context(), request.Group, request.Name)
	if err != nil {
		h.respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(value))
}

func (h *MihomoHandler) ready(c *gin.Context) bool {
	if h == nil || h.runtime == nil {
		c.JSON(http.StatusServiceUnavailable, model.NewErrorResponse(http.StatusServiceUnavailable, "Mihomo 控制面板尚未配置"))
		return false
	}
	return true
}

func (h *MihomoHandler) respondError(c *gin.Context, err error) {
	_ = c.Error(err)
	switch {
	case errors.Is(err, mihomo.ErrNotConfigured):
		c.JSON(http.StatusServiceUnavailable, model.NewErrorResponse(http.StatusServiceUnavailable, "Mihomo 控制面板尚未配置"))
	case errors.Is(err, mihomo.ErrGroupNotManaged):
		c.JSON(http.StatusForbidden, model.NewErrorResponse(http.StatusForbidden, "该代理组不在管理范围内"))
	case errors.Is(err, mihomo.ErrInvalidSelection):
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "所选节点不属于当前代理组"))
	case errors.Is(err, mihomo.ErrLatencyTestActive):
		c.JSON(http.StatusConflict, model.NewErrorResponse(http.StatusConflict, "线路测速正在进行中"))
	case errors.Is(err, mihomo.ErrSubscriptionsNotConfigured):
		c.JSON(http.StatusServiceUnavailable, model.NewErrorResponse(http.StatusServiceUnavailable, "Mihomo 订阅管理尚未配置"))
	case errors.Is(err, mihomo.ErrSubscriptionNotFound):
		c.JSON(http.StatusNotFound, model.NewErrorResponse(http.StatusNotFound, "订阅不存在"))
	case errors.Is(err, mihomo.ErrInvalidSubscription):
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "订阅名称、地址或更新周期无效"))
	case errors.Is(err, mihomo.ErrDuplicateSubscription):
		c.JSON(http.StatusConflict, model.NewErrorResponse(http.StatusConflict, "该订阅地址已经存在，无需重复添加"))
	default:
		c.JSON(http.StatusBadGateway, model.NewErrorResponse(http.StatusBadGateway, "Mihomo 控制器连接异常，请稍后重试"))
	}
}
