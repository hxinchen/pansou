package fourkhd

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"pansou/model"
	"pansou/plugin"
	jsonutil "pansou/util/json"
)

const (
	pluginName        = "4khd"
	apiKeyEnv         = "FOURKHD_API_KEY"
	defaultAPIURL     = "https://api.4khd.top/api/search"
	defaultPriority   = 3
	requestTimeout    = 20 * time.Second
	requestInterval   = 1100 * time.Millisecond
	maximumBodyBytes  = 8 << 20
	cacheTTL          = time.Hour
	maximumCacheKeys  = 512
	supportedPanQuery = "quark,baidu,tianyiyun,aliyun,uc,115,123,pikpak,xunlei,magnet,ed2k"
)

var sharedRequestGate = newRateGate(requestInterval)

type rateGate struct {
	turn     chan struct{}
	interval time.Duration
	next     time.Time
}

func newRateGate(interval time.Duration) *rateGate {
	gate := &rateGate{turn: make(chan struct{}, 1), interval: interval}
	gate.turn <- struct{}{}
	return gate
}

func (g *rateGate) Wait(ctx context.Context) error {
	if g == nil || g.interval <= 0 {
		return nil
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g.turn:
	}
	defer func() { g.turn <- struct{}{} }()

	delay := time.Until(g.next)
	if delay <= 0 {
		g.next = time.Now().Add(g.interval)
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		g.next = time.Now().Add(g.interval)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type Plugin struct {
	*plugin.BaseAsyncPlugin
	client         *http.Client
	apiURL         string
	apiKeyProvider func() string
	gate           *rateGate
	cacheMu        sync.Mutex
	cache          map[string]cachedSearch
	inflight       singleflight.Group
}

var _ plugin.AsyncSearchPlugin = (*Plugin)(nil)

type cachedSearch struct {
	results   []model.SearchResult
	timestamp time.Time
}

type apiResponse struct {
	Code    int       `json:"code"`
	Message string    `json:"message"`
	Data    []apiItem `json:"data"`
	Total   int       `json:"total"`
}

type apiItem struct {
	Title      string `json:"title"`
	URL        string `json:"url"`
	Type       string `json:"is_type"`
	MessageAt  string `json:"msg_date"`
	IsExternal bool   `json:"is_external"`
}

func init() {
	plugin.RegisterGlobalPluginFactory(pluginName, func() plugin.AsyncSearchPlugin {
		return New()
	})
}

func New() *Plugin {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 4
	transport.MaxConnsPerHost = 4
	return &Plugin{
		BaseAsyncPlugin: plugin.NewBaseAsyncPlugin(pluginName, defaultPriority),
		client:          &http.Client{Transport: transport, Timeout: requestTimeout},
		apiURL:          defaultAPIURL,
		apiKeyProvider:  func() string { return os.Getenv(apiKeyEnv) },
		gate:            sharedRequestGate,
		cache:           make(map[string]cachedSearch),
	}
}

func (p *Plugin) Search(keyword string, ext map[string]interface{}) ([]model.SearchResult, error) {
	result, err := p.SearchWithResult(keyword, ext)
	if err != nil {
		return nil, err
	}
	return result.Results, nil
}

func (p *Plugin) SearchWithResult(keyword string, ext map[string]interface{}) (model.PluginSearchResult, error) {
	keyword = strings.TrimSpace(keyword)
	forceRefresh := ext != nil && ext["refresh"] == true
	if !forceRefresh {
		if cached, ok := p.loadCached(keyword); ok {
			return model.PluginSearchResult{Results: cached.results, IsFinal: true, Timestamp: cached.timestamp, Source: pluginName, Message: "从缓存获取"}, nil
		}
	}

	value, err, _ := p.inflight.Do(keyword, func() (any, error) {
		if !forceRefresh {
			if cached, ok := p.loadCached(keyword); ok {
				return cached, nil
			}
		}
		results, searchErr := p.searchImpl(nil, keyword, ext)
		if searchErr != nil {
			return cachedSearch{}, searchErr
		}
		cached := cachedSearch{results: append([]model.SearchResult(nil), results...), timestamp: time.Now()}
		p.storeCached(keyword, cached)
		return cached, nil
	})
	if err != nil {
		return model.PluginSearchResult{}, err
	}
	cached := value.(cachedSearch)
	return model.PluginSearchResult{Results: append([]model.SearchResult(nil), cached.results...), IsFinal: true, Timestamp: cached.timestamp, Source: pluginName, Message: "搜索完成"}, nil
}

func (p *Plugin) loadCached(keyword string) (cachedSearch, bool) {
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	cached, ok := p.cache[keyword]
	if !ok || time.Since(cached.timestamp) >= cacheTTL {
		if ok {
			delete(p.cache, keyword)
		}
		return cachedSearch{}, false
	}
	cached.results = append([]model.SearchResult(nil), cached.results...)
	return cached, true
}

func (p *Plugin) storeCached(keyword string, cached cachedSearch) {
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	if len(p.cache) >= maximumCacheKeys {
		oldestKey := ""
		oldestTime := time.Now()
		for key, entry := range p.cache {
			if entry.timestamp.Before(oldestTime) {
				oldestKey, oldestTime = key, entry.timestamp
			}
		}
		if oldestKey != "" {
			delete(p.cache, oldestKey)
		}
	}
	p.cache[keyword] = cached
}

func (p *Plugin) searchImpl(client *http.Client, keyword string, _ map[string]interface{}) ([]model.SearchResult, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, errors.New("[4khd] keyword is empty")
	}
	apiKey := strings.TrimSpace(p.apiKeyProvider())
	if apiKey == "" {
		return nil, fmt.Errorf("[%s] %s is not configured", pluginName, apiKeyEnv)
	}
	if p.client != nil {
		client = p.client
	}
	if client == nil {
		client = http.DefaultClient
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	if err := p.gate.Wait(ctx); err != nil {
		return nil, fmt.Errorf("[%s] rate-limit wait ended: %v", pluginName, err)
	}

	endpoint, err := url.Parse(p.apiURL)
	if err != nil {
		return nil, fmt.Errorf("[%s] invalid API endpoint", pluginName)
	}
	query := endpoint.Query()
	query.Set("api_key", apiKey)
	query.Set("q", keyword)
	query.Set("pan", supportedPanQuery)
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("[%s] could not create request", pluginName)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "PanSou/4khd")

	resp, err := client.Do(req)
	if err != nil {
		// net/http errors can contain the full URL, including api_key.
		return nil, fmt.Errorf("[%s] upstream request failed", pluginName)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("[%s] upstream rate limited the request (HTTP 429)", pluginName)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("[%s] upstream returned HTTP %d", pluginName, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maximumBodyBytes+1))
	if err != nil || len(body) > maximumBodyBytes {
		return nil, fmt.Errorf("[%s] invalid upstream response", pluginName)
	}
	var payload apiResponse
	if err := jsonutil.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("[%s] invalid upstream response", pluginName)
	}
	if payload.Code != http.StatusOK {
		return nil, fmt.Errorf("[%s] upstream returned code %d", pluginName, payload.Code)
	}

	return plugin.FilterResultsByKeyword(convertResults(payload.Data), keyword), nil
}

