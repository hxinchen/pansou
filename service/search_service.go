package service

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"pansou/config"
	"pansou/credential"
	"pansou/model"
	"pansou/plugin"
	"pansou/sourceconfig"
	"pansou/storage"
	"pansou/tgchannel"
	"pansou/util"
	"pansou/util/cache"
	"pansou/util/pool"
)

// normalizeUrl 标准化URL，将URL编码的中文部分解码为中文，用于去重
func normalizeUrl(rawUrl string) string {
	// 解码URL中的编码字符
	decoded, err := url.QueryUnescape(rawUrl)
	if err != nil {
		// 如果解码失败，返回原始URL
		return rawUrl
	}
	return decoded
}

// 全局缓存写入管理器引用（避免循环依赖）
var globalCacheWriteManager *cache.DelayedBatchWriteManager

// SetGlobalCacheWriteManager 设置全局缓存写入管理器
func SetGlobalCacheWriteManager(manager *cache.DelayedBatchWriteManager) {
	globalCacheWriteManager = manager
}

// GetGlobalCacheWriteManager 获取全局缓存写入管理器
func GetGlobalCacheWriteManager() *cache.DelayedBatchWriteManager {
	return globalCacheWriteManager
}

// GetEnhancedTwoLevelCache 获取增强版两级缓存实例
func GetEnhancedTwoLevelCache() *cache.EnhancedTwoLevelCache {
	return enhancedTwoLevelCache
}

// 优先关键词列表
var priorityKeywords = []string{"合集", "系列", "全", "完", "最新", "附", "complete"}

const maxTGSearchWorkers = 20

// extractKeywordFromCacheKey 从缓存键中提取关键词（简化版）
func extractKeywordFromCacheKey(cacheKey string) string {
	// 这是一个简化的实现，实际中我们会通过传递来获得关键词
	// 为了演示，这里返回简化的显示
	return "搜索关键词"
}

// logAsyncCacheWithKeyword 异步缓存日志输出辅助函数（带关键词）
func logAsyncCacheWithKeyword(keyword, cacheKey string, format string, args ...interface{}) {
	// 检查配置开关
	if config.AppConfig == nil || !config.AppConfig.AsyncLogEnabled {
		return
	}

	// 构建显示的关键词信息
	displayKeyword := keyword
	if displayKeyword == "" {
		displayKeyword = "未知"
	}

	// 将缓存键替换为简化版本+关键词
	shortKey := cacheKey
	if len(cacheKey) > 8 {
		shortKey = cacheKey[:8] + "..."
	}

	// 替换格式字符串中的缓存键
	enhancedFormat := strings.Replace(format, cacheKey, fmt.Sprintf("%s(关键词:%s)", shortKey, displayKeyword), 1)
	fmt.Printf(enhancedFormat, args...)
}

// 全局缓存实例和缓存是否初始化标志
var (
	enhancedTwoLevelCache *cache.EnhancedTwoLevelCache
	cacheInitialized      bool
)

// 初始化缓存
func init() {
	if config.AppConfig != nil && config.AppConfig.CacheEnabled {
		var err error
		// 使用增强版缓存
		enhancedTwoLevelCache, err = cache.NewEnhancedTwoLevelCache()
		if err == nil {
			cacheInitialized = true
		}
	}
}

// mergeSearchResults 智能合并搜索结果，去重并保留最完整的信息
func mergeSearchResults(existing []model.SearchResult, newResults []model.SearchResult) []model.SearchResult {
	// 使用map进行去重和合并，以UniqueID作为唯一标识
	resultMap := make(map[string]model.SearchResult)

	// 先添加现有结果
	for _, result := range existing {
		key := generateResultKey(result)
		resultMap[key] = result
	}

	// 合并新结果，如果UniqueID相同则选择信息更完整的
	for _, newResult := range newResults {
		key := generateResultKey(newResult)
		if existingResult, exists := resultMap[key]; exists {
			// 选择信息更完整的结果
			resultMap[key] = selectBetterResult(existingResult, newResult)
		} else {
			// 新结果，直接添加
			resultMap[key] = newResult
		}
	}

	// 转换回切片
	merged := make([]model.SearchResult, 0, len(resultMap))
	for _, result := range resultMap {
		merged = append(merged, result)
	}

	// 按时间排序（最新的在前）
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Datetime.After(merged[j].Datetime)
	})

	return merged
}

// generateResultKey 生成结果的唯一标识键
func generateResultKey(result model.SearchResult) string {
	// 使用UniqueID作为主要标识，如果没有则使用MessageID，最后使用标题
	if result.UniqueID != "" {
		return result.UniqueID
	}
	if result.MessageID != "" {
		return result.MessageID
	}
	return fmt.Sprintf("title_%s_%s", result.Title, result.Channel)
}

// selectBetterResult 选择信息更完整的结果
func selectBetterResult(existing, new model.SearchResult) model.SearchResult {
	// 计算信息完整度得分
	existingScore := calculateCompletenessScore(existing)
	newScore := calculateCompletenessScore(new)

	if newScore > existingScore {
		return new
	}
	return existing
}

// calculateCompletenessScore 计算结果信息的完整度得分
func calculateCompletenessScore(result model.SearchResult) int {
	score := 0

	// 有UniqueID加分
	if result.UniqueID != "" {
		score += 10
	}

	// 有链接信息加分
	if len(result.Links) > 0 {
		score += 5
		// 每个链接额外加分
		score += len(result.Links)
	}

	// 有内容加分
	if result.Content != "" {
		score += 3
	}

	// 标题长度加分（更详细的标题）
	score += len(result.Title) / 10

	// 有频道信息加分
	if result.Channel != "" {
		score += 2
	}

	// 有标签加分
	score += len(result.Tags)

	return score
}

// SearchService 搜索服务
type SearchService struct {
	pluginManager *plugin.PluginManager
	snapshots     *sourceconfig.Manager
	credentials   credentialProvider
	flights       *singleflight.Group
}

type credentialProvider interface {
	Resolve(context.Context, credential.Identity, string, int) (credential.Layers, error)
	OpenStored(storage.PluginCredential) ([]byte, error)
	Success(context.Context, string) error
	Failure(context.Context, string, string, string, *time.Time) error
}

type sourceSearchBatch struct {
	Results        []model.SearchResult
	Complete       bool
	PartialSources []string
}

func completeSourceSearchBatch() sourceSearchBatch {
	return sourceSearchBatch{Complete: true}
}

// LiveSearchService names the existing Telegram/plugin implementation
// explicitly. It remains an alias so existing integrations keep compiling.
type LiveSearchService = SearchService

// SearchProvider is the stable API-facing search contract. Hybrid search and
// the live-only service both implement it.
type SearchProvider interface {
	Search(keyword string, channels []string, concurrency int, forceRefresh bool, resultType string, sourceType string, plugins []string, cloudTypes []string, ext map[string]interface{}) (model.SearchResponse, error)
	GetPluginManager() *plugin.PluginManager
}

// NewSearchService 创建搜索服务实例并确保缓存可用
func NewSearchService(pluginManager *plugin.PluginManager) *SearchService {
	// 检查缓存是否已初始化，如果未初始化则尝试重新初始化
	if !cacheInitialized && config.AppConfig != nil && config.AppConfig.CacheEnabled {
		var err error
		// 使用增强版缓存
		enhancedTwoLevelCache, err = cache.NewEnhancedTwoLevelCache()
		if err == nil {
			cacheInitialized = true
		}
	}

	// 将主缓存注入到异步插件中
	injectMainCacheToAsyncPlugins(pluginManager, enhancedTwoLevelCache)

	// 确保缓存写入管理器设置了主缓存更新函数
	if globalCacheWriteManager != nil && enhancedTwoLevelCache != nil {
		globalCacheWriteManager.SetMainCacheUpdater(func(key string, data []byte, ttl time.Duration) error {
			return enhancedTwoLevelCache.SetBothLevels(key, data, ttl)
		})
	}

	return &SearchService{
		pluginManager: pluginManager,
		flights:       &singleflight.Group{},
	}
}

