package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"pansou/util"
)

var (
	ErrNotConfigured     = errors.New("mihomo controller is not configured")
	ErrGroupNotManaged   = errors.New("mihomo group is not managed")
	ErrInvalidSelection  = errors.New("mihomo selection is invalid")
	ErrLatencyTestActive = errors.New("mihomo latency test is already running")
)

const maxControllerResponseBytes = 8 << 20

type Config struct {
	ControllerURL string
	Secret        string
	ManagedGroups []string
	ProxyURL      string
	ConfigPath    string
	ReloadPath    string
	ExitInfoURL   string
	Timeout       time.Duration
	DelayTestURL  string
	DelayTimeout  time.Duration
}

type Service struct {
	controllerURL    *url.URL
	secret           string
	managedGroups    []string
	managedGroupSet  map[string]struct{}
	controllerClient *http.Client
	exitClient       *http.Client
	exitInfoURL      string
	configPath       string
	reloadPath       string
	metadataPath     string
	delayTestURL     string
	delayTimeout     time.Duration
	now              func() time.Time

	cacheMu        sync.Mutex
	exitCache      cachedExit
	latencyMu      sync.RWMutex
	latencyRunning bool
	lastDelays     map[string]int
	lastLatency    *LatencyTestSummary

	subscriptionMu      sync.Mutex
	subscriptionStateMu sync.RWMutex
	subscriptionUpdates map[string]bool
	subscriptionErrors  map[string]string
}

type cachedExit struct {
	key       string
	expiresAt time.Time
	value     ExitInfo
}

type Overview struct {
	Configured     bool                `json:"configured"`
	Available      bool                `json:"available"`
	CheckedAt      time.Time           `json:"checked_at"`
	Mode           string              `json:"mode"`
	MixedPort      int                 `json:"mixed_port"`
	GlobalCurrent  string              `json:"global_current,omitempty"`
	Group          GroupOverview       `json:"group"`
	Route          []RouteStep         `json:"route"`
	Exit           ExitInfo            `json:"exit"`
	LatencyTesting bool                `json:"latency_testing"`
	LatencyTest    *LatencyTestSummary `json:"latency_test,omitempty"`
}

type GroupOverview struct {
	Name          string      `json:"name"`
	Type          string      `json:"type"`
	Current       string      `json:"current"`
	EffectiveNode string      `json:"effective_node"`
	Candidates    []Candidate `json:"candidates"`
}

type Candidate struct {
	Name          string `json:"name"`
	Type          string `json:"type"`
	Region        string `json:"region"`
	CountryCode   string `json:"country_code,omitempty"`
	Alive         bool   `json:"alive"`
	DelayMS       int    `json:"delay_ms"`
	Selected      bool   `json:"selected"`
	Effective     bool   `json:"effective"`
	Automatic     bool   `json:"automatic"`
	EffectiveNode string `json:"effective_node,omitempty"`
}

type RouteStep struct {
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
}

type ExitInfo struct {
	IP          string    `json:"ip,omitempty"`
	CountryCode string    `json:"country_code,omitempty"`
	Country     string    `json:"country,omitempty"`
	Region      string    `json:"region,omitempty"`
	City        string    `json:"city,omitempty"`
	Org         string    `json:"org,omitempty"`
	Source      string    `json:"source,omitempty"`
	CheckedAt   time.Time `json:"checked_at,omitempty"`
	Error       string    `json:"error,omitempty"`
}

