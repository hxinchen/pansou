package hunhepan

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	cloudscraper "github.com/Advik-B/cloudscraper/lib"
	"pansou/model"
	"pansou/plugin"
	"pansou/util/json"
)

var debugEnabled = false

func debugLog(format string, args ...interface{}) {
	if debugEnabled {
		log.Printf("[hunhepan DEBUG] "+format, args...)
	}
}

// 在init函数中注册插件
func init() {
	// 注册插件
	plugin.RegisterGlobalPlugin(NewHunhepanAsyncPlugin())
}

const (
	// API端点
	HunhepanAPI = "https://hunhepan.com/open/search/disk"

	// 默认页大小
	DefaultPageSize = 30
)

type searchEndpoint struct {
	name    string
	apiURL  string
	referer string
	origin  string
}

var searchEndpoints = []searchEndpoint{
	{name: "hunhepan", apiURL: HunhepanAPI, referer: "https://hunhepan.com/search", origin: "https://hunhepan.com"},
}

// HunhepanAsyncPlugin 混合盘搜索异步插件
type HunhepanAsyncPlugin struct {
	*plugin.BaseAsyncPlugin
	scraper *cloudscraper.Scraper
}

// NewHunhepanAsyncPlugin 创建新的混合盘搜索异步插件
func NewHunhepanAsyncPlugin() *HunhepanAsyncPlugin {
	scraper, err := cloudscraper.New()
	if err != nil {
		log.Printf("[hunhepan] cloudscraper 初始化失败: %v", err)
	}
	return &HunhepanAsyncPlugin{
		BaseAsyncPlugin: plugin.NewBaseAsyncPlugin("hunhepan", 3),
		scraper:         scraper,
	}
}

// Search 执行搜索并返回结果（兼容性方法）
func (p *HunhepanAsyncPlugin) Search(keyword string, ext map[string]interface{}) ([]model.SearchResult, error) {
	result, err := p.SearchWithResult(keyword, ext)
	if err != nil {
		return nil, err
	}
	return result.Results, nil
}

// SearchWithResult 执行搜索并返回包含IsFinal标记的结果
func (p *HunhepanAsyncPlugin) SearchWithResult(keyword string, ext map[string]interface{}) (model.PluginSearchResult, error) {
	return p.AsyncSearchWithResult(keyword, p.doSearch, p.MainCacheKey, ext)
}

// doSearch 实际的搜索实现
func (p *HunhepanAsyncPlugin) doSearch(client *http.Client, keyword string, ext map[string]interface{}) ([]model.SearchResult, error) {
	debugLog("开始搜索，关键词: %s", keyword)

	resultChan := make(chan []HunhepanItem, len(searchEndpoints))
	errChan := make(chan error, len(searchEndpoints))

	// 创建等待组
	var wg sync.WaitGroup
	wg.Add(len(searchEndpoints))
	for _, endpoint := range searchEndpoints {
		endpoint := endpoint
		go func() {
			defer wg.Done()
			items, err := p.searchAPI(client, endpoint, keyword)
			if err != nil {
				errChan <- fmt.Errorf("%s API error: %w", endpoint.name, err)
				return
			}
			resultChan <- items
		}()
	}

	// 启动一个goroutine等待所有请求完成并关闭通道
	go func() {
		wg.Wait()
		close(resultChan)
		close(errChan)
	}()

	// 收集结果
	var allItems []HunhepanItem
	var errors []error

	// 从通道读取结果
	for items := range resultChan {
		allItems = append(allItems, items...)
	}

	// 收集错误（不阻止处理）
	for err := range errChan {
		errors = append(errors, err)
	}

	debugLog("收集到 %d 条原始结果，%d 个错误", len(allItems), len(errors))

	// 如果没有获取到任何结果且有错误，则返回第一个错误
	if len(allItems) == 0 && len(errors) > 0 {
		return nil, errors[0]
	}

	// 去重处理
	uniqueItems := p.deduplicateItems(allItems)
	debugLog("去重后剩余 %d 条结果", len(uniqueItems))

	// 转换为标准格式
	results := p.convertResults(uniqueItems)
	debugLog("转换后得到 %d 条最终结果", len(results))

	return results, nil
}

