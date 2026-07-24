package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/netutil"

	"pansou/api"
	accountauth "pansou/auth"
	"pansou/collection"
	"pansou/config"
	"pansou/credential"
	"pansou/integration"
	"pansou/keywordsync"
	"pansou/mihomo"
	"pansou/model"
	"pansou/plugin"
	"pansou/proxypool"
	"pansou/service"
	"pansou/sourceconfig"
	"pansou/storage"
	"pansou/usage"
	"pansou/util"
	"pansou/util/cache"

	// 以下是插件的空导入，用于触发各插件的init函数，实现自动注册
	// 添加新插件时，只需在此处添加对应的导入语句即可
	_ "pansou/plugin/ahhhhfs"
	_ "pansou/plugin/aikanzy"
	_ "pansou/plugin/aisoupan"
	_ "pansou/plugin/alupan"
	_ "pansou/plugin/ash"
	_ "pansou/plugin/bixin"
	_ "pansou/plugin/cldi"
	_ "pansou/plugin/clmao"
	_ "pansou/plugin/clxiong"
	_ "pansou/plugin/cyg"
	_ "pansou/plugin/daishudj"
	_ "pansou/plugin/ddys"
	_ "pansou/plugin/discourse"
	_ "pansou/plugin/djgou"
	_ "pansou/plugin/duoduo"
	_ "pansou/plugin/dyyj"
	_ "pansou/plugin/dyyjpro"
	_ "pansou/plugin/erxiao"
	_ "pansou/plugin/feikuai"
	_ "pansou/plugin/fox4k"
	_ "pansou/plugin/gaoqing888"
	_ "pansou/plugin/gying"
	_ "pansou/plugin/haisou"
	_ "pansou/plugin/hdmoli"
	_ "pansou/plugin/hdr4k"
	_ "pansou/plugin/huban"
	_ "pansou/plugin/hunhepan"
	_ "pansou/plugin/javdb"
	_ "pansou/plugin/jikepan"
	_ "pansou/plugin/jsnoteclub"
	_ "pansou/plugin/jutoushe"
	_ "pansou/plugin/kkmao"
	_ "pansou/plugin/kkv"
	_ "pansou/plugin/labi"
	_ "pansou/plugin/leijing"
	_ "pansou/plugin/libvio"
	_ "pansou/plugin/lou1"
	_ "pansou/plugin/meitizy"
	_ "pansou/plugin/melost"
	_ "pansou/plugin/miaoso"
	_ "pansou/plugin/mikuclub"
	_ "pansou/plugin/mizixing"
	_ "pansou/plugin/muou"
	_ "pansou/plugin/nsgame"
	_ "pansou/plugin/nyaa"
	_ "pansou/plugin/ouge"
	_ "pansou/plugin/pan666"
	_ "pansou/plugin/panlian"
	_ "pansou/plugin/pansearch"
	_ "pansou/plugin/panta"
	_ "pansou/plugin/panwiki"
	_ "pansou/plugin/panyq"
	_ "pansou/plugin/pianku"
	_ "pansou/plugin/qingying"
	_ "pansou/plugin/qiwei"
	_ "pansou/plugin/qqpd"
	_ "pansou/plugin/quark4k"
	_ "pansou/plugin/quarksoo"
	_ "pansou/plugin/qupanshe"
	_ "pansou/plugin/qupansou"
	_ "pansou/plugin/sdso"
	_ "pansou/plugin/shandian"
	_ "pansou/plugin/sousou"
	_ "pansou/plugin/susu"
	_ "pansou/plugin/thepiratebay"
	_ "pansou/plugin/u3c3"
	_ "pansou/plugin/wanou"
	_ "pansou/plugin/weibo"
	_ "pansou/plugin/wuji"
	_ "pansou/plugin/xb6v"
	_ "pansou/plugin/xdpan"
	_ "pansou/plugin/xdyh"
	_ "pansou/plugin/xiaoji"
	_ "pansou/plugin/xiaozhang"
	_ "pansou/plugin/xinjuc"
	_ "pansou/plugin/xuexizhinan"
	_ "pansou/plugin/xys"
	_ "pansou/plugin/yiove"
	_ "pansou/plugin/ypfxw"
	_ "pansou/plugin/yuhuage"
	_ "pansou/plugin/yulinshufa"
	_ "pansou/plugin/yunsou"
	_ "pansou/plugin/zhizhen"
	_ "pansou/plugin/zxzj"

	_ "pansou/plugin/duanjuw"
	_ "pansou/plugin/jupansou"
	_ "pansou/plugin/lingjisp"
	_ "pansou/plugin/panzun"
	_ "pansou/plugin/quarktv"
	_ "pansou/plugin/yunso"
)