func convertResults(items []apiItem) []model.SearchResult {
	results := make([]model.SearchResult, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		title := strings.TrimSpace(item.Title)
		rawURL := strings.TrimSpace(item.URL)
		linkType, ok := normalizeLinkType(item.Type, rawURL)
		if title == "" || !ok {
			continue
		}
		if _, exists := seen[rawURL]; exists {
			continue
		}
		seen[rawURL] = struct{}{}

		messageAt, _ := time.Parse("2006-01-02 15:04:05", strings.TrimSpace(item.MessageAt))
		digest := sha256.Sum256([]byte(rawURL))
		results = append(results, model.SearchResult{
			UniqueID:  fmt.Sprintf("%s-%x", pluginName, digest[:12]),
			Datetime:  messageAt,
			Title:     title,
			Content:   "来源: 4KHD",
			SubSource: pluginName,
			Tags:      []string{linkType},
			Links: []model.Link{{
				Type:     linkType,
				URL:      rawURL,
				Password: extractPassword(rawURL),
				Datetime: messageAt,
			}},
		})
	}
	return results
}

func normalizeLinkType(declaredType, rawURL string) (string, bool) {
	lowerURL := strings.ToLower(strings.TrimSpace(rawURL))
	actualType := ""
	switch {
	case strings.HasPrefix(lowerURL, "magnet:?xt=urn:btih:"):
		actualType = "magnet"
	case strings.HasPrefix(lowerURL, "ed2k://|file|"):
		actualType = "ed2k"
	}

	if actualType != "" {
		declared := normalizeDeclaredType(declaredType)
		return actualType, declared == "" || declared == actualType
	}

	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	switch {
	case hostMatches(host, "pan.quark.cn"):
		actualType = "quark"
	case hostMatches(host, "pan.baidu.com"):
		actualType = "baidu"
	case hostMatches(host, "aliyundrive.com", "alipan.com"):
		actualType = "aliyun"
	case hostMatches(host, "drive.uc.cn", "pan.uc.cn"):
		actualType = "uc"
	case hostMatches(host, "cloud.189.cn", "content.21cn.com"):
		actualType = "tianyi"
	case hostMatches(host, "115.com", "115cdn.com", "anxia.com"):
		actualType = "115"
	case hostMatches(host, "123pan.com", "123pan.cn", "123684.com", "123685.com", "123865.com", "123912.com", "123592.com"):
		actualType = "123"
	case hostMatches(host, "mypikpak.com", "pikpak.com"):
		actualType = "pikpak"
	case hostMatches(host, "pan.xunlei.com"):
		actualType = "xunlei"
	default:
		return "", false
	}
	declared := normalizeDeclaredType(declaredType)
	if declared != "" && declared != actualType {
		return "", false
	}
	return actualType, true
}

func normalizeDeclaredType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "quark":
		return "quark"
	case "baidu":
		return "baidu"
	case "aliyun", "alipan":
		return "aliyun"
	case "uc":
		return "uc"
	case "tianyiyun", "tianyi":
		return "tianyi"
	case "115":
		return "115"
	case "123", "123pan":
		return "123"
	case "pikpak":
		return "pikpak"
	case "xunlei":
		return "xunlei"
	case "magnet":
		return "magnet"
	case "ed2k":
		return "ed2k"
	default:
		return ""
	}
}

func hostMatches(host string, domains ...string) bool {
	for _, domain := range domains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

func extractPassword(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	for _, key := range []string{"pwd", "code", "passcode"} {
		if value := strings.TrimSpace(parsed.Query().Get(key)); value != "" {
			return value
		}
	}
	return ""
}
