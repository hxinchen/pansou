package api

import (
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAdminHandlerRegistersPaginatedDetailRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewAdminHandler(nil, nil).Register(router.Group("/api/admin"))
	routes := make(map[string]bool)
	for _, route := range router.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	for _, route := range []string{
		"GET /api/admin/runs/:id/items",
		"GET /api/admin/runs/:id/items/:itemId/sources",
		"GET /api/admin/resources/:id/sources",
		"GET /api/admin/resources/:id/keywords",
	} {
		if !routes[route] {
			t.Fatalf("route %q is not registered", route)
		}
	}
}