func NewDynamicSearchService(snapshots *sourceconfig.Manager) *SearchService {
	return &SearchService{snapshots: snapshots, flights: &singleflight.Group{}}
}

func (s *SearchService) UsesManagedSources() bool {
	return s != nil && s.snapshots != nil
}

func (s *SearchService) SetCredentialService(service *credential.Service) {
	if s != nil {
		s.credentials = service
	}
}

func (s *SearchService) ResolveSearchRequest(ctx context.Context, request ContextSearchRequest) (ContextSearchRequest, error) {
	if err := ctx.Err(); err != nil {
		return ContextSearchRequest{}, err
	}
	if s == nil || s.snapshots == nil {
		channels, err := tgchannel.NormalizeList(request.Channels)
		if err != nil {
			return ContextSearchRequest{}, err
		}
		request.Channels = channels
		return request, nil
	}

	lease, err := s.snapshots.Acquire()
	if err != nil {
		return ContextSearchRequest{}, err
	}
	defer lease.Release()
	snapshot := lease.Snapshot()
	if snapshot == nil || snapshot.PluginManager == nil {
		return ContextSearchRequest{}, fmt.Errorf("source snapshot is incomplete")
	}
	return resolveManagedSearchRequest(request, snapshot)
}

func resolveManagedSearchRequest(request ContextSearchRequest, snapshot *sourceconfig.Snapshot) (ContextSearchRequest, error) {
	sourceType := strings.ToLower(strings.TrimSpace(request.SourceType))
	if sourceType == "plugin" {
		request.Channels = []string{}
		request.requiresLiveTG = false
	} else {
		if request.Channels == nil {
			request.Channels = append([]string{}, snapshot.Channels()...)
			request.requiresLiveTG = false
		} else {
			channels, err := tgchannel.NormalizeList(request.Channels)
			if err != nil {
				return ContextSearchRequest{}, err
			}
			request.Channels = channels
			publicChannels := make(map[string]struct{}, len(snapshot.Channels()))
			for _, channel := range snapshot.Channels() {
				publicChannels[channel] = struct{}{}
			}
			request.requiresLiveTG = false
			for _, channel := range channels {
				if _, public := publicChannels[channel]; !public {
					request.requiresLiveTG = true
					break
				}
			}
		}
	}
	if sourceType == "tg" {
		request.Plugins = []string{}
	} else {
		request.Plugins = intersectSources(request.Plugins, snapshot.PluginNames())
	}
	return request, nil
}

// injectMainCacheToAsyncPlugins 将主缓存系统注入到异步插件中
func injectMainCacheToAsyncPlugins(pluginManager *plugin.PluginManager, mainCache *cache.EnhancedTwoLevelCache) {
	// 如果缓存或插件管理器不可用，直接返回
	if mainCache == nil || pluginManager == nil {
		return
	}

	// 设置全局序列化器，确保异步插件与主程序使用相同的序列化格式
	serializer := mainCache.GetSerializer()
	if serializer != nil {
		plugin.SetGlobalCacheSerializer(serializer)
	}

	// 创建缓存更新函数（支持IsFinal参数）- 接收原始数据并与现有缓存合并
	cacheUpdater := func(key string, newResults []model.SearchResult, ttl time.Duration, isFinal bool, keyword string, pluginName string) error {
		if len(newResults) == 0 {
			return nil
		}
		// A final callback only means this individual plugin finished; it does
		// not make the aggregate cache complete. The aggregate search marks the
		// envelope complete after every selected plugin has finished.
		merged, err := mergeAndStoreCachedSearch(mainCache, key, newResults, ttl, false)
		if err == nil && config.AppConfig != nil && config.AppConfig.AsyncLogEnabled {
			fmt.Printf("🔄 [%s:%s] 单调合并缓存 | 新增: %d | 合并后: %d | 插件最终: %t\n",
				pluginName, keyword, len(newResults), len(merged.Results), isFinal)
		}
		return err
	}

	// 获取所有插件
	plugins := pluginManager.GetPlugins()

	// 遍历所有插件，找出异步插件
	for _, p := range plugins {
		// 检查插件是否实现了SetMainCacheUpdater方法（修复后的签名，增加关键词参数）
		if asyncPlugin, ok := p.(interface {
			SetMainCacheUpdater(func(string, []model.SearchResult, time.Duration, bool, string) error)
		}); ok {
			// 为每个插件创建专门的缓存更新函数，绑定插件名称
			pluginName := p.Name()
			pluginCacheUpdater := func(key string, newResults []model.SearchResult, ttl time.Duration, isFinal bool, keyword string) error {
				return cacheUpdater(key, newResults, ttl, isFinal, keyword, pluginName)
			}
			// 注入缓存更新函数
			asyncPlugin.SetMainCacheUpdater(pluginCacheUpdater)
		}
	}
}

// Search 执行搜索
func (s *SearchService) Search(keyword string, channels []string, concurrency int, forceRefresh bool, resultType string, sourceType string, plugins []string, cloudTypes []string, ext map[string]interface{}) (model.SearchResponse, error) {
	return s.SearchContext(context.Background(), ContextSearchRequest{
		Keyword: keyword, Channels: channels, Concurrency: concurrency, ForceRefresh: forceRefresh,
		ResultType: resultType, SourceType: sourceType, Plugins: plugins, CloudTypes: cloudTypes,
		Ext: ext, Identity: SearchIdentity{Actor: SearchActorLegacy},
	})
}

func (s *SearchService) SearchContext(ctx context.Context, request ContextSearchRequest) (model.SearchResponse, error) {
	if s != nil && s.snapshots != nil {
		lease, err := s.snapshots.Acquire()
		if err != nil {
			return model.SearchResponse{}, err
		}
		defer lease.Release()
		snapshot := lease.Snapshot()
		if snapshot == nil || snapshot.PluginManager == nil {
			return model.SearchResponse{}, fmt.Errorf("source snapshot is incomplete")
		}
		request, err = resolveManagedSearchRequest(request, snapshot)
		if err != nil {
			return model.SearchResponse{}, err
		}
		static := NewSearchService(snapshot.PluginManager)
		static.credentials = s.credentials
		static.flights = s.flights
		return static.SearchContext(ctx, request)
	}
	return executeSearchFlight(ctx, s.flights, "live", request, func(sharedCtx context.Context) (model.SearchResponse, error) {
		return s.searchContextUncoalesced(sharedCtx, request)
	})
}

