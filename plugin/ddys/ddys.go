package ddys

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	cloudscraper "github.com/Advik-B/cloudscraper/lib"
	"github.com/PuerkitoBio/goquery"
	"pansou/model"
	"pansou/plugin"
)

const (
	PluginName     = "ddys"
	DisplayName    = "低端影视"
	Description    = "低端影视 - 影视资源网盘链接搜索"
	BaseURL        = "https://ddys.io"
	SearchPath     = "/api/v1/search?q=%s&type=movie&per_page=5"
	UserAgent      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36"
	MaxResults     = 50
	MaxConcurrency = 4
	DetailTimeout  = 8 * time.Second
)

type ddysSearchResponse struct {
	Success bool `json:"success"`
	Data    []struct {
		ID       int    `json:"id"`
		Title    string `json:"title"`
		Aka      string `json:"aka"`
		Slug     string `json:"slug"`
		Year     int    `json:"year"`
		Type     string `json:"type"`
		TypeCode string `json:"type_code"`
		Region   string `json:"region"`
		URL      string `json:"url"`
	} `json:"data"`
	Message string `json:"message"`
}

type ddysSourcesResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Download []struct {
			ID           int    `json:"id"`
			Name         string `json:"name"`
			URL          string `json:"url"`
			Quality      string `json:"quality"`
			DownloadType string `json:"download_type"`
			Size         string `json:"size"`
			ExtractCode  string `json:"extract_code"`
		} `json:"download"`
	} `json:"data"`
	Message string `json:"message"`
}

// DdysPlugin 低端影视插件
type DdysPlugin struct {
	*plugin.BaseAsyncPlugin
	debugMode   bool
	detailCache sync.Map // 缓存详情页结果
	cacheTTL    time.Duration
	scraper     *cloudscraper.Scraper
}

// init 注册插件
func init() {
	plugin.RegisterGlobalPlugin(NewDdysPlugin())
}

// NewDdysPlugin 创建新的低端影视插件实例
func NewDdysPlugin() *DdysPlugin {
	debugMode := false // 生产环境关闭调试
	scraper, err := cloudscraper.New()
	if err != nil {
		log.Printf("[DDYS] cloudscraper 初始化失败: %v", err)
	}

	p := &DdysPlugin{
		BaseAsyncPlugin: plugin.NewBaseAsyncPlugin(PluginName, 1), // 标准网盘插件，启用Service层过滤
		debugMode:       debugMode,
		cacheTTL:        30 * time.Minute, // 详情页缓存30分钟
		scraper:         scraper,
	}

	return p
}

// Name 插件名称
func (p *DdysPlugin) Name() string {
	return PluginName
}

// DisplayName 插件显示名称
func (p *DdysPlugin) DisplayName() string {
	return DisplayName
}

// Description 插件描述
func (p *DdysPlugin) Description() string {
	return Description
}

// Search 搜索接口
func (p *DdysPlugin) Search(keyword string, ext map[string]interface{}) ([]model.SearchResult, error) {
	return p.searchImpl(&http.Client{Timeout: 30 * time.Second}, keyword, ext)
}

// searchImpl 搜索实现
func (p *DdysPlugin) searchImpl(client *http.Client, keyword string, ext map[string]interface{}) ([]model.SearchResult, error) {
	if p.debugMode {
		log.Printf("[DDYS] 开始搜索: %s", keyword)
	}

	// 第一步：执行搜索获取结果列表
	searchResults, err := p.executeSearch(client, keyword)
	if err != nil {
		return nil, fmt.Errorf("[%s] 执行搜索失败: %w", p.Name(), err)
	}

	if p.debugMode {
		log.Printf("[DDYS] 搜索获取到 %d 个结果", len(searchResults))
	}

	// 第二步：并发获取详情页链接
	finalResults := p.fetchDetailLinks(client, searchResults, keyword)

	if p.debugMode {
		log.Printf("[DDYS] 最终获取到 %d 个有效结果", len(finalResults))
	}

	// 第三步：关键词过滤（标准网盘插件需要过滤）
	filteredResults := plugin.FilterResultsByKeyword(finalResults, keyword)

	if p.debugMode {
		log.Printf("[DDYS] 关键词过滤后剩余 %d 个结果", len(filteredResults))
	}

	return filteredResults, nil
}

