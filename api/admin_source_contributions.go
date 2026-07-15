package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"pansou/model"
	"pansou/storage"
)

func (h *AdminHandler) listSourceContributions(c *gin.Context) {
	if !h.available(c) {
		return
	}
	page, err := h.store.ListSourceContributions(c.Request.Context(), storage.SourceContributionFilter{
		SourceType: strings.TrimSpace(c.Query("source_type")),
		Page:       queryInt(c, "page", 1),
		PageSize:   queryInt(c, "page_size", 50),
		SortBy:     strings.TrimSpace(c.Query("sort_by")),
		SortDir:    strings.TrimSpace(c.Query("sort_dir")),
	})
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(page))
}

func (h *AdminHandler) getSourceContribution(c *gin.Context) {
	if !h.available(c) {
		return
	}
	detail, err := h.store.GetSourceContribution(
		c.Request.Context(),
		strings.TrimSpace(c.Param("source_type")),
		strings.TrimSpace(c.Param("source_key")),
		storage.SourceContributionDetailFilter{
			Page:     queryInt(c, "page", 1),
			PageSize: queryInt(c, "page_size", 20),
			SortBy:   strings.TrimSpace(c.Query("sort_by")),
			SortDir:  strings.TrimSpace(c.Query("sort_dir")),
		},
	)
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(detail))
}