func (s *SearchService) searchContextUncoalesced(ctx context.Context, request ContextSearchRequest) (model.SearchResponse, error) {
	if err := ctx.Err(); err != nil {
		return model.SearchResponse{}, err
	}
	if request.ForceRefresh {
		MarkSearchCacheStatus(ctx, SearchCacheRefresh)
	}
	normalizedChannels, err := tgchannel.NormalizeList(request.Channels)
	if err != nil {
		return model.SearchResponse{}, err
	}
	request.Channels = normalizedChannels
	keyword, channels, concurrency, forceRefresh := request.Keyword, request.Channels, request.Concurrency, request.ForceRefresh
	resultType, sourceType, plugins, cloudTypes, ext := request.ResultType, request.SourceType, request.Plugins, request.CloudTypes, request.Ext
	// 确保ext不为nil
	if ext == nil {
		ext = make(map[string]interface{})
	}

	// 参数预处理
	// 源类型标准化
	if sourceType == "" {
		sourceType = "all"
	}

	// 插件参数规范化处理
	if sourceType == "tg" {
		// 对于只搜索Telegram的请求，忽略插件参数
		plugins = nil
	} else if sourceType == "all" || sourceType == "plugin" {
		// 检查是否为空列表或只包含空字符串
		if plugins == nil {
			plugins = nil
		} else if len(plugins) == 0 {
			plugins = []string{}
		} else {
			// 检查是否有非空元素
			hasNonEmpty := false
			for _, p := range plugins {
				if p != "" {
					hasNonEmpty = true
					break
				}
			}

			// 如果全是空字符串，视为未指定
			if !hasNonEmpty {
				plugins = nil
			} else {
				// 检查是否包含所有插件
				allPlugins := s.pluginManager.GetPlugins()
				allPluginNames := make([]string, 0, len(allPlugins))
				for _, p := range allPlugins {
					allPluginNames = append(allPluginNames, strings.ToLower(p.Name()))
				}

				// 创建请求的插件名称集合（忽略空字符串）
				requestedPlugins := make([]string, 0, len(plugins))
				for _, p := range plugins {
					if p != "" {
						requestedPlugins = append(requestedPlugins, strings.ToLower(p))
					}
				}

				// 如果请求的插件数量与所有插件数量相同，检查是否包含所有插件
				if len(requestedPlugins) == len(allPluginNames) {
					// 创建映射以便快速查找
					pluginMap := make(map[string]bool)
					for _, p := range requestedPlugins {
						pluginMap[p] = true
					}

					// 检查是否包含所有插件
					allIncluded := true
					for _, name := range allPluginNames {
						if !pluginMap[name] {
							allIncluded = false
							break
						}
					}

					// 如果包含所有插件，统一设为nil
					if allIncluded {
						plugins = nil
					}
				}
			}
		}
	}

	// 如果未指定并发数，使用配置中的默认值
	if concurrency <= 0 {
		concurrency = config.AppConfig.DefaultConcurrency
	}

	// 并行获取TG搜索和插件搜索结果
	tgBatch := completeSourceSearchBatch()
	pluginBatch := completeSourceSearchBatch()

	var wg sync.WaitGroup
	var tgErr, pluginErr error

	// 如果需要搜索TG
	if (sourceType == "all" || sourceType == "tg") && !(channels != nil && len(channels) == 0) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tgBatch, tgErr = s.searchTGWithStatus(ctx, keyword, channels, forceRefresh)
		}()
	}
	// 如果需要搜索插件（且插件功能已启用）
	if (sourceType == "all" || sourceType == "plugin") && config.AppConfig.AsyncPluginEnabled && !(plugins != nil && len(plugins) == 0) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 对于插件搜索，我们总是希望获取最新的缓存数据
			// 因此，即使forceRefresh=false，我们也需要确保获取到最新的缓存
			pluginBatch, pluginErr = s.searchPluginsForIdentityWithStatus(ctx, request.Identity, keyword, plugins, forceRefresh, concurrency, ext)
		}()
	}

	// 等待所有搜索完成
	wg.Wait()

	// 检查错误
	if tgErr != nil {
		return model.SearchResponse{}, tgErr
	}
	if pluginErr != nil {
		return model.SearchResponse{}, pluginErr
	}

	// 合并结果
	allResults := mergeSearchResults(tgBatch.Results, pluginBatch.Results)

	// 按照优化后的规则排序结果
	sortResultsByTimeAndKeywords(allResults)

	// 过滤结果，只保留有时间的结果或包含优先关键词的结果或高等级插件结果到Results中
	filteredForResults := make([]model.SearchResult, 0, len(allResults))
	for _, result := range allResults {
		source := getResultSource(result)
		pluginLevel := getPluginLevelBySource(source)

		// 有时间的结果或包含优先关键词的结果或高等级插件(1-2级)结果保留在Results中
		if !result.Datetime.IsZero() || getKeywordPriority(result.Title) > 0 || pluginLevel <= 2 {
			filteredForResults = append(filteredForResults, result)
		}
	}

	// 合并链接按网盘类型分组（使用所有过滤后的结果）
	mergedLinks := mergeResultsByType(allResults, keyword, cloudTypes)

	// 构建响应
	var total int
	if resultType == "merged_by_type" {
		// 计算所有类型链接的总数
		total = 0
		for _, links := range mergedLinks {
			total += len(links)
		}
	} else {
		// 只计算filteredForResults的数量
		total = len(filteredForResults)
	}

	response := model.SearchResponse{
		Total:          total,
		Results:        filteredForResults, // 使用进一步过滤的结果
		MergedByType:   mergedLinks,
		Completion:     model.SearchCompletionComplete,
		PartialSources: append(append([]string(nil), tgBatch.PartialSources...), pluginBatch.PartialSources...),
	}
	if !tgBatch.Complete || !pluginBatch.Complete {
		response.Completion = model.SearchCompletionPartial
	}

	// 根据resultType过滤返回结果
	return filterResponseByType(response, resultType), nil
}

// filterResponseByType 根据结果类型过滤响应
func filterResponseByType(response model.SearchResponse, resultType string) model.SearchResponse {
	switch resultType {
	case "merged_by_type":
		// 只返回MergedByType，Results设为nil，结合omitempty标签，JSON序列化时会忽略此字段
		return model.SearchResponse{
			Total:          response.Total,
			MergedByType:   response.MergedByType,
			Results:        nil,
			Completion:     response.Completion,
			PartialSources: response.PartialSources,
		}
	case "all":
		return response
	case "results":
		// 只返回Results
		return model.SearchResponse{
			Total:          response.Total,
			Results:        response.Results,
			Completion:     response.Completion,
			PartialSources: response.PartialSources,
		}
	default:
		// // 默认返回全部
		// return response
		return model.SearchResponse{
			Total:          response.Total,
			MergedByType:   response.MergedByType,
			Results:        nil,
			Completion:     response.Completion,
			PartialSources: response.PartialSources,
		}
	}
}

// 根据时间和关键词排序结果
func sortResultsByTimeAndKeywords(results []model.SearchResult) {
	// 1. 计算每个结果的综合得分
	scores := make([]ResultScore, len(results))

	for i, result := range results {
		source := getResultSource(result)

		scores[i] = ResultScore{
			Result:       result,
			TimeScore:    calculateTimeScore(result.Datetime),
			KeywordScore: getKeywordPriority(result.Title),
			PluginScore:  getPluginLevelScore(source),
			TotalScore:   0, // 稍后计算
		}

		// 计算综合得分
		scores[i].TotalScore = scores[i].TimeScore +
			float64(scores[i].KeywordScore) +
			float64(scores[i].PluginScore)
	}

	// 2. 按综合得分排序
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].TotalScore > scores[j].TotalScore
	})

	// 3. 更新原数组
	for i, score := range scores {
		results[i] = score.Result
	}
}

// 获取标题中包含优先关键词的优先级
func getKeywordPriority(title string) int {
	title = strings.ToLower(title)
	for i, keyword := range priorityKeywords {
		if strings.Contains(title, keyword) {
			// 返回优先级得分（数组索引越小，优先级越高，最高400分）
			return (len(priorityKeywords) - i) * 70
		}
	}
	return 0
}

// 搜索单个频道
func (s *SearchService) searchChannel(keyword string, channel string) ([]model.SearchResult, error) {
	// 构建搜索URL
	url := util.BuildSearchURL(channel, keyword, "")

	// 使用全局HTTP客户端（已配置代理）
	client := util.GetHTTPClient()

	// 创建一个带超时的上下文
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	// 创建请求
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	// 发送请求
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// 读取响应体
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// 解析响应
	results, _, err := util.ParseSearchResults(string(body), channel)
	if err != nil {
		return nil, err
	}

	return results, nil
}

// 用于从消息内容中提取链接-标题对应关系的函数
func extractLinkTitlePairs(content string) map[string]string {
	// 首先尝试使用换行符分割的方法
	if strings.Contains(content, "\n") {
		return extractLinkTitlePairsWithNewlines(content)
	}

	// 如果没有换行符，使用正则表达式直接提取
	return extractLinkTitlePairsWithoutNewlines(content)
}