// 全局缓存写入管理器
var globalCacheWriteManager *cache.DelayedBatchWriteManager

func main() {
	// 初始化应用
	initApp()

	// 启动服务器
	startServer()
}

// initApp 初始化应用程序
func initApp() {
	// 初始化配置
	config.Init()

	// 初始化HTTP客户端
	util.InitHTTPClient()

	// 初始化缓存写入管理器
	var err error
	globalCacheWriteManager, err = cache.NewDelayedBatchWriteManager()
	if err != nil {
		log.Fatalf("缓存写入管理器创建失败: %v", err)
	}
	if err := globalCacheWriteManager.Initialize(); err != nil {
		log.Fatalf("缓存写入管理器初始化失败: %v", err)
	}
	// 将缓存写入管理器注入到service包
	service.SetGlobalCacheWriteManager(globalCacheWriteManager)

	// 延迟设置主缓存更新函数，确保service初始化完成
	go func() {
		// 等待一小段时间确保service包完全初始化
		time.Sleep(100 * time.Millisecond)
		if mainCache := service.GetEnhancedTwoLevelCache(); mainCache != nil {
			globalCacheWriteManager.SetMainCacheUpdater(func(key string, data []byte, ttl time.Duration) error {
				return mainCache.SetBothLevels(key, data, ttl)
			})
		}
	}()

	// 确保异步插件系统初始化
	plugin.InitAsyncPluginSystem()
}