// executeSearch 执行搜索请求
func (p *DdysPlugin) executeSearch(client *http.Client, keyword string) ([]model.SearchResult, error) {
	return p.executeSuggestSearch(client, keyword)
}

func (p *DdysPlugin) executeSuggestSearch(client *http.Client, keyword string) ([]model.SearchResult, error) {
	searchURL := fmt.Sprintf(BaseURL+SearchPath, url.QueryEscape(keyword))
	respBody, err := p.fetchAPIJSON(client, searchURL, 4*time.Second)
	if err != nil {
		return nil, fmt.Errorf("[%s] 搜索API请求失败: %w", p.Name(), err)
	}

	var payload ddysSearchResponse
	if err := json.Unmarshal(respBody, &payload); err != nil {
		return nil, fmt.Errorf("[%s] 解析搜索API失败: %w", p.Name(), err)
	}
	if !payload.Success {
		if strings.TrimSpace(payload.Message) != "" {
			return nil, fmt.Errorf("[%s] 搜索API返回失败: %s", p.Name(), payload.Message)
		}
		return nil, fmt.Errorf("[%s] 搜索API返回失败", p.Name())
	}

	results := make([]model.SearchResult, 0, len(payload.Data))
	for index, item := range payload.Data {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			continue
		}
		displayTitle := title
		if item.Year > 0 {
			displayTitle = fmt.Sprintf("%s（%d）", title, item.Year)
		}
		detailURL := strings.TrimSpace(item.URL)
		if detailURL == "" && strings.TrimSpace(item.Slug) != "" {
			detailURL = "/movie/" + strings.TrimSpace(item.Slug)
		}
		if strings.HasPrefix(detailURL, "/") {
			detailURL = BaseURL + detailURL
		}
		if detailURL == "" {
			continue
		}
		contentParts := []string{
			fmt.Sprintf("类型：%s", strings.TrimSpace(item.Type)),
			fmt.Sprintf("详情页: %s", detailURL),
		}
		if strings.TrimSpace(item.Aka) != "" {
			contentParts = append(contentParts, "别名："+strings.TrimSpace(item.Aka))
		}
		if strings.TrimSpace(item.Region) != "" {
			contentParts = append(contentParts, "地区："+strings.TrimSpace(item.Region))
		}

		results = append(results, model.SearchResult{
			Title:     displayTitle,
			Content:   strings.Join(contentParts, "\n"),
			Channel:   "",
			MessageID: fmt.Sprintf("%s-suggest-%d", p.Name(), index+1),
			UniqueID:  fmt.Sprintf("%s-%s-%d", p.Name(), strings.TrimSpace(item.Slug), item.ID),
			Datetime:  time.Now(),
			Links:     []model.Link{},
			Tags:      []string{strings.TrimSpace(item.Type), strings.TrimSpace(item.TypeCode)},
		})
	}

	return results, nil
}

func (p *DdysPlugin) doGet(client *http.Client, req *http.Request) (*http.Response, error) {
	if p.scraper != nil {
		return p.scraper.Get(req.URL.String())
	}
	return client.Do(req)
}

func (p *DdysPlugin) fetchAPIJSON(client *http.Client, apiURL string, directTimeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), directTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("创建API请求失败: %w", err)
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Referer", BaseURL+"/")

	resp, err := client.Do(req)
	if err == nil && resp.StatusCode == http.StatusOK {
		defer resp.Body.Close()
		return io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	}
	if resp != nil {
		resp.Body.Close()
		err = fmt.Errorf("API HTTP状态错误: %d", resp.StatusCode)
	}

	body, chromeErr := ddysFetchWithChrome(apiURL, directTimeout+8*time.Second)
	if chromeErr != nil {
		if err != nil {
			return nil, fmt.Errorf("%w; Chrome兜底失败: %v", err, chromeErr)
		}
		return nil, chromeErr
	}

	return []byte(ddysExtractChromeResponse(body)), nil
}

