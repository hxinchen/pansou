package api

import (
	"context"
	"io/fs"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"pansou/collection"
	"pansou/config"
	"pansou/credential"
	"pansou/keywordsync"
	"pansou/plugin"
	"pansou/service"
	"pansou/sourceconfig"
	"pansou/storage"
	"pansou/util"
	adminweb "pansou/web"
)

type RouterDependencies struct {
	Store              *storage.Store
	Runner             *collection.Runner
	SourceRuntime      *sourceconfig.Service
	Credentials        *credential.Service
	CredentialAdapters map[string]any
	KeywordSources     *keywordsync.Service
}

// SetupRouter 设置路由
func SetupRouter(searchService service.SearchProvider, options ...RouterDependencies) *gin.Engine {
	var dependencies RouterDependencies
	if len(options) > 0 {
		dependencies = options[0]
	}

	// 设置搜索服务
	SetSearchService(searchService)

	// 设置为生产模式
	gin.SetMode(gin.ReleaseMode)

	// 创建默认路由
	r := gin.Default()

	// 添加中间件
	r.Use(CORSMiddleware())
	r.Use(RequestIDMiddleware())
	r.Use(LoggerMiddleware())
	r.Use(util.GzipMiddleware()) // 添加压缩中间件
	r.Use(AuthMiddleware())      // 添加认证中间件

	// 定义API路由组
	api := r.Group("/api")
	{
		// 认证接口（不需要认证，由中间件公开路径处理）
		auth := api.Group("/auth")
		{
			auth.POST("/login", LoginHandler)
			auth.POST("/verify", VerifyHandler)
			auth.POST("/logout", LogoutHandler)
			auth.POST("/change-password", ChangePasswordHandler)
		}
		userAPI := api.Group("/user")
		NewUserHandler(dependencies.Store).Register(userAPI)
		var sourceManager *sourceconfig.Manager
		if dependencies.SourceRuntime != nil {
			sourceManager = dependencies.SourceRuntime.Manager
		}
		NewCredentialHandler(dependencies.Credentials, dependencies.CredentialAdapters, sourceManager).RegisterUser(userAPI)

		// 搜索接口 - 网页 JWT 与 API Key 共用同一用户限额和调用记录。
		searchAPI := api.Group("")
		searchAPI.Use(SearchAccessMiddleware())
		searchAPI.POST("/search", SearchHandler)
		searchAPI.GET("/search", SearchHandler)
		api.POST("/check/links", CheckHandler)

		// 健康检查接口
		api.GET("/health", func(c *gin.Context) {
			// 根据配置决定是否返回插件信息
			pluginCount := 0
			pluginNames := []string{}
			pluginsEnabled := config.AppConfig.AsyncPluginEnabled

			if pluginsEnabled && searchService != nil && searchService.GetPluginManager() != nil {
				plugins := searchService.GetPluginManager().GetPlugins()
				pluginCount = len(plugins)
				for _, p := range plugins {
					pluginNames = append(pluginNames, p.Name())
				}
			}

			// 获取频道信息。配置了动态来源运行时时，健康检查应展示
			// 当前热更新快照，而不是进程启动时的 CHANNELS 环境变量。
			channels := append([]string(nil), config.AppConfig.DefaultChannels...)
			if dependencies.SourceRuntime != nil && dependencies.SourceRuntime.Manager != nil {
				if lease, err := dependencies.SourceRuntime.Manager.Acquire(); err == nil {
					if snapshot := lease.Snapshot(); snapshot != nil {
						channels = snapshot.Channels()
					}
					lease.Release()
				}
			}
			channelsCount := len(channels)

			response := gin.H{
				"status":             "ok",
				"auth_enabled":       config.AppConfig.AuthEnabled, // 添加认证状态
				"multi_user_enabled": databaseAuthService != nil,
				"plugins_enabled":    pluginsEnabled,
				"channels":           channels,
				"channels_count":     channelsCount,
			}
			if dependencies.Store == nil {
				response["database_configured"] = false
				response["database_status"] = "disabled"
			} else {
				response["database_configured"] = true
				ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
				err := dependencies.Store.Ping(ctx)
				cancel()
				if err != nil {
					response["status"] = "degraded"
					response["database_status"] = "unavailable"
				} else {
					response["database_status"] = "healthy"
				}
			}

			// 只有当插件启用时才返回插件相关信息
			if pluginsEnabled {
				response["plugin_count"] = pluginCount
				response["plugins"] = pluginNames
			}

			c.JSON(200, response)
		})
	}

	adminAPI := api.Group("/admin")
	adminAPI.Use(RequireAdminAuth())
	adminHandler := NewAdminHandler(dependencies.Store, dependencies.Runner, dependencies.SourceRuntime)
	adminHandler.credentials = dependencies.Credentials
	adminHandler.keywordSources = dependencies.KeywordSources
	adminHandler.Register(adminAPI)
	var adminSourceManager *sourceconfig.Manager
	if dependencies.SourceRuntime != nil {
		adminSourceManager = dependencies.SourceRuntime.Manager
	}
	NewCredentialHandler(dependencies.Credentials, dependencies.CredentialAdapters, adminSourceManager).RegisterAdmin(adminAPI)

	// The admin shell is public so administrators can reach the login form.
	// Its API group is registered separately with RequireAdminAuth.
	adminIndex := func(c *gin.Context) {
		index, err := adminweb.Assets.ReadFile("index.html")
		if err != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", index)
	}
	r.GET("/admin", adminIndex)
	r.GET("/admin/", adminIndex)
	if assets, err := fs.Sub(adminweb.Assets, "assets"); err == nil {
		r.StaticFS("/admin/assets", http.FS(assets))
	}

	// 注册插件的Web路由（如果插件实现了PluginWithWebHandler接口）
	// 只有当插件功能启用且插件在启用列表中时才注册路由
	if dependencies.Store == nil && config.AppConfig.AsyncPluginEnabled && searchService != nil && searchService.GetPluginManager() != nil {
		enabledPlugins := searchService.GetPluginManager().GetPlugins()
		for _, p := range enabledPlugins {
			if webPlugin, ok := p.(plugin.PluginWithWebHandler); ok {
				webPlugin.RegisterWebRoutes(r.Group(""))
			}
		}
	}

	return r
}