// startServer 启动Web服务器
func startServer() {
	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	// DATABASE_URL 为空时 storage.Open 返回 nil，完整保留原有运行模式。
	openCtx, openCancel := context.WithTimeout(appCtx, 30*time.Second)
	store, err := storage.Open(openCtx, config.AppConfig.DatabaseURL, storage.WithDefaultCooldown(config.AppConfig.DefaultCooldown))
	openCancel()
	if err != nil {
		log.Fatalf("PostgreSQL 初始化或 migration 失败: %v", err)
	}
	var sourceRuntime *sourceconfig.Service
	var pluginManager *plugin.PluginManager
	var liveSearchService *service.SearchService
	var credentialService *credential.Service
	var proxyPool *proxypool.Service
	mihomoController, err := mihomo.NewService(mihomo.Config{
		ControllerURL: config.AppConfig.MihomoControllerURL,
		Secret:        config.AppConfig.MihomoControllerSecret,
		ManagedGroups: config.AppConfig.MihomoManagedGroups,
		ProxyURL:      config.AppConfig.ProxyURL,
		ConfigPath:    config.AppConfig.MihomoConfigPath,
		ReloadPath:    config.AppConfig.MihomoReloadPath,
		ExitInfoURL:   config.AppConfig.MihomoExitInfoURL,
		Timeout:       config.AppConfig.MihomoControllerTimeout,
		DelayTestURL:  config.AppConfig.MihomoDelayTestURL,
		DelayTimeout:  config.AppConfig.MihomoDelayTestTimeout,
	})
	if err != nil {
		log.Fatalf("初始化 Mihomo 控制器失败: %v", err)
	}
	if store != nil {
		sourceRuntime, err = bootstrapSourceRuntime(appCtx, store)
		if err != nil {
			store.Close()
			log.Fatalf("初始化搜索来源运行时失败: %v", err)
		}
		credentialService, err = bootstrapCredentialService(appCtx, store, sourceRuntime)
		if err != nil {
			store.Close()
			log.Fatalf("初始化插件凭据服务失败: %v", err)
		}
		proxyPool, err = bootstrapProxyPool(appCtx, store)
		if err != nil {
			store.Close()
			log.Fatalf("初始化代理池失败: %v", err)
		}
		liveSearchService = service.NewDynamicSearchService(sourceRuntime.Manager)
		liveSearchService.SetCredentialService(credentialService)
		pluginManager = liveSearchService.GetPluginManager()
	} else {
		pluginManager = plugin.NewPluginManager()
		if config.AppConfig.AsyncPluginEnabled {
			pluginManager.RegisterGlobalPluginsWithFilter(config.AppConfig.EnabledPlugins)
		}
		liveSearchService = service.NewSearchService(pluginManager)
	}
	pluginCount := 0
	if pluginManager != nil {
		pluginCount = len(pluginManager.GetPlugins())
	}
	config.UpdateDefaultConcurrency(pluginCount)
	var searchService service.SearchProvider = liveSearchService
	var runner *collection.Runner
	var linkChecks *collection.LinkCheckQueue
	var collectionRepository *integration.CollectionRepository
	var usageRecorder *usage.Recorder
	var keywordSourceSync *keywordsync.Service
	api.SetAuthService(nil)
	api.SetUsageServices(nil, nil)
	if store != nil {
		service.NewSchedulerMetricsRecorder(store, config.AppConfig.SearchMetricsInterval).Start(appCtx)
		credentialHealth := credential.NewHealthMonitor(store, credentialService, credentialHealthAdapterMap(pluginManager), credential.HealthMonitorConfig{
			Enabled:      config.AppConfig.GyingHealthCheckEnabled,
			Interval:     config.AppConfig.GyingHealthCheckInterval,
			ScanInterval: config.AppConfig.GyingHealthCheckScanInterval,
			InitialDelay: config.AppConfig.GyingHealthCheckInitialDelay,
			Timeout:      config.AppConfig.GyingHealthCheckTimeout,
			Jitter:       config.AppConfig.GyingHealthCheckJitter,
			BatchSize:    config.AppConfig.GyingHealthCheckBatchSize,
			OnError:      func(err error) { log.Printf("插件账号测活: %v", err) },
		})
		if err := credentialHealth.Start(appCtx); err != nil {
			store.Close()
			log.Fatalf("启动插件账号测活失败: %v", err)
		}
		if config.AppConfig.AuthEnabled {
			if strings.TrimSpace(os.Getenv("AUTH_JWT_SECRET")) == "" {
				store.Close()
				log.Fatal("数据库多用户模式必须设置持久化的 AUTH_JWT_SECRET")
			}
			if err := bootstrapAdmin(appCtx, store); err != nil {
				store.Close()
				log.Fatalf("初始化管理员账号失败: %v", err)
			}
			authService := accountauth.NewService(
				integration.IdentityRepository{Store: store},
				config.AppConfig.AuthJWTSecret,
				config.AppConfig.AuthTokenExpiry,
			)
			limiter := usage.NewLimiter(usage.LimitConfig{
				RequestsPerSecond: config.AppConfig.DefaultUserRPS,
				RequestsPerMinute: config.AppConfig.DefaultUserRPM,
			})
			recorderConfig := usage.DefaultRecorderConfig()
			recorderConfig.Cleanup = func(ctx context.Context) error {
				for iteration := 0; iteration < 100; iteration++ {
					deleted, err := store.CleanupAPIRequestLogs(ctx, time.Now(), config.AppConfig.UsageRetention, 1000)
					if err != nil || deleted < 1000 {
						return err
					}
				}
				return nil
			}
			recorderConfig.OnError = func(err error) { log.Printf("API 调用日志: %v", err) }
			usageRecorder = usage.NewRecorder(integration.UsageRepository{Store: store}, recorderConfig)
			if err := usageRecorder.Start(appCtx); err != nil {
				store.Close()
				log.Fatalf("启动 API 调用日志 Worker 失败: %v", err)
			}
			api.SetAuthService(authService)
			api.SetUsageServices(limiter, usageRecorder)
		}

		collectionRepository = &integration.CollectionRepository{Store: store}
		linkChecks = collection.NewLinkCheckQueue(
			collectionRepository,
			integration.LinkChecker{Service: api.SharedCheckService(), Pool: proxyPool},
			collection.LinkCheckQueueConfig{
				Workers:            config.AppConfig.LinkCheckWorkers,
				Buffer:             1024,
				Timeout:            config.AppConfig.LinkCheckTimeout,
				PollInterval:       time.Minute,
				BacklogInterval:    config.AppConfig.LinkCheckBacklogInterval,
				BatchSize:          500,
				PlatformLimit:      config.AppConfig.LinkCheckPerPlatform,
				CircuitFailures:    config.AppConfig.LinkCheckCircuitFailures,
				CircuitCooldown:    config.AppConfig.LinkCheckCircuitCooldown,
				WriteBatchSize:     config.AppConfig.LinkCheckWriteBatchSize,
				WriteFlushInterval: config.AppConfig.LinkCheckWriteFlushInterval,
				OnError:            func(err error) { log.Printf("链接检测任务: %v", err) },
			},
		)
		var sourceProvider collection.SourceProvider = integration.ConfiguredSources(config.AppConfig.DefaultChannels, pluginManager)
		if sourceRuntime != nil {
			sourceProvider = integration.SnapshotSources{Manager: sourceRuntime.Manager, Credentials: credentialService}
		}
		runner = collection.NewRunner(
			collectionRepository,
			integration.NewLiveSearcher(liveSearchService),
			sourceProvider,
			linkChecks,
			collection.Config{
				ScheduleInterval: config.AppConfig.CollectionInterval,
				DefaultCooldown:  config.AppConfig.DefaultCooldown,
				MaxSourceRetries: 2,
				OnError:          func(err error) { log.Printf("采集任务: %v", err) },
			},
		)
		if err := runner.Start(appCtx); err != nil {
			store.Close()
			log.Fatalf("启动采集调度器失败: %v", err)
		}
		hybrid := service.NewHybridSearchService(liveSearchService, store, config.AppConfig.HybridRefreshAfter)
		hybrid.SetExternalResultRecorder(func(ctx context.Context, keyword string, response model.SearchResponse) error {
			_, err := runner.RecordExternal(ctx, keyword, response)
			return err
		})
		searchService = hybrid

		keywordSourceSync = keywordsync.New(store, keywordsync.Config{
			PollInterval: time.Minute,
			OnError:      func(err error) { log.Printf("API 关键词同步: %v", err) },
		})
		if err := keywordSourceSync.Start(appCtx); err != nil {
			store.Close()
			log.Fatalf("启动 API 关键词同步器失败: %v", err)
		}
	}

	// 设置路由
	router := api.SetupRouter(searchService, api.RouterDependencies{
		Store: store, Runner: runner, LinkChecks: linkChecks, SourceRuntime: sourceRuntime,
		Credentials: credentialService, CredentialAdapters: credentialAdapterMap(pluginManager),
		CredentialDiagnostics: liveSearchService, KeywordSources: keywordSourceSync, ProxyPool: proxyPool, Mihomo: mihomoController,
		SearchCache: service.GetEnhancedTwoLevelCache(),
	})

	// 获取端口配置
	port := config.AppConfig.Port

	// 输出服务信息
	printServiceInfo(port, pluginManager)

	// 创建HTTP服务器
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      router,
		ReadTimeout:  config.AppConfig.HTTPReadTimeout,
		WriteTimeout: config.AppConfig.HTTPWriteTimeout,
		IdleTimeout:  config.AppConfig.HTTPIdleTimeout,
	}

	// 创建通道来接收操作系统信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// 在单独的goroutine中启动服务器
	go func() {
		// 如果设置了最大连接数，使用限制监听器
		if config.AppConfig.HTTPMaxConns > 0 {
			// 创建监听器
			listener, err := net.Listen("tcp", srv.Addr)
			if err != nil {
				log.Fatalf("创建监听器失败: %v", err)
			}

			// 创建限制连接数的监听器
			limitListener := netutil.LimitListener(listener, config.AppConfig.HTTPMaxConns)

			// 使用限制监听器启动服务器
			if err := srv.Serve(limitListener); err != nil && err != http.ErrServerClosed {
				log.Fatalf("启动服务器失败: %v", err)
			}
		} else {
			// 使用默认方式启动服务器（不限制连接数）
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("启动服务器失败: %v", err)
			}
		}
	}()

	// 等待中断信号
	<-quit
	fmt.Println("正在关闭服务器...")
	appCancel()

	// 优先保存缓存数据到磁盘（数据安全第一）
	// 增加关闭超时时间，确保数据有足够时间保存
	shutdownTimeout := 10 * time.Second
	if runner != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), shutdownTimeout)
		if err := runner.Stop(stopCtx); err != nil {
			log.Printf("采集调度器关闭失败: %v", err)
		}
		stopCancel()
	}
	if usageRecorder != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), shutdownTimeout)
		if err := usageRecorder.Close(stopCtx); err != nil {
			log.Printf("API 调用日志 Worker 关闭失败: %v", err)
		}
		stopCancel()
	}
	if keywordSourceSync != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), shutdownTimeout)
		if err := keywordSourceSync.Close(stopCtx); err != nil {
			log.Printf("API 关键词同步器关闭失败: %v", err)
		}
		stopCancel()
	}

	if globalCacheWriteManager != nil {
		if err := globalCacheWriteManager.Shutdown(shutdownTimeout); err != nil {
			log.Printf("缓存数据保存失败: %v", err)
		}
	}

	// 额外确保内存缓存也被保存（双重保障）
	if mainCache := service.GetEnhancedTwoLevelCache(); mainCache != nil {
		if err := mainCache.FlushMemoryToDisk(); err != nil {
			log.Printf("内存缓存同步失败: %v", err)
		}
	}

	// 设置关闭超时时间
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 优雅关闭服务器
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("服务器关闭异常: %v", err)
	}
	if store != nil {
		if sourceRuntime != nil && sourceRuntime.Manager != nil {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), shutdownTimeout)
			if err := sourceRuntime.Manager.Close(closeCtx); err != nil {
				log.Printf("搜索来源运行时关闭失败: %v", err)
			}
			closeCancel()
		}
		store.Close()
	}
	if err := api.SharedCheckService().Close(); err != nil {
		log.Printf("链接检测缓存关闭失败: %v", err)
	}

	fmt.Println("服务器已安全关闭")
}