// 处理有换行符的情况
func extractLinkTitlePairsWithNewlines(content string) map[string]string {
	// 结果映射：链接URL -> 对应标题
	linkTitleMap := make(map[string]string)

	// 按行分割内容
	lines := strings.Split(content, "\n")

	// 链接正则表达式
	linkRegex := regexp.MustCompile(`https?://[^\s"']+`)

	// 第一遍扫描：识别标题-链接对
	var lastTitle string
	var lastTitleIndex int

	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		// 检查当前行是否包含链接
		links := linkRegex.FindAllString(line, -1)

		if len(links) > 0 {
			// 当前行包含链接

			// 检查是否是标准链接行（以"链接："、"地址："等开头）
			isStandardLinkLine := isLinkLine(line)

			if isStandardLinkLine && lastTitle != "" {
				// 标准链接行，使用上一个标题
				for _, link := range links {
					linkTitleMap[link] = lastTitle
				}
			} else if !isStandardLinkLine {
				// 非标准链接行，可能是"标题：链接"格式
				titleFromLine := extractTitleFromLinkLine(line)
				if titleFromLine != "" {
					// 是"标题：链接"格式
					for _, link := range links {
						linkTitleMap[link] = titleFromLine
					}
				} else if lastTitle != "" {
					// 其他情况，使用上一个标题
					for _, link := range links {
						linkTitleMap[link] = lastTitle
					}
				}
			}
		} else {
			// 当前行不包含链接，可能是标题行
			// 检查下一行是否为链接行
			if i+1 < len(lines) {
				nextLine := strings.TrimSpace(lines[i+1])
				if isLinkLine(nextLine) || linkRegex.MatchString(nextLine) {
					// 下一行是链接行或包含链接，当前行很可能是标题
					lastTitle = cleanTitle(line)
					lastTitleIndex = i
				}
			} else {
				// 最后一行，也可能是标题
				lastTitle = cleanTitle(line)
				lastTitleIndex = i
			}
		}
	}

	// 第二遍扫描：处理没有匹配到标题的链接
	// 为每个链接找到最近的上文标题
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		links := linkRegex.FindAllString(line, -1)
		if len(links) == 0 {
			continue
		}

		for _, link := range links {
			if _, exists := linkTitleMap[link]; !exists {
				// 链接没有匹配到标题，尝试找最近的上文标题
				nearestTitle := ""

				// 向上查找最近的标题行
				for j := i - 1; j >= 0; j-- {
					if j == lastTitleIndex || (j+1 < len(lines) &&
						linkRegex.MatchString(lines[j+1]) &&
						!linkRegex.MatchString(lines[j])) {
						candidateTitle := cleanTitle(lines[j])
						if candidateTitle != "" {
							nearestTitle = candidateTitle
							break
						}
					}
				}

				if nearestTitle != "" {
					linkTitleMap[link] = nearestTitle
				}
			}
		}
	}

	return linkTitleMap
}

// 处理没有换行符的情况
func extractLinkTitlePairsWithoutNewlines(content string) map[string]string {
	// 结果映射：链接URL -> 对应标题
	linkTitleMap := make(map[string]string)

	// 使用精确的网盘链接正则表达式集合，避免贪婪匹配
	linkPatterns := []*regexp.Regexp{
		util.TianyiPanPattern, // 天翼云盘
		util.BaiduPanPattern,  // 百度网盘
		util.QuarkPanPattern,  // 夸克网盘
		util.AliyunPanPattern, // 阿里云盘
		util.MobilePanPattern, // 移动云盘
		util.UCPanPattern,     // UC网盘
		util.Pan123Pattern,    // 123网盘
		util.Pan115Pattern,    // 115网盘
		util.XunleiPanPattern, // 迅雷网盘
	}

	// 收集所有链接及其位置
	type linkInfo struct {
		url string
		pos int
	}
	var allLinks []linkInfo

	// 使用各个精确正则表达式查找链接
	for _, pattern := range linkPatterns {
		matches := pattern.FindAllString(content, -1)
		for _, match := range matches {
			pos := strings.Index(content, match)
			if pos >= 0 {
				allLinks = append(allLinks, linkInfo{url: match, pos: pos})
			}
		}
	}

	// 按位置排序
	for i := 0; i < len(allLinks)-1; i++ {
		for j := i + 1; j < len(allLinks); j++ {
			if allLinks[i].pos > allLinks[j].pos {
				allLinks[i], allLinks[j] = allLinks[j], allLinks[i]
			}
		}
	}

	// URL标准化和去重
	uniqueLinks := make(map[string]string) // 标准化URL -> 原始URL
	var links []string

	for _, linkInfo := range allLinks {
		// 标准化URL（将URL编码转换为中文）
		normalized := normalizeUrl(linkInfo.url)

		// 如果这个标准化URL还没有见过，则保留
		if _, exists := uniqueLinks[normalized]; !exists {
			uniqueLinks[normalized] = linkInfo.url
			links = append(links, linkInfo.url)
		}
	}

	if len(links) == 0 {
		return linkTitleMap
	}

	// 使用链接位置分割内容
	segments := make([]string, len(links)+1)
	lastPos := 0

	// 查找每个链接的位置，并提取链接前的文本作为段落
	for i, link := range links {
		idx := strings.Index(content[lastPos:], link)
		if idx == -1 {
			// 链接在content中不存在，跳过
			continue
		}
		pos := idx + lastPos
		if pos > lastPos {
			segments[i] = content[lastPos:pos]
		}
		lastPos = pos + len(link)
	}

	// 最后一段
	if lastPos < len(content) {
		segments[len(links)] = content[lastPos:]
	}

	// 从每个段落中提取标题
	for i, link := range links {
		// 当前链接的标题应该在当前段落的末尾
		var title string

		// 如果是第一个链接
		if i == 0 {
			// 提取第一个段落作为标题
			title = extractTitleBeforeLink(segments[i])
		} else {
			// 从上一个链接后的文本中提取标题
			title = extractTitleBeforeLink(segments[i])
		}

		// 如果提取到了标题，保存链接-标题对应关系
		if title != "" {
			linkTitleMap[link] = title
		}
	}

	return linkTitleMap
}

// 从文本中提取链接前的标题
func extractTitleBeforeLink(text string) string {
	// 移除可能的链接前缀词
	text = strings.TrimSpace(text)

	// 查找"链接："前的文本作为标题
	if idx := strings.Index(text, "链接："); idx > 0 {
		return cleanTitle(text[:idx])
	}

	// 尝试匹配常见的标题模式
	titlePattern := regexp.MustCompile(`([^链地资网\s]+?(?:\([^)]+\))?(?:\s*\d+K)?(?:\s*臻彩)?(?:\s*MAX)?(?:\s*HDR)?(?:\s*更(?:新)?\d+集))$`)
	matches := titlePattern.FindStringSubmatch(text)
	if len(matches) > 1 {
		return cleanTitle(matches[1])
	}

	return cleanTitle(text)
}

// 判断一行是否为链接行（主要包含链接的行）
func isLinkLine(line string) bool {
	lowerLine := strings.ToLower(line)
	return strings.HasPrefix(lowerLine, "链接：") ||
		strings.HasPrefix(lowerLine, "地址：") ||
		strings.HasPrefix(lowerLine, "资源地址：") ||
		strings.HasPrefix(lowerLine, "网盘：") ||
		strings.HasPrefix(lowerLine, "网盘地址：") ||
		strings.HasPrefix(lowerLine, "链接:")
}

// 从链接行中提取可能的标题
func extractTitleFromLinkLine(line string) string {
	// 处理"标题：链接"格式
	parts := strings.SplitN(line, "：", 2)
	if len(parts) == 2 && !strings.Contains(parts[0], "http") &&
		!isLinkPrefix(parts[0]) {
		return cleanTitle(parts[0])
	}

	// 处理"标题:链接"格式（半角冒号）
	parts = strings.SplitN(line, ":", 2)
	if len(parts) == 2 && !strings.Contains(parts[0], "http") &&
		!isLinkPrefix(parts[0]) {
		return cleanTitle(parts[0])
	}

	return ""
}