func ddysFetchWithChrome(targetURL string, timeout time.Duration) (string, error) {
	candidates := ddysChromeCandidates()
	if len(candidates) == 0 {
		return "", fmt.Errorf("未找到Chrome可执行文件")
	}

	var lastErr error
	for _, chromePath := range candidates {
		userDataDir, err := os.MkdirTemp("", "pansou-ddys-chrome-*")
		if err != nil {
			return "", err
		}

		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		cmd := exec.CommandContext(ctx, chromePath,
			"--headless=new",
			"--disable-gpu",
			"--disable-extensions",
			"--disable-background-networking",
			"--disable-dev-shm-usage",
			"--no-sandbox",
			"--user-data-dir="+userDataDir,
			"--dump-dom",
			targetURL,
		)
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err = cmd.Run()
		cancel()
		_ = os.RemoveAll(userDataDir)

		body := stdout.String()
		if err == nil && strings.TrimSpace(body) != "" {
			return body, nil
		}
		if ctx.Err() != nil {
			lastErr = ctx.Err()
		} else if err != nil {
			lastErr = fmt.Errorf("%s: %w (%s)", chromePath, err, strings.TrimSpace(stderr.String()))
		} else {
			lastErr = fmt.Errorf("%s: Chrome输出为空", chromePath)
		}
	}

	return "", lastErr
}

func ddysExtractChromeResponse(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "{") || strings.HasPrefix(raw, "[") {
		return raw
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(raw))
	if err != nil {
		return raw
	}
	if pre := strings.TrimSpace(doc.Find("pre").First().Text()); pre != "" {
		return pre
	}
	return strings.TrimSpace(doc.Find("body").Text())
}

func ddysChromeCandidates() []string {
	var candidates []string
	seen := make(map[string]struct{})
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		candidates = append(candidates, path)
	}

	if envPath := os.Getenv("PANSOU_CHROME_PATH"); envPath != "" {
		if _, err := os.Stat(envPath); err == nil {
			add(envPath)
		}
	}

	for _, path := range []string{
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
	} {
		if _, err := os.Stat(path); err == nil {
			add(path)
		}
	}

	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome", "msedge"} {
		if path, err := exec.LookPath(name); err == nil {
			add(path)
		}
	}

	return candidates
}

// doRequestWithRetry 带重试机制的HTTP请求
func (p *DdysPlugin) doRequestWithRetry(req *http.Request, client *http.Client) (*http.Response, error) {
	maxRetries := 3
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		if i > 0 {
			// 指数退避重试
			backoff := time.Duration(1<<uint(i-1)) * 200 * time.Millisecond
			time.Sleep(backoff)
		}

		// 克隆请求避免并发问题
		reqClone := req.Clone(req.Context())

		resp, err := client.Do(reqClone)
		if err == nil && resp.StatusCode == 200 {
			return resp, nil
		}

		if resp != nil {
			resp.Body.Close()
		}
		lastErr = err
	}

	return nil, fmt.Errorf("[%s] 重试 %d 次后仍然失败: %w", p.Name(), maxRetries, lastErr)
}

// parseSearchResults 解析搜索结果HTML
func (p *DdysPlugin) parseSearchResults(doc *goquery.Document) ([]model.SearchResult, error) {
	var results []model.SearchResult

	// 查找搜索结果项: article[class^="post-"]
	doc.Find("article[class*='post-']").Each(func(i int, s *goquery.Selection) {
		if len(results) >= MaxResults {
			return
		}

		result := p.parseResultItem(s, i+1)
		if result != nil {
			results = append(results, *result)
		}
	})

	if p.debugMode {
		log.Printf("[DDYS] 解析到 %d 个原始结果", len(results))
	}

	return results, nil
}

// parseResultItem 解析单个搜索结果项
func (p *DdysPlugin) parseResultItem(s *goquery.Selection, index int) *model.SearchResult {
	// 提取文章ID
	articleClass, _ := s.Attr("class")
	postID := p.extractPostID(articleClass)
	if postID == "" {
		postID = fmt.Sprintf("unknown-%d", index)
	}

	// 提取标题和链接
	linkEl := s.Find(".post-title a")
	if linkEl.Length() == 0 {
		if p.debugMode {
			log.Printf("[DDYS] 跳过无标题链接的结果")
		}
		return nil
	}

	// 提取标题
	title := strings.TrimSpace(linkEl.Text())
	if title == "" {
		return nil
	}

	// 提取详情页链接
	detailURL, _ := linkEl.Attr("href")
	if detailURL == "" {
		if p.debugMode {
			log.Printf("[DDYS] 跳过无链接的结果: %s", title)
		}
		return nil
	}

	// 提取发布时间
	publishTime := p.extractPublishTime(s)

	// 提取分类
	category := p.extractCategory(s)

	// 提取简介
	content := p.extractContent(s)

	// 构建初始结果对象（详情页链接稍后获取）
	result := model.SearchResult{
		Title:     title,
		Content:   fmt.Sprintf("分类：%s\n%s", category, content),
		Channel:   "", // 插件搜索结果必须为空字符串（按开发指南要求）
		MessageID: fmt.Sprintf("%s-%s-%d", p.Name(), postID, index),
		UniqueID:  fmt.Sprintf("%s-%s-%d", p.Name(), postID, index),
		Datetime:  publishTime,
		Links:     []model.Link{}, // 先为空，详情页处理后添加
		Tags:      []string{category},
	}

	// 添加详情页URL到临时字段（用于后续处理）
	result.Content += fmt.Sprintf("\n详情页: %s", detailURL)

	if p.debugMode {
		log.Printf("[DDYS] 解析结果: %s (%s)", title, category)
	}

	return &result
}