func bootstrapAdmin(ctx context.Context, store *storage.Store) error {
	count, err := store.CountUsers(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	username := strings.TrimSpace(config.AppConfig.AdminUsername)
	password := config.AppConfig.AdminPassword
	if username == "" || password == "" {
		legacyUsers := make([]string, 0, len(config.AppConfig.AuthUsers))
		for candidate := range config.AppConfig.AuthUsers {
			legacyUsers = append(legacyUsers, candidate)
		}
		sort.Strings(legacyUsers)
		if len(legacyUsers) > 0 {
			username = legacyUsers[0]
			password = config.AppConfig.AuthUsers[username]
			log.Printf("用户表为空，使用 AUTH_USERS 中的首个账号 %q 引导管理员；迁移后请改用 ADMIN_USERNAME/ADMIN_PASSWORD", username)
		}
	}
	if username == "" || password == "" {
		return errors.New("用户表为空，请设置 ADMIN_USERNAME 和 ADMIN_PASSWORD")
	}
	passwordHash, err := accountauth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("管理员初始密码不符合安全要求: %w", err)
	}
	enabled := true
	mustChange := false
	_, err = store.CreateUser(ctx, storage.CreateUserInput{
		Username:           username,
		PasswordHash:       passwordHash,
		Role:               storage.UserRoleAdmin,
		Enabled:            &enabled,
		MustChangePassword: &mustChange,
		RPSLimit:           config.AppConfig.DefaultUserRPS,
		RPMLimit:           config.AppConfig.DefaultUserRPM,
	})
	return err
}

