package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"pansou/model"
	"pansou/storage"
)

type updateLinkCheckPolicyRequest struct {
	Enabled         bool     `json:"enabled"`
	Statuses        []string `json:"statuses"`
	IntervalSeconds int64    `json:"interval_seconds"`
}

func (h *AdminHandler) getLinkCheckPolicy(c *gin.Context) {
	if !h.available(c) {
		return
	}
	policy, err := h.store.GetLinkCheckPolicy(c.Request.Context())
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(policy))
}

func (h *AdminHandler) updateLinkCheckPolicy(c *gin.Context) {
	if !h.available(c) {
		return
	}
	var request updateLinkCheckPolicyRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(http.StatusBadRequest, "检测策略参数无效: "+err.Error()))
		return
	}
	policy, err := h.store.UpdateLinkCheckPolicy(c.Request.Context(), storage.UpdateLinkCheckPolicyInput{
		Enabled:         request.Enabled,
		Statuses:        request.Statuses,
		IntervalSeconds: request.IntervalSeconds,
	})
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(policy))
}