// extractPostID 从文章class中提取文章ID
func (p *DdysPlugin) extractPostID(articleClass string) string {
	// 匹配 post-{数字} 格式
	re := regexp.MustCompile(`post-(\d+)`)
	matches := re.FindStringSubmatch(articleClass)
	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}

// extractPublishTime 提取发布时间
func (p *DdysPlugin) extractPublishTime(s *goquery.Selection) time.Time {
	timeEl := s.Find(".meta_date time.entry-date")
	if timeEl.Length() == 0 {
		return time.Now()
	}

	datetime, exists := timeEl.Attr("datetime")
	if !exists {
		return time.Now()
	}

	// 解析ISO 8601格式时间
	if t, err := time.Parse(time.RFC3339, datetime); err == nil {
		return t
	}

	return time.Now()
}

// extractCategory 提取分类
func (p *DdysPlugin) extractCategory(s *goquery.Selection) string {
	categoryEl := s.Find(".meta_categories .cat-links a")
	if categoryEl.Length() > 0 {
		return strings.TrimSpace(categoryEl.Text())
	}
	return "未分类"
}

// extractContent 提取内容简介
func (p *DdysPlugin) extractContent(s *goquery.Selection) string {
	contentEl := s.Find(".entry-content")
	if contentEl.Length() > 0 {
		content := strings.TrimSpace(contentEl.Text())
		// 限制长度
		if len(content) > 200 {
			content = content[:200] + "..."
		}
		return content
	}
	return ""
}

// fetchDetailLinks 并发获取详情页链接
func (p *DdysPlugin) fetchDetailLinks(client *http.Client, searchResults []model.SearchResult, keyword string) []model.SearchResult {
	if len(searchResults) == 0 {
		return []model.SearchResult{}
	}

	// 使用通道控制并发数
	semaphore := make(chan struct{}, MaxConcurrency)
	var wg sync.WaitGroup
	resultsChan := make(chan model.SearchResult, len(searchResults))

	for _, result := range searchResults {
		wg.Add(1)
		go func(r model.SearchResult) {
			defer wg.Done()
			semaphore <- struct{}{}        // 获取信号量
			defer func() { <-semaphore }() // 释放信号量

			// 从Content中提取详情页URL
			detailURL := p.extractDetailURLFromContent(r.Content)
			if detailURL == "" {
				if p.debugMode {
					log.Printf("[DDYS] 跳过无详情页URL的结果: %s", r.Title)
				}
				return
			}

			// 获取详情页链接
			links := p.fetchDetailPageLinks(client, detailURL)
			if len(links) > 0 {
				r.Links = links
				// 清理Content中的详情页URL
				r.Content = p.cleanContent(r.Content)
				resultsChan <- r
			} else if p.debugMode {
				log.Printf("[DDYS] 详情页无有效链接: %s", r.Title)
			}
		}(result)
	}

	// 等待所有goroutine完成
	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	// 收集结果
	var finalResults []model.SearchResult
	for result := range resultsChan {
		finalResults = append(finalResults, result)
	}

	return finalResults
}

// extractDetailURLFromContent 从Content中提取详情页URL
func (p *DdysPlugin) extractDetailURLFromContent(content string) string {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "详情页: ") {
			return strings.TrimPrefix(line, "详情页: ")
		}
	}
	return ""
}