// printServiceInfo 打印服务信息
func printServiceInfo(port string, pluginManager *plugin.PluginManager) {
	// 启动服务器
	fmt.Printf("服务器启动在 http://localhost:%s\n", port)

	// 输出代理信息
	hasProxy := false
	if config.AppConfig.ProxyURL != "" {
		proxyType := "代理"
		if strings.HasPrefix(config.AppConfig.ProxyURL, "socks5://") {
			proxyType = "SOCKS5代理"
		} else if strings.HasPrefix(config.AppConfig.ProxyURL, "http://") {
			proxyType = "HTTP代理"
		} else if strings.HasPrefix(config.AppConfig.ProxyURL, "https://") {
			proxyType = "HTTPS代理"
		}
		fmt.Printf("使用%s (PROXY): %s\n", proxyType, config.AppConfig.ProxyURL)
		hasProxy = true
	}
	if config.AppConfig.HTTPProxyURL != "" {
		fmt.Printf("使用HTTP代理 (HTTP_PROXY/http_proxy): %s\n", config.AppConfig.HTTPProxyURL)
		hasProxy = true
	}
	if config.AppConfig.HTTPSProxyURL != "" {
		fmt.Printf("使用HTTPS代理 (HTTPS_PROXY/https_proxy): %s\n", config.AppConfig.HTTPSProxyURL)
		hasProxy = true
	}
	if !hasProxy {
		fmt.Println("未使用代理")
	}

	// 输出并发信息
	if os.Getenv("CONCURRENCY") != "" {
		fmt.Printf("默认并发数: %d (由环境变量CONCURRENCY指定)\n", config.AppConfig.DefaultConcurrency)
	} else {
		channelCount := len(config.AppConfig.DefaultChannels)
		pluginCount := 0
		// 只有插件启用时才计算插件数
		if config.AppConfig.AsyncPluginEnabled && pluginManager != nil {
			pluginCount = len(pluginManager.GetPlugins())
		}
		fmt.Printf("默认并发数: %d (= 频道数%d + 插件数%d + 10)\n",
			config.AppConfig.DefaultConcurrency, channelCount, pluginCount)
	}

	// 输出缓存信息
	if config.AppConfig.CacheEnabled {
		fmt.Printf("缓存已启用: 路径=%s, 最大大小=%dMB, TTL=%d分钟\n",
			config.AppConfig.CachePath,
			config.AppConfig.CacheMaxSizeMB,
			config.AppConfig.CacheTTLMinutes)
	} else {
		fmt.Println("缓存已禁用")
	}

	// 输出压缩信息
	if config.AppConfig.EnableCompression {
		fmt.Printf("响应压缩已启用: 最小压缩大小=%d字节\n",
			config.AppConfig.MinSizeToCompress)
	}

	// 输出GC配置信息
	fmt.Printf("GC配置: 触发阈值=%d%%, 内存优化=%v\n",
		config.AppConfig.GCPercent,
		config.AppConfig.OptimizeMemory)

	// 输出HTTP服务器配置信息
	readTimeoutMsg := ""
	if os.Getenv("HTTP_READ_TIMEOUT") != "" {
		readTimeoutMsg = "(由环境变量指定)"
	} else {
		readTimeoutMsg = "(自动计算)"
	}

	writeTimeoutMsg := ""
	if os.Getenv("HTTP_WRITE_TIMEOUT") != "" {
		writeTimeoutMsg = "(由环境变量指定)"
	} else {
		writeTimeoutMsg = "(自动计算)"
	}

	maxConnsMsg := ""
	if os.Getenv("HTTP_MAX_CONNS") != "" {
		maxConnsMsg = "(由环境变量指定)"
	} else {
		cpuCount := runtime.NumCPU()
		maxConnsMsg = fmt.Sprintf("(自动计算: CPU核心数%d × 200)", cpuCount)
	}

	fmt.Printf("HTTP服务器配置: 读取超时=%v %s, 写入超时=%v %s, 空闲超时=%v, 最大连接数=%d %s\n",
		config.AppConfig.HTTPReadTimeout, readTimeoutMsg,
		config.AppConfig.HTTPWriteTimeout, writeTimeoutMsg,
		config.AppConfig.HTTPIdleTimeout,
		config.AppConfig.HTTPMaxConns, maxConnsMsg)

	// 输出异步插件配置信息
	if config.AppConfig.AsyncPluginEnabled {
		// 检查工作者数量是否由环境变量指定
		workersMsg := ""
		if os.Getenv("ASYNC_MAX_BACKGROUND_WORKERS") != "" {
			workersMsg = "(由环境变量指定)"
		} else {
			cpuCount := runtime.NumCPU()
			workersMsg = fmt.Sprintf("(自动计算: CPU核心数%d × 5)", cpuCount)
		}

		// 检查任务数量是否由环境变量指定
		tasksMsg := ""
		if os.Getenv("ASYNC_MAX_BACKGROUND_TASKS") != "" {
			tasksMsg = "(由环境变量指定)"
		} else {
			tasksMsg = "(自动计算: 工作者数量 × 5)"
		}

		fmt.Printf("异步插件已启用: 响应超时=%d秒, 最大工作者=%d %s, 最大任务=%d %s, 缓存TTL=%d小时\n",
			config.AppConfig.AsyncResponseTimeout,
			config.AppConfig.AsyncMaxBackgroundWorkers, workersMsg,
			config.AppConfig.AsyncMaxBackgroundTasks, tasksMsg,
			config.AppConfig.AsyncCacheTTLHours)
	} else {
		fmt.Println("异步插件已禁用")
	}

	// 只有当插件功能启用时才输出插件信息
	if config.AppConfig.AsyncPluginEnabled {
		plugins := pluginManager.GetPlugins()
		if len(plugins) > 0 {
			// 根据新逻辑，只有指定了具体插件才会加载插件
			fmt.Printf("已启用指定插件 (%d个):\n", len(plugins))

			// 按优先级排序（优先级数字越小越靠前）
			sort.Slice(plugins, func(i, j int) bool {
				// 优先级相同时按名称排序
				if plugins[i].Priority() == plugins[j].Priority() {
					return plugins[i].Name() < plugins[j].Name()
				}
				return plugins[i].Priority() < plugins[j].Priority()
			})

			for _, p := range plugins {
				fmt.Printf("  - %s (优先级: %d)\n", p.Name(), p.Priority())
			}
		} else {
			// 区分不同的情况
			if config.AppConfig.EnabledPlugins == nil {
				fmt.Println("未设置插件列表 (ENABLED_PLUGINS)，未加载任何插件")
			} else if len(config.AppConfig.EnabledPlugins) > 0 {
				fmt.Printf("未找到指定的插件: %s\n", strings.Join(config.AppConfig.EnabledPlugins, ", "))
			} else {
				fmt.Println("插件列表为空 (ENABLED_PLUGINS=\"\")，未加载任何插件")
			}
		}
	}
}