// 判断是否为链接前缀词（包括网盘名称）
func isLinkPrefix(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))

	// 标准链接前缀词
	if text == "链接" ||
		text == "地址" ||
		text == "资源地址" ||
		text == "网盘" ||
		text == "网盘地址" {
		return true
	}

	// 网盘名称（防止误将网盘名称当作标题）
	cloudDiskNames := []string{
		// 夸克网盘
		"夸克", "夸克网盘", "quark", "夸克云盘",

		// 百度网盘
		"百度", "百度网盘", "baidu", "百度云", "bdwp", "bdpan",

		// 迅雷网盘
		"迅雷", "迅雷网盘", "xunlei", "迅雷云盘",

		// 115网盘
		"115", "115网盘", "115云盘",

		// 123网盘
		"123", "123pan", "123网盘", "123云盘",

		// 阿里云盘
		"阿里", "阿里云", "阿里云盘", "aliyun", "alipan", "阿里网盘",

		// 光鸭云盘
		"光鸭", "光鸭云盘", "光鸭网盘", "guangya",

		// 天翼云盘
		"天翼", "天翼云", "天翼云盘", "tianyi", "天翼网盘",

		// UC网盘
		"uc", "uc网盘", "uc云盘",

		// 移动云盘
		"移动", "移动云", "移动云盘", "caiyun", "彩云",

		// PikPak
		"pikpak", "pikpak网盘",
	}

	for _, name := range cloudDiskNames {
		if text == name {
			return true
		}
	}

	return false
}

// 清理标题文本
func cleanTitle(title string) string {
	// 移除常见的无关前缀
	title = strings.TrimSpace(title)
	title = strings.TrimPrefix(title, "名称：")
	title = strings.TrimPrefix(title, "标题：")
	title = strings.TrimPrefix(title, "片名：")
	title = strings.TrimPrefix(title, "名称:")
	title = strings.TrimPrefix(title, "标题:")
	title = strings.TrimPrefix(title, "片名:")

	// 移除表情符号和特殊字符
	emojiRegex := regexp.MustCompile(`[\p{So}\p{Sk}]`)
	title = emojiRegex.ReplaceAllString(title, "")

	return strings.TrimSpace(title)
}

// 判断一行是否为空或只包含空白字符
func isEmpty(line string) bool {
	return strings.TrimSpace(line) == ""
}

// 将搜索结果按网盘类型分组
func mergeResultsByType(results []model.SearchResult, keyword string, cloudTypes []string) model.MergedLinks {
	// 创建合并结果的映射
	mergedLinks := make(model.MergedLinks, 12) // 预分配容量，假设有12种不同的网盘类型

	// 用于去重的映射，键为URL
	uniqueLinks := make(map[string]model.MergedLink)

	// 将关键词转为小写，用于不区分大小写的匹配
	lowerKeyword := strings.ToLower(keyword)

	// 遍历所有搜索结果
	for _, result := range results {
		// 提取消息中的链接-标题对应关系
		linkTitleMap := extractLinkTitlePairs(result.Content)

		// 如果没有从内容中提取到标题，尝试直接从内容中匹配
		if len(linkTitleMap) == 0 && len(result.Links) > 0 && !strings.Contains(result.Content, "\n") {
			// 这是没有换行符的情况，尝试直接匹配
			content := result.Content

			// 支持多种网盘链接前缀
			linkPrefixes := []string{"天翼链接：", "百度链接：", "夸克链接：", "阿里链接：", "UC链接：", "115链接：", "迅雷链接：", "123链接：", "链接："}

			var parts []string

			// 尝试找到匹配的前缀
			for _, prefix := range linkPrefixes {
				if strings.Contains(content, prefix) {
					parts = strings.Split(content, prefix)
					break
				}
			}

			// 如果找到了匹配的前缀并且分割成功
			if len(parts) > 1 && len(result.Links) <= len(parts)-1 {
				// 第一部分是第一个标题
				titles := make([]string, 0, len(parts))
				titles = append(titles, cleanTitle(parts[0]))

				// 处理每个包含链接的部分，提取标题
				for i := 1; i < len(parts)-1; i++ {
					part := parts[i]
					// 找到链接的结束位置，使用更通用的分隔符
					linkEnd := -1
					for j, c := range part {
						// 扩展分隔符列表，包含更多可能的字符
						if c == ' ' || c == '窃' || c == '东' || c == '迎' || c == '千' || c == '我' || c == '恋' || c == '将' || c == '野' ||
							c == '合' || c == '集' || c == '天' || c == '翼' || c == '网' || c == '盘' || c == '(' || c == '（' {
							linkEnd = j
							break
						}
					}

					if linkEnd > 0 {
						// 提取标题
						title := cleanTitle(part[linkEnd:])
						titles = append(titles, title)
					}
				}

				// 将标题与链接关联
				for i, link := range result.Links {
					if i < len(titles) {
						linkTitleMap[link.URL] = titles[i]
					}
				}
			}
		}

		for _, link := range result.Links {
			// 优先使用链接的WorkTitle字段，如果为空则回退到传统方式
			title := result.Title // 默认使用消息标题

			if link.WorkTitle != "" {
				// 如果链接有WorkTitle字段，优先使用
				title = link.WorkTitle
			} else {
				// 如果没有WorkTitle，使用传统方式从映射中获取该链接对应的标题
				// 查找完全匹配的链接
				if specificTitle, found := linkTitleMap[link.URL]; found && specificTitle != "" {
					title = specificTitle // 如果找到特定标题，则使用它
				} else {
					// 如果没有找到完全匹配的链接，尝试查找前缀匹配的链接
					for mappedLink, mappedTitle := range linkTitleMap {
						if strings.HasPrefix(mappedLink, link.URL) {
							title = mappedTitle
							break
						}
					}
				}
			}

			// 检查插件是否需要跳过Service层过滤
			var skipKeywordFilter bool = false
			if result.UniqueID != "" && strings.Contains(result.UniqueID, "-") {
				parts := strings.SplitN(result.UniqueID, "-", 2)
				if len(parts) >= 1 {
					pluginName := parts[0]
					// 通过插件注册表动态获取过滤设置
					if pluginInstance, exists := plugin.GetPluginByName(pluginName); exists {
						skipKeywordFilter = pluginInstance.SkipServiceFilter()
					}
				}
			}

			// 关键词过滤：现在我们有了准确的链接-标题对应关系，只需检查每个链接的具体标题
			if !skipKeywordFilter && keyword != "" {
				// 只检查链接的具体标题，无论是TG来源还是插件来源
				if !strings.Contains(strings.ToLower(title), lowerKeyword) {
					continue
				}
			}

			// 确定数据来源
			var source string
			if result.Channel != "" {
				// 来自TG频道
				source = "tg:" + result.Channel
			} else if result.UniqueID != "" && strings.Contains(result.UniqueID, "-") {
				// 来自插件：UniqueID格式通常为 "插件名-ID"
				parts := strings.SplitN(result.UniqueID, "-", 2)
				if len(parts) >= 1 {
					source = "plugin:" + parts[0]
				}
			} else {
				// 无法确定来源，使用默认值
				source = "unknown"
			}

			// 赋值给Note前，支持多个关键词裁剪
			title = util.CutTitleByKeywords(title, []string{"简介", "描述"})

			// 优先使用链接自己的时间，如果没有则使用搜索结果的时间
			linkDatetime := result.Datetime
			if !link.Datetime.IsZero() {
				linkDatetime = link.Datetime
			}

			mergedLink := model.MergedLink{
				URL:       link.URL,
				Password:  link.Password,
				Note:      title, // 使用找到的特定标题
				Datetime:  linkDatetime,
				Source:    source, // 添加数据来源字段
				SubSource: result.SubSource,
				Images:    result.Images, // 添加TG消息中的图片链接
			}

			// 检查是否已存在相同URL的链接
			if existingLink, exists := uniqueLinks[link.URL]; exists {
				// 如果已存在，只有当当前链接的时间更新时才替换
				if mergedLink.Datetime.After(existingLink.Datetime) {
					uniqueLinks[link.URL] = mergedLink
				}
			} else {
				// 如果不存在，直接添加
				uniqueLinks[link.URL] = mergedLink
			}
		}
	}

	// 为保持排序顺序，按原始results顺序处理链接，而不是随机遍历map
	// 创建一个有序的链接列表，按原始results中的顺序
	orderedLinks := make([]model.MergedLink, 0, len(uniqueLinks))
	linkTypeMap := make(map[string]string) // URL -> Type的映射

	// 按原始results的顺序收集唯一链接
	for _, result := range results {
		for _, link := range result.Links {
			if mergedLink, exists := uniqueLinks[link.URL]; exists {
				// 检查是否已经添加过这个链接
				found := false
				for _, existing := range orderedLinks {
					if existing.URL == link.URL {
						found = true
						break
					}
				}
				if !found {
					orderedLinks = append(orderedLinks, mergedLink)
					linkTypeMap[link.URL] = link.Type
				}
			}
		}
	}

	// 将有序链接按类型分组
	for _, mergedLink := range orderedLinks {
		// 从预建的映射中获取链接类型
		linkType := linkTypeMap[mergedLink.URL]
		if linkType == "" {
			linkType = "unknown"
		}

		// 添加到对应类型的列表中
		mergedLinks[linkType] = append(mergedLinks[linkType], mergedLink)
	}

	// 如果指定了cloudTypes，则过滤结果
	if len(cloudTypes) > 0 {
		// 创建过滤后的结果映射
		filteredLinks := make(model.MergedLinks)

		// 将cloudTypes转换为map以提高查找性能
		allowedTypes := make(map[string]bool)
		for _, cloudType := range cloudTypes {
			allowedTypes[strings.ToLower(strings.TrimSpace(cloudType))] = true
		}

		// 只保留指定类型的链接
		for linkType, links := range mergedLinks {
			if allowedTypes[strings.ToLower(linkType)] {
				filteredLinks[linkType] = links
			}
		}

		return filteredLinks
	}

	return mergedLinks
}