// cleanContent 清理Content，移除详情页URL行
func (p *DdysPlugin) cleanContent(content string) string {
	lines := strings.Split(content, "\n")
	var cleanedLines []string
	for _, line := range lines {
		if !strings.HasPrefix(line, "详情页: ") {
			cleanedLines = append(cleanedLines, line)
		}
	}
	return strings.Join(cleanedLines, "\n")
}

// fetchDetailPageLinks 获取详情页的网盘链接
func (p *DdysPlugin) fetchDetailPageLinks(client *http.Client, detailURL string) []model.Link {
	// 检查缓存
	if cached, found := p.detailCache.Load(detailURL); found {
		if links, ok := cached.([]model.Link); ok {
			if p.debugMode {
				log.Printf("[DDYS] 使用缓存的详情页链接: %s", detailURL)
			}
			return links
		}
	}

	if links := p.fetchSourceAPILinks(client, detailURL); len(links) > 0 {
		p.detailCache.Store(detailURL, links)
		return links
	}

	// 创建带超时的上下文
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", detailURL, nil)
	if err != nil {
		if p.debugMode {
			log.Printf("[DDYS] 创建详情页请求失败: %v", err)
		}
		return []model.Link{}
	}

	// 设置请求头
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Referer", BaseURL+"/")

	resp, err := p.doGet(client, req)
	if err != nil {
		if p.debugMode {
			log.Printf("[DDYS] 详情页请求失败: %v", err)
		}
		return []model.Link{}
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		if p.debugMode {
			log.Printf("[DDYS] 详情页HTTP状态错误: %d", resp.StatusCode)
		}
		return []model.Link{}
	}

	// 读取响应体
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		if p.debugMode {
			log.Printf("[DDYS] 读取详情页响应失败: %v", err)
		}
		return []model.Link{}
	}

	// 解析网盘链接
	links := p.parseNetworkDiskLinks(string(body))

	// 缓存结果
	if len(links) > 0 {
		p.detailCache.Store(detailURL, links)
	}

	if p.debugMode {
		log.Printf("[DDYS] 从详情页提取到 %d 个链接: %s", len(links), detailURL)
	}

	return links
}

func (p *DdysPlugin) fetchSourceAPILinks(client *http.Client, detailURL string) []model.Link {
	slug := extractDdysSlug(detailURL)
	if slug == "" {
		return nil
	}

	apiURL := fmt.Sprintf("%s/api/v1/movies/%s/sources", BaseURL, url.PathEscape(slug))
	respBody, err := p.fetchAPIJSON(client, apiURL, 4*time.Second)
	if err != nil {
		if p.debugMode {
			log.Printf("[DDYS] sources API请求失败: %v", err)
		}
		return nil
	}

	var payload ddysSourcesResponse
	if err := json.Unmarshal(respBody, &payload); err != nil {
		if p.debugMode {
			log.Printf("[DDYS] 解析sources API失败: %v", err)
		}
		return nil
	}
	if !payload.Success {
		return nil
	}

	links := make([]model.Link, 0, len(payload.Data.Download))
	seen := make(map[string]struct{})
	for _, source := range payload.Data.Download {
		rawURL := strings.TrimSpace(source.URL)
		if rawURL == "" {
			continue
		}
		if _, ok := seen[rawURL]; ok {
			continue
		}
		seen[rawURL] = struct{}{}

		linkType := strings.TrimSpace(source.DownloadType)
		if linkType == "" {
			linkType = p.determineCloudType(rawURL)
		}
		if linkType == "" || linkType == "others" {
			linkType = p.determineCloudType(rawURL)
		}
		if linkType == "" || linkType == "others" {
			continue
		}

		link := model.Link{
			Type:      linkType,
			URL:       rawURL,
			Password:  strings.TrimSpace(source.ExtractCode),
			WorkTitle: strings.TrimSpace(source.Name),
		}
		if link.WorkTitle == "" {
			link.WorkTitle = strings.TrimSpace(source.Quality)
		}
		links = append(links, link)
	}

	return links
}

func extractDdysSlug(detailURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(detailURL))
	if err != nil {
		return ""
	}
	path := strings.Trim(parsed.Path, "/")
	if !strings.HasPrefix(path, "movie/") {
		return ""
	}
	return strings.Trim(strings.TrimPrefix(path, "movie/"), "/")
}