// searchAPI 向单个API发送请求
func (p *HunhepanAsyncPlugin) searchAPI(client *http.Client, endpoint searchEndpoint, keyword string) ([]HunhepanItem, error) {
	maxPages := 3 // 最多获取3页数据，可以根据需要调整

	// 创建结果通道和错误通道
	resultChan := make(chan []HunhepanItem, maxPages)
	errChan := make(chan error, maxPages)

	// 创建等待组，用于等待所有页面请求完成
	var wg sync.WaitGroup

	// 并发请求每一页
	for page := 1; page <= maxPages; page++ {
		wg.Add(1)

		go func(pageNum int) {
			defer wg.Done()

			// 构建请求体 - 根据1.txt的实际请求格式
			reqBody := map[string]interface{}{
				"page":         pageNum,
				"q":            keyword,
				"user":         "",
				"exact":        false,
				"format":       []string{},
				"share_time":   "",
				"size":         DefaultPageSize,
				"type":         "",
				"exclude_user": []string{},
				"adv_params": map[string]interface{}{
					"wechat_pwd": "",
					"platform":   "pc",
				},
			}

			jsonData, err := json.Marshal(reqBody)
			if err != nil {
				debugLog("序列化请求失败 (page %d): %v", pageNum, err)
				errChan <- fmt.Errorf("marshal request failed (page %d): %w", pageNum, err)
				return
			}

			debugLog("发送请求到 %s (page %d): %s", endpoint.apiURL, pageNum, string(jsonData))

			req, err := http.NewRequest("POST", endpoint.apiURL, bytes.NewBuffer(jsonData))
			if err != nil {
				debugLog("创建请求失败 (page %d): %v", pageNum, err)
				errChan <- fmt.Errorf("create request failed (page %d): %w", pageNum, err)
				return
			}

			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
			req.Header.Set("Accept", "application/json, text/plain, */*")
			req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
			req.Header.Set("Sec-Fetch-Dest", "empty")
			req.Header.Set("Sec-Fetch-Mode", "cors")
			req.Header.Set("Sec-Fetch-Site", "same-origin")

			if endpoint.referer != "" {
				req.Header.Set("Referer", endpoint.referer)
			}
			if endpoint.origin != "" {
				req.Header.Set("Origin", endpoint.origin)
			}

			// 发送请求
			var resp *http.Response
			var respErr error
			if endpoint.name == "hunhepan" && p.scraper != nil {
				resp, respErr = p.scraper.Post(endpoint.apiURL, "application/json", bytes.NewBuffer(jsonData))
			} else {
				resp, respErr = client.Do(req)
			}
			if respErr != nil {
				debugLog("请求失败 (page %d): %v", pageNum, respErr)
				errChan <- fmt.Errorf("request failed (page %d): %w", pageNum, respErr)
				return
			}
			defer resp.Body.Close()

			debugLog("收到响应 (page %d), 状态码: %d", pageNum, resp.StatusCode)

			// 读取响应体
			respBody, err := io.ReadAll(resp.Body)
			if err != nil {
				debugLog("读取响应失败 (page %d): %v", pageNum, err)
				errChan <- fmt.Errorf("read response body failed (page %d): %w", pageNum, err)
				return
			}

			debugLog("响应内容 (page %d, 前500字符): %s", pageNum, string(respBody[:min(500, len(respBody))]))

			// 解析响应
			var apiResp HunhepanResponse
			if err := json.Unmarshal(respBody, &apiResp); err != nil {
				debugLog("JSON解析失败 (page %d): %v", pageNum, err)
				errChan <- fmt.Errorf("decode response failed (page %d): %w", pageNum, err)
				return
			}

			// 检查响应状态
			if apiResp.Code != 200 {
				debugLog("API返回错误 (page %d): code=%d, msg=%s", pageNum, apiResp.Code, apiResp.Msg)
				errChan <- fmt.Errorf("API returned error (page %d): %s", pageNum, apiResp.Msg)
				return
			}

			debugLog("成功获取第 %d 页数据，共 %d 条结果", pageNum, len(apiResp.Data.List))

			// 将结果发送到通道
			resultChan <- apiResp.Data.List
		}(page)
	}

	// 启动一个goroutine等待所有页面请求完成并关闭通道
	go func() {
		wg.Wait()
		close(resultChan)
		close(errChan)
	}()

	// 收集结果
	var allItems []HunhepanItem
	for items := range resultChan {
		allItems = append(allItems, items...)
	}

	// 检查是否有错误
	var errors []error
	for err := range errChan {
		errors = append(errors, err)
	}

	// 如果没有获取到任何结果且有错误，则返回第一个错误
	if len(allItems) == 0 && len(errors) > 0 {
		return nil, errors[0]
	}

	return allItems, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// deduplicateItems 去重处理
func (p *HunhepanAsyncPlugin) deduplicateItems(items []HunhepanItem) []HunhepanItem {
	// 使用map进行去重
	uniqueMap := make(map[string]HunhepanItem)

	for _, item := range items {
		// 清理DiskName中的HTML标签
		cleanedName := cleanTitle(item.DiskName)
		item.DiskName = cleanedName

		// 创建复合键：优先使用DiskID，如果为空则使用Link+DiskName组合
		var key string
		if item.DiskID != "" {
			key = item.DiskID
		} else if item.Link != "" {
			// 使用Link和清理后的DiskName组合作为键
			key = item.Link + "|" + cleanedName
		} else {
			// 如果DiskID和Link都为空，则使用DiskName+DiskType作为键
			key = cleanedName + "|" + item.DiskType
		}

		// 如果已存在，保留信息更丰富的那个
		if existing, exists := uniqueMap[key]; exists {
			// 比较文件列表长度和其他信息
			existingScore := len(existing.Files)
			newScore := len(item.Files)

			// 如果新项有密码而现有项没有，增加新项分数
			if existing.DiskPass == "" && item.DiskPass != "" {
				newScore += 5
			}

			// 如果新项有时间而现有项没有，增加新项分数
			if existing.SharedTime == "" && item.SharedTime != "" {
				newScore += 3
			}

			if newScore > existingScore {
				uniqueMap[key] = item
			}
		} else {
			uniqueMap[key] = item
		}
	}

	// 将map转回切片
	result := make([]HunhepanItem, 0, len(uniqueMap))
	for _, item := range uniqueMap {
		result = append(result, item)
	}

	return result
}

// convertResults 将API响应转换为标准SearchResult格式
func (p *HunhepanAsyncPlugin) convertResults(items []HunhepanItem) []model.SearchResult {
	results := make([]model.SearchResult, 0, len(items))

	for i, item := range items {
		// 跳过无效链接的结果
		if item.Link == "" {
			debugLog("跳过无链接的结果: %s", item.DiskName)
			continue
		}

		// 创建链接
		link := model.Link{
			URL:      item.Link,
			Type:     p.convertDiskType(item.DiskType),
			Password: item.DiskPass,
		}

		// 创建唯一ID
		uniqueID := fmt.Sprintf("hunhepan-%s", item.DiskID)
		if item.DiskID == "" {
			// 使用索引作为后备
			uniqueID = fmt.Sprintf("hunhepan-%d-%d", time.Now().Unix(), i)
		}

		// 解析时间
		var datetime time.Time
		if item.SharedTime != "" {
			// 尝试解析时间，格式：2025-07-07 13:19:48
			parsedTime, err := time.Parse("2006-01-02 15:04:05", item.SharedTime)
			if err == nil {
				datetime = parsedTime
			} else {
				debugLog("时间解析失败: %s, err: %v", item.SharedTime, err)
			}
		}

		// 如果时间解析失败，使用零值
		if datetime.IsZero() {
			datetime = time.Time{}
		}

		// 创建搜索结果
		result := model.SearchResult{
			UniqueID: uniqueID,
			Title:    cleanTitle(item.DiskName),
			Content:  item.Files,
			Datetime: datetime,
			Links:    []model.Link{link},
			Channel:  "", // 插件搜索结果必须为空字符串
		}

		debugLog("转换结果: ID=%s, Title=%s, Type=%s, Link=%s", uniqueID, result.Title, link.Type, link.URL)
		results = append(results, result)
	}

	return results
}

// convertDiskType 将API的网盘类型转换为标准链接类型
func (p *HunhepanAsyncPlugin) convertDiskType(diskType string) string {
	switch diskType {
	case "BDY":
		return "baidu"
	case "ALY":
		return "aliyun"
	case "QUARK":
		return "quark"
	case "TIANYI":
		return "tianyi"
	case "UC":
		return "uc"
	case "CAIYUN":
		return "mobile"
	case "115":
		return "115"
	case "XUNLEI":
		return "xunlei"
	case "123PAN":
		return "123"
	case "PIKPAK":
		return "pikpak"
	default:
		return "others"
	}
}

// cleanTitle 清理标题中的HTML标签
func cleanTitle(title string) string {
	// 一次性替换所有常见HTML标签
	replacements := map[string]string{
		"<em>":      "",
		"</em>":     "",
		"<b>":       "",
		"</b>":      "",
		"<strong>":  "",
		"</strong>": "",
		"<i>":       "",
		"</i>":      "",
	}

	result := title
	for tag, replacement := range replacements {
		result = strings.Replace(result, tag, replacement, -1)
	}

	// 移除多余的空格
	return strings.TrimSpace(result)
}

// HunhepanResponse API响应结构
type HunhepanResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Total   int            `json:"total"`
		PerSize int            `json:"per_size"`
		List    []HunhepanItem `json:"list"`
	} `json:"data"`
}

// HunhepanItem API响应中的单个结果项
type HunhepanItem struct {
	DiskID     string `json:"disk_id"`
	DiskName   string `json:"disk_name"`
	DiskPass   string `json:"disk_pass"`
	DiskType   string `json:"disk_type"`
	Files      string `json:"files"`
	DocID      string `json:"doc_id"`
	ShareUser  string `json:"share_user"`
	SharedTime string `json:"shared_time"`
	Link       string `json:"link"`
	Enabled    bool   `json:"enabled"`
	Weight     int    `json:"weight"`
	Status     int    `json:"status"`
}