type LatencyTestSummary struct {
	Group       string    `json:"group"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	DurationMS  int64     `json:"duration_ms"`
	Total       int       `json:"total"`
	Succeeded   int       `json:"succeeded"`
	Failed      int       `json:"failed"`
}

type LatencyTestResponse struct {
	Overview Overview           `json:"overview"`
	Summary  LatencyTestSummary `json:"summary"`
}

type controllerConfig struct {
	Mode      string `json:"mode"`
	MixedPort int    `json:"mixed-port"`
}

type controllerProxyList struct {
	Proxies map[string]controllerProxy `json:"proxies"`
}

type controllerProxy struct {
	Name    string              `json:"name"`
	Type    string              `json:"type"`
	Now     string              `json:"now"`
	All     []string            `json:"all"`
	Alive   bool                `json:"alive"`
	History []controllerHistory `json:"history"`
}

type controllerHistory struct {
	Delay int `json:"delay"`
}

type exitInfoPayload struct {
	IP      string `json:"ip"`
	City    string `json:"city"`
	Region  string `json:"region"`
	Country string `json:"country"`
	Org     string `json:"org"`
}

func NewService(cfg Config) (*Service, error) {
	rawURL := strings.TrimSpace(cfg.ControllerURL)
	if rawURL == "" {
		return nil, nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("invalid mihomo controller URL")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.DelayTimeout <= 0 {
		cfg.DelayTimeout = 6 * time.Second
	}
	if strings.TrimSpace(cfg.DelayTestURL) == "" {
		cfg.DelayTestURL = "http://www.gstatic.com/generate_204"
	}
	groups := uniqueNonEmpty(cfg.ManagedGroups)
	if len(groups) == 0 {
		groups = []string{"良心云"}
	}
	exitClient, err := util.NewHTTPClient(strings.TrimSpace(cfg.ProxyURL))
	if err != nil {
		return nil, fmt.Errorf("create mihomo exit client: %w", err)
	}
	exitClient.Timeout = cfg.Timeout
	groupSet := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		groupSet[group] = struct{}{}
	}
	return &Service{
		controllerURL:       parsed,
		secret:              strings.TrimSpace(cfg.Secret),
		managedGroups:       groups,
		managedGroupSet:     groupSet,
		controllerClient:    &http.Client{Timeout: cfg.Timeout},
		exitClient:          exitClient,
		exitInfoURL:         strings.TrimSpace(cfg.ExitInfoURL),
		configPath:          strings.TrimSpace(cfg.ConfigPath),
		reloadPath:          strings.TrimSpace(cfg.ReloadPath),
		metadataPath:        subscriptionMetadataPath(strings.TrimSpace(cfg.ConfigPath)),
		delayTestURL:        strings.TrimSpace(cfg.DelayTestURL),
		delayTimeout:        cfg.DelayTimeout,
		now:                 time.Now,
		lastDelays:          make(map[string]int),
		subscriptionUpdates: make(map[string]bool),
		subscriptionErrors:  make(map[string]string),
	}, nil
}

func (s *Service) Overview(ctx context.Context, forceExitRefresh bool) (Overview, error) {
	if s == nil || s.controllerURL == nil {
		return Overview{}, ErrNotConfigured
	}
	var runtime controllerConfig
	if err := s.getJSON(ctx, []string{"configs"}, &runtime); err != nil {
		return Overview{}, err
	}
	proxies, err := s.fetchProxies(ctx)
	if err != nil {
		return Overview{}, err
	}
	groupName, group, ok := s.primaryGroup(proxies)
	if !ok {
		return Overview{}, fmt.Errorf("managed mihomo group was not found")
	}
	effective, path := resolveEffective(proxies, groupName)
	delays, latencySummary, latencyRunning := s.latencySnapshot()
	candidates := buildCandidates(proxies, group, effective, delays)
	exit := s.lookupExit(ctx, effective, forceExitRefresh)
	overview := Overview{
		Configured:     true,
		Available:      true,
		CheckedAt:      s.now().UTC(),
		Mode:           runtime.Mode,
		MixedPort:      runtime.MixedPort,
		Group:          GroupOverview{Name: groupName, Type: group.Type, Current: group.Now, EffectiveNode: effective, Candidates: candidates},
		Exit:           exit,
		LatencyTesting: latencyRunning,
		LatencyTest:    latencySummary,
	}
	if global, exists := proxies["GLOBAL"]; exists {
		overview.GlobalCurrent = global.Now
	}
	overview.Route = []RouteStep{
		{Kind: "application", Label: "PanSou", Detail: "采集与接口请求"},
		{Kind: "proxy", Label: "Mihomo", Detail: fmt.Sprintf("mixed-port %d", runtime.MixedPort)},
		{Kind: "rule", Label: "Telegram 规则", Detail: "优先于 GLOBAL"},
	}
	for index, name := range path {
		kind := "group"
		if index == len(path)-1 {
			kind = "node"
		}
		detail := ""
		if proxy, exists := proxies[name]; exists {
			detail = proxy.Type
		}
		overview.Route = append(overview.Route, RouteStep{Kind: kind, Label: name, Detail: detail})
	}
	return overview, nil
}

func (s *Service) TestLatency(ctx context.Context, groupName string) (LatencyTestResponse, error) {
	if s == nil || s.controllerURL == nil {
		return LatencyTestResponse{}, ErrNotConfigured
	}
	s.latencyMu.Lock()
	if s.latencyRunning {
		s.latencyMu.Unlock()
		return LatencyTestResponse{}, ErrLatencyTestActive
	}
	s.latencyRunning = true
	s.latencyMu.Unlock()
	defer func() {
		s.latencyMu.Lock()
		s.latencyRunning = false
		s.latencyMu.Unlock()
	}()

	proxies, err := s.fetchProxies(ctx)
	if err != nil {
		return LatencyTestResponse{}, err
	}
	groupName = strings.TrimSpace(groupName)
	var group controllerProxy
	var ok bool
	if groupName == "" {
		groupName, group, ok = s.primaryGroup(proxies)
	} else {
		_, managed := s.managedGroupSet[groupName]
		group, ok = proxies[groupName]
		ok = ok && managed && strings.EqualFold(group.Type, "Selector")
	}
	if !ok {
		return LatencyTestResponse{}, ErrGroupNotManaged
	}

	tested := make(map[string]int)
	for _, name := range group.All {
		if !isInformationalNode(name) {
			tested[name] = -1
		}
	}
	startedAt := s.now().UTC()
	var measured map[string]int
	testCtx, cancel := context.WithTimeout(ctx, s.delayTimeout+4*time.Second)
	defer cancel()
	query := url.Values{"url": []string{s.delayTestURL}, "timeout": []string{fmt.Sprintf("%d", s.delayTimeout.Milliseconds())}}
	if err := s.getJSONQuery(testCtx, []string{"group", groupName, "delay"}, query, s.delayTimeout+3*time.Second, &measured); err != nil {
		return LatencyTestResponse{}, err
	}
	for name, delay := range measured {
		if _, exists := tested[name]; exists && delay > 0 {
			tested[name] = delay
		}
	}
	succeeded := 0
	for _, delay := range tested {
		if delay > 0 {
			succeeded++
		}
	}
	completedAt := s.now().UTC()
	summary := LatencyTestSummary{Group: groupName, StartedAt: startedAt, CompletedAt: completedAt, DurationMS: completedAt.Sub(startedAt).Milliseconds(), Total: len(tested), Succeeded: succeeded, Failed: len(tested) - succeeded}
	s.latencyMu.Lock()
	s.lastDelays = tested
	s.lastLatency = &summary
	s.latencyRunning = false
	s.latencyMu.Unlock()

	overview, err := s.Overview(ctx, false)
	if err != nil {
		return LatencyTestResponse{}, err
	}
	return LatencyTestResponse{Overview: overview, Summary: summary}, nil
}

func (s *Service) Select(ctx context.Context, groupName, candidateName string) (Overview, error) {
	if s == nil || s.controllerURL == nil {
		return Overview{}, ErrNotConfigured
	}
	groupName = strings.TrimSpace(groupName)
	candidateName = strings.TrimSpace(candidateName)
	if _, ok := s.managedGroupSet[groupName]; !ok {
		return Overview{}, ErrGroupNotManaged
	}
	proxies, err := s.fetchProxies(ctx)
	if err != nil {
		return Overview{}, err
	}
	group, ok := proxies[groupName]
	if !ok || !strings.EqualFold(group.Type, "Selector") {
		return Overview{}, ErrGroupNotManaged
	}
	if candidateName == "" || isInformationalNode(candidateName) || !contains(group.All, candidateName) {
		return Overview{}, ErrInvalidSelection
	}
	if group.Now != candidateName {
		if err := s.putJSON(ctx, []string{"proxies", groupName}, map[string]string{"name": candidateName}); err != nil {
			return Overview{}, err
		}
	}
	s.cacheMu.Lock()
	s.exitCache = cachedExit{}
	s.cacheMu.Unlock()
	return s.Overview(ctx, true)
}

func (s *Service) primaryGroup(proxies map[string]controllerProxy) (string, controllerProxy, bool) {
	for _, name := range s.managedGroups {
		if group, ok := proxies[name]; ok && strings.EqualFold(group.Type, "Selector") {
			return name, group, true
		}
	}
	return "", controllerProxy{}, false
}

func (s *Service) fetchProxies(ctx context.Context) (map[string]controllerProxy, error) {
	var payload controllerProxyList
	if err := s.getJSON(ctx, []string{"proxies"}, &payload); err != nil {
		return nil, err
	}
	if payload.Proxies == nil {
		return nil, fmt.Errorf("mihomo controller returned no proxies")
	}
	return payload.Proxies, nil
}

func (s *Service) getJSON(ctx context.Context, path []string, destination any) error {
	return s.getJSONQuery(ctx, path, nil, s.controllerClient.Timeout, destination)
}

func (s *Service) getJSONQuery(ctx context.Context, path []string, query url.Values, timeout time.Duration, destination any) error {
	request, err := s.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if len(query) > 0 {
		request.URL.RawQuery = query.Encode()
	}
	client := *s.controllerClient
	if timeout > 0 {
		client.Timeout = timeout
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("mihomo controller request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("mihomo controller returned HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxControllerResponseBytes))
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode mihomo controller response: %w", err)
	}
	return nil
}

func (s *Service) latencySnapshot() (map[string]int, *LatencyTestSummary, bool) {
	s.latencyMu.RLock()
	defer s.latencyMu.RUnlock()
	delays := make(map[string]int, len(s.lastDelays))
	for name, delay := range s.lastDelays {
		delays[name] = delay
	}
	var summary *LatencyTestSummary
	if s.lastLatency != nil {
		copyValue := *s.lastLatency
		summary = &copyValue
	}
	return delays, summary, s.latencyRunning
}

func (s *Service) putJSON(ctx context.Context, path []string, payload any) error {
	return s.putJSONQuery(ctx, path, nil, payload)
}

func (s *Service) putJSONQuery(ctx context.Context, path []string, query url.Values, payload any) error {
	request, err := s.newRequest(ctx, http.MethodPut, path, payload)
	if err != nil {
		return err
	}
	if len(query) > 0 {
		request.URL.RawQuery = query.Encode()
	}
	response, err := s.controllerClient.Do(request)
	if err != nil {
		return fmt.Errorf("mihomo controller update failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("mihomo controller update returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (s *Service) newRequest(ctx context.Context, method string, path []string, payload any) (*http.Request, error) {
	parts := append([]string{s.controllerURL.String()}, path...)
	endpoint, err := url.JoinPath(parts[0], parts[1:]...)
	if err != nil {
		return nil, fmt.Errorf("build mihomo controller URL: %w", err)
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = strings.NewReader(string(encoded))
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if s.secret != "" {
		request.Header.Set("Authorization", "Bearer "+s.secret)
	}
	return request, nil
}

func (s *Service) lookupExit(ctx context.Context, effectiveNode string, force bool) ExitInfo {
	now := s.now().UTC()
	inferredRegion, inferredCode := inferRegion(effectiveNode)
	if s.exitInfoURL == "" {
		return ExitInfo{Region: inferredRegion, CountryCode: inferredCode, Country: countryName(inferredCode), Source: "node_name", CheckedAt: now}
	}
	s.cacheMu.Lock()
	if !force && s.exitCache.key == effectiveNode && now.Before(s.exitCache.expiresAt) {
		cached := s.exitCache.value
		s.cacheMu.Unlock()
		return cached
	}
	s.cacheMu.Unlock()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.exitInfoURL, nil)
	if err != nil {
		return ExitInfo{Region: inferredRegion, CountryCode: inferredCode, Country: countryName(inferredCode), Source: "node_name", CheckedAt: now, Error: "出口定位暂不可用"}
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "PanSou-Mihomo-Monitor/1.0")
	response, err := s.exitClient.Do(request)
	if err != nil {
		return ExitInfo{Region: inferredRegion, CountryCode: inferredCode, Country: countryName(inferredCode), Source: "node_name", CheckedAt: now, Error: "出口定位暂不可用"}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ExitInfo{Region: inferredRegion, CountryCode: inferredCode, Country: countryName(inferredCode), Source: "node_name", CheckedAt: now, Error: "出口定位暂不可用"}
	}
	var payload exitInfoPayload
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return ExitInfo{Region: inferredRegion, CountryCode: inferredCode, Country: countryName(inferredCode), Source: "node_name", CheckedAt: now, Error: "出口定位暂不可用"}
	}
	code := strings.ToUpper(strings.TrimSpace(payload.Country))
	value := ExitInfo{IP: strings.TrimSpace(payload.IP), CountryCode: code, Country: countryName(code), Region: strings.TrimSpace(payload.Region), City: strings.TrimSpace(payload.City), Org: strings.TrimSpace(payload.Org), Source: "ipinfo", CheckedAt: now}
	if value.Region == "" {
		value.Region = inferredRegion
	}
	if value.Country == "" {
		value.Country = countryName(inferredCode)
	}
	if value.CountryCode == "" {
		value.CountryCode = inferredCode
	}
	s.cacheMu.Lock()
	s.exitCache = cachedExit{key: effectiveNode, expiresAt: now.Add(2 * time.Minute), value: value}
	s.cacheMu.Unlock()
	return value
}

func resolveEffective(proxies map[string]controllerProxy, start string) (string, []string) {
	current := start
	path := make([]string, 0, 6)
	seen := make(map[string]struct{})
	for len(path) < 10 && current != "" {
		if _, exists := seen[current]; exists {
			break
		}
		seen[current] = struct{}{}
		path = append(path, current)
		proxy, exists := proxies[current]
		if !exists || strings.TrimSpace(proxy.Now) == "" || proxy.Now == current {
			break
		}
		current = proxy.Now
	}
	if len(path) == 0 {
		return start, []string{start}
	}
	return path[len(path)-1], path
}

func buildCandidates(proxies map[string]controllerProxy, group controllerProxy, effective string, measured map[string]int) []Candidate {
	items := make([]Candidate, 0, len(group.All))
	seen := make(map[string]struct{}, len(group.All))
	for _, name := range group.All {
		if isInformationalNode(name) {
			continue
		}
		proxy := proxies[name]
		key := strings.ToLower(strings.TrimSpace(proxy.Type)) + "\x00" + strings.TrimSpace(name)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		resolved, _ := resolveEffective(proxies, name)
		region, code := inferRegion(name)
		delay := latestDelay(proxy)
		if measuredDelay, exists := measured[name]; exists {
			delay = measuredDelay
			if delay < 0 {
				delay = 0
			}
		}
		candidate := Candidate{
			Name: name, Type: proxy.Type, Region: region, CountryCode: code,
			Alive: proxy.Alive, DelayMS: delay, Selected: name == group.Now,
			Effective: name == effective, Automatic: isPolicyType(proxy.Type), EffectiveNode: resolved,
		}
		if candidate.Region == "" && candidate.Automatic {
			candidate.Region = "策略组"
		}
		if candidate.Type == "" {
			candidate.Type = "Proxy"
		}
		items = append(items, candidate)
	}
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i].DelayMS, items[j].DelayMS
		if left > 0 && right <= 0 {
			return true
		}
		if left <= 0 && right > 0 {
			return false
		}
		if left != right {
			return left < right
		}
		if items[i].Selected != items[j].Selected {
			return items[i].Selected
		}
		return items[i].Name < items[j].Name
	})
	return items
}

func latestDelay(proxy controllerProxy) int {
	for index := len(proxy.History) - 1; index >= 0; index-- {
		if proxy.History[index].Delay > 0 {
			return proxy.History[index].Delay
		}
	}
	return 0
}

func isPolicyType(proxyType string) bool {
	switch strings.ToLower(strings.TrimSpace(proxyType)) {
	case "selector", "urltest", "fallback", "loadbalance", "relay":
		return true
	default:
		return false
	}
}

func isInformationalNode(name string) bool {
	for _, marker := range []string{"剩余流量", "套餐到期", "到期时间", "官网地址", "订阅到期"} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

func inferRegion(name string) (string, string) {
	regions := []struct {
		markers    []string
		name, code string
	}{
		{[]string{"香港", "🇭🇰"}, "香港", "HK"},
		{[]string{"新加坡", "狮城", "🇸🇬"}, "新加坡", "SG"},
		{[]string{"日本", "东京", "大阪", "🇯🇵"}, "日本", "JP"},
		{[]string{"美国", "洛杉矶", "西雅图", "🇺🇸"}, "美国", "US"},
		{[]string{"英国", "伦敦", "🇬🇧"}, "英国", "GB"},
		{[]string{"韩国", "首尔", "🇰🇷"}, "韩国", "KR"},
		{[]string{"台湾", "台北", "🇹🇼", "🇨🇳台湾"}, "台湾", "TW"},
	}
	for _, region := range regions {
		for _, marker := range region.markers {
			if strings.Contains(name, marker) {
				return region.name, region.code
			}
		}
	}
	return "", ""
}

func countryName(code string) string {
	return map[string]string{"HK": "中国香港", "SG": "新加坡", "JP": "日本", "US": "美国", "GB": "英国", "KR": "韩国", "TW": "中国台湾", "CN": "中国"}[strings.ToUpper(code)]
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