// searchTG 搜索TG频道
func (s *SearchService) searchTG(ctx context.Context, keyword string, channels []string, forceRefresh bool) ([]model.SearchResult, error) {
	batch, err := s.searchTGWithStatus(ctx, keyword, channels, forceRefresh)
	return batch.Results, err
}

type tgChannelSearchResult struct {
	Channel string
	Results []model.SearchResult
	Err     error
}

func (s *SearchService) searchTGWithStatus(ctx context.Context, keyword string, channels []string, forceRefresh bool) (sourceSearchBatch, error) {
	// 生成缓存键
	cacheKey := cache.GenerateTGCacheKey(keyword, channels)
	seed := cachedSearchResults{}

	// 如果未启用强制刷新，尝试从缓存获取结果
	if !forceRefresh && cacheInitialized && config.AppConfig.CacheEnabled && enhancedTwoLevelCache != nil {
		cached, hit, err := loadCachedSearch(enhancedTwoLevelCache, cacheKey)
		if err == nil && hit && cached.Complete {
			MarkSearchCacheStatus(ctx, SearchCacheHit)
			return sourceSearchBatch{Results: cached.Results, Complete: true}, nil
		}
		if err != nil {
			MarkSearchCacheStatus(ctx, SearchCacheBypass)
		} else {
			MarkSearchCacheStatus(ctx, SearchCacheMiss)
			if hit {
				seed = cached
			}
		}
	} else if !forceRefresh {
		MarkSearchCacheStatus(ctx, SearchCacheBypass)
	}

	// 缓存未命中或强制刷新，执行实际搜索
	var results []model.SearchResult

	// 使用工作池并行搜索多个频道
	tasks := make([]pool.Task, 0, len(channels))

	for _, channel := range channels {
		ch := channel // 创建副本，避免闭包问题
		tasks = append(tasks, func() interface{} {
			results, err := s.searchChannel(keyword, ch)
			return tgChannelSearchResult{Channel: ch, Results: results, Err: err}
		})
	}

	// 执行搜索任务并获取结果
	workers := tgSearchWorkerCount(len(channels))
	taskResults := executeTGTasksWithTimeout(tasks, workers, config.AppConfig.PluginTimeout)

	complete := len(taskResults) == len(tasks)
	finished := make(map[string]struct{}, len(taskResults))
	partialSources := make([]string, 0)
	// 合并所有频道的结果
	for _, result := range taskResults {
		outcome, ok := result.(tgChannelSearchResult)
		if !ok {
			complete = false
			continue
		}
		finished[outcome.Channel] = struct{}{}
		if outcome.Err != nil {
			complete = false
			partialSources = append(partialSources, "tg:"+outcome.Channel)
			continue
		}
		results = append(results, outcome.Results...)
	}
	for _, channel := range channels {
		if _, ok := finished[channel]; !ok {
			partialSources = append(partialSources, "tg:"+channel)
		}
	}
	results = mergeSearchResults(seed.Results, results)

	// Cache updates are monotonic and carry completeness metadata. A timed-out
	// subset can seed a later request but can never be served as complete.
	if cacheInitialized && config.AppConfig.CacheEnabled {
		go func(res []model.SearchResult, isComplete bool) {
			ttl := time.Duration(config.AppConfig.CacheTTLMinutes) * time.Minute
			_, _ = mergeAndStoreCachedSearch(enhancedTwoLevelCache, cacheKey, res, ttl, isComplete)
		}(results, complete)
	}

	return sourceSearchBatch{Results: results, Complete: complete, PartialSources: partialSources}, nil
}

func tgSearchWorkerCount(channelCount int) int {
	if channelCount <= 0 {
		return 0
	}
	if channelCount > maxTGSearchWorkers {
		return maxTGSearchWorkers
	}
	return channelCount
}

type indexedTGTaskResult struct {
	index int
	value interface{}
}