// parseNetworkDiskLinks 解析网盘链接
func (p *DdysPlugin) parseNetworkDiskLinks(htmlContent string) []model.Link {
	var links []model.Link

	// 定义网盘链接匹配模式
	patterns := []struct {
		name    string
		pattern string
		urlType string
	}{
		{"夸克网盘", `\(夸克[^)]*\)[：:]\s*<a[^>]*href\s*=\s*["']([^"']+)["'][^>]*>([^<]+)</a>`, "quark"},
		{"百度网盘", `\(百度[^)]*\)[：:]\s*<a[^>]*href\s*=\s*["']([^"']+)["'][^>]*>([^<]+)</a>`, "baidu"},
		{"阿里云盘", `\(阿里[^)]*\)[：:]\s*<a[^>]*href\s*=\s*["']([^"']+)["'][^>]*>([^<]+)</a>`, "aliyun"},
		{"天翼云盘", `\(天翼[^)]*\)[：:]\s*<a[^>]*href\s*=\s*["']([^"']+)["'][^>]*>([^<]+)</a>`, "tianyi"},
		{"迅雷网盘", `\(迅雷[^)]*\)[：:]\s*<a[^>]*href\s*=\s*["']([^"']+)["'][^>]*>([^<]+)</a>`, "xunlei"},
		// 通用模式
		{"通用网盘", `<a[^>]*href\s*=\s*["'](https?://[^"']*(?:pan|drive|cloud)[^"']*)["'][^>]*>([^<]+)</a>`, "others"},
	}

	// 去重用的map
	seen := make(map[string]bool)

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern.pattern)
		matches := re.FindAllStringSubmatch(htmlContent, -1)

		for _, match := range matches {
			if len(match) >= 3 {
				url := match[1]

				// 去重
				if seen[url] {
					continue
				}
				seen[url] = true

				// 确定网盘类型
				urlType := p.determineCloudType(url)
				if urlType == "others" {
					urlType = pattern.urlType
				}

				// 提取可能的提取码
				password := p.extractPassword(htmlContent, url)

				link := model.Link{
					Type:     urlType,
					URL:      url,
					Password: password,
				}

				links = append(links, link)

				if p.debugMode {
					log.Printf("[DDYS] 找到链接: %s (%s)", url, urlType)
				}
			}
		}
	}

	return links
}

// extractPassword 提取网盘提取码
func (p *DdysPlugin) extractPassword(content string, panURL string) string {
	// 常见提取码模式
	patterns := []string{
		`提取[码密][：:]?\s*([A-Za-z0-9]{4,8})`,
		`密码[：:]?\s*([A-Za-z0-9]{4,8})`,
		`[码密][：:]?\s*([A-Za-z0-9]{4,8})`,
		`([A-Za-z0-9]{4,8})\s*[是为]?提取[码密]`,
	}

	// 在网盘链接附近搜索提取码
	urlIndex := strings.Index(content, panURL)
	if urlIndex == -1 {
		return ""
	}

	// 搜索范围：链接前后200个字符
	start := urlIndex - 200
	if start < 0 {
		start = 0
	}
	end := urlIndex + len(panURL) + 200
	if end > len(content) {
		end = len(content)
	}

	searchArea := content[start:end]

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindStringSubmatch(searchArea)
		if len(matches) > 1 {
			return matches[1]
		}
	}

	return ""
}

// determineCloudType 根据URL自动识别网盘类型（按开发指南完整列表）
func (p *DdysPlugin) determineCloudType(url string) string {
	switch {
	case strings.Contains(url, "pan.quark.cn"):
		return "quark"
	case strings.Contains(url, "drive.uc.cn"):
		return "uc"
	case strings.Contains(url, "pan.baidu.com"):
		return "baidu"
	case strings.Contains(url, "aliyundrive.com") || strings.Contains(url, "alipan.com"):
		return "aliyun"
	case strings.Contains(url, "pan.xunlei.com"):
		return "xunlei"
	case strings.Contains(url, "cloud.189.cn"):
		return "tianyi"
	case strings.Contains(url, "caiyun.139.com"):
		return "mobile"
	case strings.Contains(url, "115.com"):
		return "115"
	case strings.Contains(url, "123pan.com"):
		return "123"
	case strings.Contains(url, "mypikpak.com"):
		return "pikpak"
	case strings.Contains(url, "lanzou"):
		return "lanzou"
	default:
		return "others"
	}
}