func executeTGTasksWithTimeout(tasks []pool.Task, maxWorkers int, timeout time.Duration) []interface{} {
	if len(tasks) == 0 || maxWorkers <= 0 {
		return []interface{}{}
	}
	if maxWorkers > len(tasks) {
		maxWorkers = len(tasks)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	jobs := make(chan int)
	completed := make(chan indexedTGTaskResult, len(tasks))
	var workers sync.WaitGroup

	for worker := 0; worker < maxWorkers; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case index, ok := <-jobs:
					if !ok {
						return
					}
					value := tasks[index]()
					select {
					case completed <- indexedTGTaskResult{index: index, value: value}:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for index := range tasks {
			select {
			case jobs <- index:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(completed)
	}()

	ordered := make([]interface{}, len(tasks))
	finished := make([]bool, len(tasks))
	for {
		select {
		case result, ok := <-completed:
			if !ok {
				return compactTGTaskResults(ordered, finished)
			}
			ordered[result.index] = result.value
			finished[result.index] = true
		case <-ctx.Done():
			return compactTGTaskResults(ordered, finished)
		}
	}
}

func compactTGTaskResults(values []interface{}, finished []bool) []interface{} {
	result := make([]interface{}, 0, len(values))
	for index, value := range values {
		if finished[index] {
			result = append(result, value)
		}
	}
	return result
}

// searchPlugins 搜索插件
func (s *SearchService) searchPlugins(ctx context.Context, keyword string, plugins []string, forceRefresh bool, concurrency int, ext map[string]interface{}) ([]model.SearchResult, error) {
	batch, err := s.searchPluginsWithStatus(ctx, keyword, plugins, forceRefresh, concurrency, ext)
	return batch.Results, err
}

type pluginSearchWithResult interface {
	SearchWithResult(string, map[string]interface{}) (model.PluginSearchResult, error)
}

type pluginTaskResult struct {
	Source   string
	Results  []model.SearchResult
	Complete bool
	Err      error
}

func (s *SearchService) searchPluginsWithStatus(ctx context.Context, keyword string, plugins []string, forceRefresh bool, concurrency int, ext map[string]interface{}) (sourceSearchBatch, error) {
	baseExt := cloneSearchExt(ext)
	if forceRefresh {
		baseExt["refresh"] = true
	}

	// 生成缓存键
	cacheKey := cache.GeneratePluginCacheKey(keyword, plugins)
	seed := cachedSearchResults{}

	// 如果未启用强制刷新，尝试从缓存获取结果
	if !forceRefresh && cacheInitialized && config.AppConfig.CacheEnabled && enhancedTwoLevelCache != nil {
		cached, hit, err := loadCachedSearch(enhancedTwoLevelCache, cacheKey)
		if err == nil && hit && cached.Complete {
			MarkSearchCacheStatus(ctx, SearchCacheHit)
			fmt.Printf("✅ [%s] 命中完整缓存 结果数: %d\n", keyword, len(cached.Results))
			return sourceSearchBatch{Results: cached.Results, Complete: true}, nil
		}
		if err != nil {
			MarkSearchCacheStatus(ctx, SearchCacheBypass)
		} else {
			MarkSearchCacheStatus(ctx, SearchCacheMiss)
			if hit {
				seed = cached
			}
		}
	} else if !forceRefresh {
		MarkSearchCacheStatus(ctx, SearchCacheBypass)
	}

	// 缓存未命中或强制刷新，执行实际搜索

	// 获取所有可用插件
	var availablePlugins []plugin.AsyncSearchPlugin
	if s.pluginManager != nil {
		allPlugins := s.pluginManager.GetPlugins()

		// 确保plugins不为nil并且有非空元素
		hasPlugins := plugins != nil && len(plugins) > 0
		hasNonEmptyPlugin := false

		if hasPlugins {
			for _, p := range plugins {
				if p != "" {
					hasNonEmptyPlugin = true
					break
				}
			}
		}

		// 只有当plugins数组包含非空元素时才进行过滤
		if hasPlugins && hasNonEmptyPlugin {
			pluginMap := make(map[string]bool)
			for _, p := range plugins {
				if p != "" { // 忽略空字符串
					pluginMap[strings.ToLower(p)] = true
				}
			}

			for _, p := range allPlugins {
				if pluginMap[strings.ToLower(p.Name())] {
					availablePlugins = append(availablePlugins, p)
				}
			}
		} else {
			// 如果plugins为nil、空数组或只包含空字符串，视为未指定，使用所有插件
			availablePlugins = allPlugins
		}
	}

	// 控制并发数
	if concurrency <= 0 {
		// 使用配置中的默认值
		concurrency = config.AppConfig.DefaultConcurrency
	}

	// 使用工作池执行并行搜索
	tasks := make([]pool.Task, 0, len(availablePlugins))
	for _, p := range availablePlugins {
		instance := p // 创建副本，避免闭包问题
		tasks = append(tasks, func() interface{} {
			pluginExt := cloneSearchExt(baseExt)
			// 设置主缓存键和当前关键词
			instance.SetMainCacheKey(cacheKey)
			instance.SetCurrentKeyword(keyword)

			if detailed, ok := instance.(pluginSearchWithResult); ok {
				result, err := detailed.SearchWithResult(keyword, pluginExt)
				return pluginTaskResult{Source: instance.Name(), Results: result.Results, Complete: result.IsFinal, Err: err}
			}
			results, err := instance.Search(keyword, pluginExt)
			return pluginTaskResult{Source: instance.Name(), Results: results, Complete: err == nil, Err: err}
		})
	}

	// 执行搜索任务并获取结果
	results := pool.ExecuteBatchWithTimeout(tasks, concurrency, config.AppConfig.PluginTimeout)

	// 合并所有插件的结果，过滤掉无链接的结果
	allResults := append([]model.SearchResult(nil), seed.Results...)
	complete := len(results) == len(tasks)
	finished := make(map[string]struct{}, len(results))
	partialSources := make([]string, 0)
	for _, value := range results {
		outcome, ok := value.(pluginTaskResult)
		if !ok {
			complete = false
			continue
		}
		finished[outcome.Source] = struct{}{}
		if outcome.Err != nil || !outcome.Complete {
			complete = false
			partialSources = append(partialSources, "plugin:"+outcome.Source)
		}
		for _, pluginResult := range outcome.Results {
			if len(pluginResult.Links) > 0 {
				allResults = append(allResults, pluginResult)
			}
		}
	}
	for _, instance := range availablePlugins {
		if _, ok := finished[instance.Name()]; !ok {
			partialSources = append(partialSources, "plugin:"+instance.Name())
		}
	}
	allResults = mergeSearchResults(nil, allResults)

	// 恢复主程序缓存更新：确保最终合并结果被正确缓存
	if cacheInitialized && config.AppConfig.CacheEnabled {
		go func(res []model.SearchResult, kw string, key string, isComplete bool) {
			ttl := time.Duration(config.AppConfig.CacheTTLMinutes) * time.Minute
			merged, err := mergeAndStoreCachedSearch(enhancedTwoLevelCache, key, res, ttl, isComplete)
			if err != nil {
				fmt.Printf("[主程序] 缓存更新失败: %s | 错误: %v\n", key, err)
				return
			}
			if config.AppConfig != nil && config.AppConfig.AsyncLogEnabled {
				fmt.Printf("[主程序] 缓存更新完成: %s | 结果数: %d | 完整: %t\n", key, len(merged.Results), merged.Complete)
			}
		}(allResults, keyword, cacheKey, complete)
	}

	return sourceSearchBatch{Results: allResults, Complete: complete, PartialSources: partialSources}, nil
}

// searchPluginsForIdentity keeps the legacy plugin cache for stateless
// plugins, while account-backed plugins execute synchronously with credentials
// resolved for the current tenant. This prevents one user's account state or
// background refresh from leaking into another request.
func (s *SearchService) searchPluginsForIdentity(ctx context.Context, identity SearchIdentity, keyword string, requested []string, forceRefresh bool, concurrency int, ext map[string]interface{}) ([]model.SearchResult, error) {
	batch, err := s.searchPluginsForIdentityWithStatus(ctx, identity, keyword, requested, forceRefresh, concurrency, ext)
	return batch.Results, err
}

func (s *SearchService) searchPluginsForIdentityWithStatus(ctx context.Context, identity SearchIdentity, keyword string, requested []string, forceRefresh bool, concurrency int, ext map[string]interface{}) (sourceSearchBatch, error) {
	if s == nil || s.credentials == nil || s.pluginManager == nil {
		return s.searchPluginsWithStatus(ctx, keyword, requested, forceRefresh, concurrency, ext)
	}

	selected := selectPlugins(s.pluginManager.GetPlugins(), requested)
	legacyNames := make([]string, 0, len(selected))
	managed := make([]struct {
		plugin   plugin.AsyncSearchPlugin
		searcher credential.LayerSearcher
	}, 0)
	for _, instance := range selected {
		if searcher, ok := instance.(credential.LayerSearcher); ok {
			managed = append(managed, struct {
				plugin   plugin.AsyncSearchPlugin
				searcher credential.LayerSearcher
			}{plugin: instance, searcher: searcher})
			continue
		}
		legacyNames = append(legacyNames, instance.Name())
	}

	legacyBatch := completeSourceSearchBatch()
	if len(legacyNames) > 0 {
		var err error
		legacyBatch, err = s.searchPluginsWithStatus(ctx, keyword, legacyNames, forceRefresh, concurrency, ext)
		if err != nil {
			return sourceSearchBatch{}, err
		}
	}
	if len(managed) == 0 {
		return legacyBatch, nil
	}
	if !forceRefresh {
		MarkSearchCacheStatus(ctx, SearchCacheBypass)
	}

	actor := credential.ActorUser
	switch identity.Actor {
	case SearchActorAdmin:
		actor = credential.ActorAdmin
	case SearchActorCollector:
		actor = credential.ActorCollector
	case SearchActorUser:
		actor = credential.ActorUser
	default:
		// Database mode authenticates external requests. Treat internal or
		// legacy invocations as collector work so they use admin-private then
		// shared credentials instead of an arbitrary user's account.
		actor = credential.ActorCollector
	}
	credentialIdentity := credential.Identity{Actor: actor, UserID: identity.UserID}
	baseExt := cloneSearchExt(ext)
	if forceRefresh {
		baseExt["refresh"] = true
	}

	access := credential.Access{
		Open: s.credentials.OpenStored,
		Success: func(callbackCtx context.Context, publicID string) {
			_ = s.credentials.Success(callbackCtx, publicID)
		},
		Failure: func(callbackCtx context.Context, publicID, status, code string, cooldown *time.Time) {
			_ = s.credentials.Failure(callbackCtx, publicID, status, code, cooldown)
		},
	}

	tasks := make([]pool.Task, 0, len(managed))
	for _, item := range managed {
		item := item
		tasks = append(tasks, func() interface{} {
			layers, err := s.credentials.Resolve(ctx, credentialIdentity, item.plugin.Name(), 20)
			if err != nil {
				return pluginTaskResult{Source: item.plugin.Name(), Complete: false, Err: err}
			}
			pluginExt := cloneSearchExt(baseExt)
			results, succeeded, searchErr := item.searcher.SearchCredentialLayer(ctx, keyword, pluginExt, layers.Private, access)
			if succeeded {
				return pluginTaskResult{Source: item.plugin.Name(), Results: results, Complete: searchErr == nil, Err: searchErr}
			}
			results, succeeded, searchErr = item.searcher.SearchCredentialLayer(ctx, keyword, pluginExt, layers.Shared, access)
			if !succeeded {
				if searchErr == nil {
					searchErr = fmt.Errorf("all credential layers failed")
				}
				return pluginTaskResult{Source: item.plugin.Name(), Complete: false, Err: searchErr}
			}
			return pluginTaskResult{Source: item.plugin.Name(), Results: results, Complete: searchErr == nil, Err: searchErr}
		})
	}

	managedResults := pool.ExecuteBatchWithTimeout(tasks, concurrency, config.AppConfig.PluginTimeout)
	allResults := append([]model.SearchResult(nil), legacyBatch.Results...)
	complete := legacyBatch.Complete && len(managedResults) == len(tasks)
	partialSources := append([]string(nil), legacyBatch.PartialSources...)
	finished := make(map[string]struct{}, len(managedResults))
	for _, value := range managedResults {
		outcome, ok := value.(pluginTaskResult)
		if !ok {
			complete = false
			continue
		}
		finished[outcome.Source] = struct{}{}
		if outcome.Err != nil || !outcome.Complete {
			complete = false
			partialSources = append(partialSources, "plugin:"+outcome.Source)
		}
		for _, result := range outcome.Results {
			if len(result.Links) > 0 {
				allResults = append(allResults, result)
			}
		}
	}
	for _, item := range managed {
		if _, ok := finished[item.plugin.Name()]; !ok {
			partialSources = append(partialSources, "plugin:"+item.plugin.Name())
		}
	}
	return sourceSearchBatch{Results: mergeSearchResults(nil, allResults), Complete: complete, PartialSources: partialSources}, nil
}

func selectPlugins(all []plugin.AsyncSearchPlugin, requested []string) []plugin.AsyncSearchPlugin {
	if len(requested) == 0 {
		return all
	}
	wanted := make(map[string]struct{}, len(requested))
	for _, name := range requested {
		if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
			wanted[name] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return all
	}
	selected := make([]plugin.AsyncSearchPlugin, 0, len(wanted))
	for _, instance := range all {
		if _, ok := wanted[strings.ToLower(instance.Name())]; ok {
			selected = append(selected, instance)
		}
	}
	return selected
}

func cloneSearchExt(ext map[string]interface{}) map[string]interface{} {
	copyExt := make(map[string]interface{}, len(ext)+1)
	for key, value := range ext {
		copyExt[key] = value
	}
	return copyExt
}

// GetPluginManager 获取插件管理器
func (s *SearchService) GetPluginManager() *plugin.PluginManager {
	if s != nil && s.snapshots != nil {
		lease, err := s.snapshots.Acquire()
		if err != nil {
			return nil
		}
		defer lease.Release()
		return lease.Snapshot().PluginManager
	}
	return s.pluginManager
}

func intersectSources(requested, enabled []string) []string {
	if len(enabled) == 0 {
		return []string{}
	}
	if requested == nil {
		return append([]string(nil), enabled...)
	}
	if len(requested) == 0 {
		return []string{}
	}
	allowed := make(map[string]string, len(enabled))
	for _, value := range enabled {
		allowed[strings.ToLower(strings.TrimSpace(value))] = value
	}
	result := make([]string, 0, len(requested))
	seen := make(map[string]struct{}, len(requested))
	for _, value := range requested {
		key := strings.ToLower(strings.TrimSpace(value))
		canonical, ok := allowed[key]
		if !ok {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, canonical)
	}
	return result
}

// =============================================================================
// 轻量级插件优先级排序实现
// =============================================================================

// ResultScore 搜索结果评分结构
type ResultScore struct {
	Result       model.SearchResult
	TimeScore    float64 // 时间得分
	KeywordScore int     // 关键词得分
	PluginScore  int     // 插件等级得分
	TotalScore   float64 // 综合得分
}

// 插件等级缓存
var (
	pluginLevelCache = sync.Map{} // 插件等级缓存
)

// getResultSource 从SearchResult推断数据来源
func getResultSource(result model.SearchResult) string {
	if result.Channel != "" {
		// 来自TG频道
		return "tg:" + result.Channel
	} else if result.UniqueID != "" && strings.Contains(result.UniqueID, "-") {
		// 来自插件：UniqueID格式通常为 "插件名-ID"
		parts := strings.SplitN(result.UniqueID, "-", 2)
		if len(parts) >= 1 {
			return "plugin:" + parts[0]
		}
	}
	return "unknown"
}

// getPluginLevelBySource 根据来源获取插件等级
func getPluginLevelBySource(source string) int {
	// 尝试从缓存获取
	if level, ok := pluginLevelCache.Load(source); ok {
		return level.(int)
	}

	parts := strings.Split(source, ":")
	if len(parts) != 2 {
		pluginLevelCache.Store(source, 3)
		return 3 // 默认等级
	}

	if parts[0] == "tg" {
		pluginLevelCache.Store(source, 3)
		return 3 // TG搜索等同于等级3
	}

	if parts[0] == "plugin" {
		level := getPluginPriorityByName(parts[1])
		pluginLevelCache.Store(source, level)
		return level
	}

	pluginLevelCache.Store(source, 3)
	return 3
}

// getPluginPriorityByName 根据插件名获取优先级
func getPluginPriorityByName(pluginName string) int {
	// 从插件管理器动态获取真实的优先级 (O(1)哈希查找)
	if pluginInstance, exists := plugin.GetPluginByName(pluginName); exists {
		return pluginInstance.Priority()
	}
	return 3 // 默认等级
}

// getPluginLevelScore 获取插件等级得分
func getPluginLevelScore(source string) int {
	level := getPluginLevelBySource(source)

	switch level {
	case 1:
		return 1000 // 等级1插件：1000分
	case 2:
		return 500 // 等级2插件：500分
	case 3:
		return 0 // 等级3插件：0分
	case 4:
		return -200 // 等级4插件：-200分
	default:
		return 0 // 默认使用等级3得分
	}
}

// calculateTimeScore 计算时间得分
func calculateTimeScore(datetime time.Time) float64 {
	if datetime.IsZero() {
		return 0 // 无时间信息得0分
	}

	now := time.Now()
	daysDiff := now.Sub(datetime).Hours() / 24

	// 时间得分：越新得分越高，最大500分（增加时间权重）
	switch {
	case daysDiff <= 1:
		return 500 // 1天内
	case daysDiff <= 3:
		return 400 // 3天内
	case daysDiff <= 7:
		return 300 // 1周内
	case daysDiff <= 30:
		return 200 // 1月内
	case daysDiff <= 90:
		return 100 // 3月内
	case daysDiff <= 365:
		return 50 // 1年内
	default:
		return 20 // 1年以上
	}
}
