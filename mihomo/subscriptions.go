package mihomo

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var (
	ErrSubscriptionsNotConfigured = errors.New("mihomo subscriptions are not configured")
	ErrSubscriptionNotFound       = errors.New("mihomo subscription was not found")
	ErrInvalidSubscription        = errors.New("mihomo subscription is invalid")
	ErrDuplicateSubscription      = errors.New("mihomo subscription already exists")
)

const (
	defaultSubscriptionInterval = 3600
	minSubscriptionInterval     = 300
	maxSubscriptionInterval     = 7 * 24 * 3600
	providerPrefix              = "pansou_"
	builtinSubscriptionID       = "builtin-baseline"
	fetchViaRule                = "rule"
	fetchViaDirect              = "direct"
	fetchViaBaseline            = "baseline"
	fetchViaAuto                = "auto"
	fetchViaFallback            = "fallback"
)

type SubscriptionInput struct {
	Name            string
	URL             string
	IntervalSeconds int
	FetchVia        string
}

type SubscriptionPatch struct {
	Name            *string
	URL             *string
	IntervalSeconds *int
	FetchVia        *string
}

type Subscription struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	URLMasked       string    `json:"url_masked"`
	Provider        string    `json:"provider"`
	Path            string    `json:"path,omitempty"`
	IntervalSeconds int       `json:"interval_seconds"`
	AutoUpdate      bool      `json:"auto_update"`
	NodeCount       int       `json:"node_count"`
	UniqueNodeCount int       `json:"unique_node_count"`
	DuplicateNodes  int       `json:"duplicate_node_count"`
	Status          string    `json:"status"`
	Error           string    `json:"error,omitempty"`
	Updating        bool      `json:"updating"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
	CreatedAt       time.Time `json:"created_at,omitempty"`
	Groups          []string  `json:"groups,omitempty"`
	FetchVia        string    `json:"fetch_via"`
	Builtin         bool      `json:"builtin"`
	Editable        bool      `json:"editable"`
}

type subscriptionMetadata struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Provider  string    `json:"provider"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type subscriptionMetadataFile struct {
	Subscriptions []subscriptionMetadata `json:"subscriptions"`
}

type subscriptionProviderConfig struct {
	Type        string `yaml:"type"`
	URL         string `yaml:"url"`
	Path        string `yaml:"path"`
	Interval    int    `yaml:"interval"`
	Proxy       string `yaml:"proxy,omitempty"`
	HealthCheck struct {
		Enable   bool   `yaml:"enable"`
		URL      string `yaml:"url"`
		Interval int    `yaml:"interval"`
	} `yaml:"health-check"`
}

type controllerProvidersResponse struct {
	Providers map[string]controllerProvider `json:"providers"`
}

type controllerProvider struct {
	Name        string            `json:"name"`
	VehicleType string            `json:"vehicleType"`
	UpdatedAt   string            `json:"updatedAt"`
	Proxies     []json.RawMessage `json:"proxies"`
}

func (s *Service) ListSubscriptions(ctx context.Context) ([]Subscription, error) {
	if !s.subscriptionsReady() {
		return nil, ErrSubscriptionsNotConfigured
	}
	s.subscriptionMu.Lock()
	defer s.subscriptionMu.Unlock()
	providers, groups, err := s.readSubscriptionConfig()
	if err != nil {
		return nil, err
	}
	metadata, err := s.readSubscriptionMetadata()
	if err != nil {
		return nil, err
	}
	controllerProviders, controllerErr := s.fetchControllerProviders(ctx)
	items := make([]Subscription, 0, len(providers)+1)
	builtin := s.builtinSubscription(controllerProviders, controllerErr)
	builtinSource := controllerProviders["default"].Proxies
	if len(builtinSource) == 0 && len(s.managedGroups) > 0 {
		builtinSource = controllerProviders[s.managedGroups[0]].Proxies
	}
	builtinKeys, _ := controllerProxyNodeKeys(builtinSource)
	if len(builtinKeys) > 0 {
		builtin.NodeCount = len(builtinKeys)
	}
	builtin.UniqueNodeCount = builtin.NodeCount
	items = append(items, builtin)
	seenNodes := builtinKeys
	for _, providerName := range sortedManagedProviderNames(providers, metadata) {
		provider := providers[providerName]
		id := strings.TrimPrefix(providerName, providerPrefix)
		meta := metadata[id]
		if meta.Provider == "" {
			meta = subscriptionMetadata{ID: id, Name: providerName, Provider: providerName}
		}
		item := Subscription{
			ID: id, Name: meta.Name, URLMasked: maskSubscriptionURL(provider.URL), Provider: providerName,
			Path: provider.Path, IntervalSeconds: provider.Interval, AutoUpdate: provider.Interval > 0,
			CreatedAt: meta.CreatedAt, UpdatedAt: meta.UpdatedAt, Groups: groups[providerName], Status: "pending",
			FetchVia: s.subscriptionFetchVia(provider.Proxy), Editable: true,
		}
		if item.IntervalSeconds == 0 {
			item.IntervalSeconds = defaultSubscriptionInterval
		}
		if controllerErr == nil {
			if remote, ok := controllerProviders[providerName]; ok {
				nodeKeys, internalDuplicates := controllerProxyNodeKeys(remote.Proxies)
				item.NodeCount = len(nodeKeys)
				item.DuplicateNodes = internalDuplicates
				for key := range nodeKeys {
					if _, exists := seenNodes[key]; exists {
						item.DuplicateNodes++
						continue
					}
					seenNodes[key] = struct{}{}
					item.UniqueNodeCount++
				}
				item.UpdatedAt = laterTime(item.UpdatedAt, parseProviderTime(remote.UpdatedAt))
				if item.NodeCount > 0 {
					if item.UniqueNodeCount == 0 {
						item.Status = "duplicate_only"
					} else {
						item.Status = "healthy"
					}
				}
			}
		} else {
			item.Status = "controller_unavailable"
			item.Error = "Mihomo 控制器暂不可用"
		}
		if updateError := s.subscriptionUpdateError(providerName); updateError != "" {
			if item.NodeCount > 0 {
				s.clearSubscriptionUpdateError(providerName)
			} else {
				item.Status = "error"
				item.Error = updateError
			}
		}
		item.Updating = s.subscriptionUpdating(providerName)
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Builtin != items[j].Builtin {
			return items[i].Builtin
		}
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].Name < items[j].Name
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	return items, nil
}

func (s *Service) CreateSubscription(ctx context.Context, input SubscriptionInput) (Subscription, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.URL = strings.TrimSpace(input.URL)
	if err := validateSubscription(input.Name, input.URL); err != nil {
		return Subscription{}, err
	}
	interval := normalizeSubscriptionInterval(input.IntervalSeconds)
	fetchProxy, err := s.subscriptionFetchProxy(input.FetchVia)
	if err != nil {
		return Subscription{}, err
	}
	if !s.subscriptionsReady() {
		return Subscription{}, ErrSubscriptionsNotConfigured
	}
	s.subscriptionMu.Lock()
	unlocked := false
	defer func() {
		if !unlocked {
			s.subscriptionMu.Unlock()
		}
	}()
	providers, groups, err := s.readSubscriptionConfig()
	if err != nil {
		return Subscription{}, err
	}
	if hasDuplicateSubscriptionURL(providers, "", input.URL) {
		return Subscription{}, ErrDuplicateSubscription
	}
	metadata, err := s.readSubscriptionMetadata()
	if err != nil {
		return Subscription{}, err
	}
	id := newSubscriptionID(input.URL)
	for metadata[id].ID != "" || providers[providerPrefix+id].URL != "" {
		id = newSubscriptionID(input.URL + time.Now().String())
	}
	providerName := providerPrefix + id
	providers[providerName] = newProviderConfig(providerName, input.URL, interval, s.delayTestURL, fetchProxy)
	addProviderToGroups(groups, providerName, s.managedGroupSet)
	previous, err := os.ReadFile(s.configPath)
	if err != nil {
		return Subscription{}, err
	}
	updatedConfig, err := updateSubscriptionYAML(previous, providers, groups)
	if err != nil {
		return Subscription{}, err
	}
	if err := s.replaceConfig(ctx, previous, updatedConfig); err != nil {
		return Subscription{}, err
	}
	now := s.now().UTC()
	metadata[id] = subscriptionMetadata{ID: id, Name: input.Name, Provider: providerName, CreatedAt: now, UpdatedAt: now}
	if err := s.writeSubscriptionMetadata(metadata); err != nil {
		return Subscription{}, err
	}
	s.subscriptionMu.Unlock()
	unlocked = true
	return s.subscriptionByID(ctx, id)
}

func (s *Service) UpdateSubscription(ctx context.Context, id string, patch SubscriptionPatch) (Subscription, error) {
	id = strings.TrimSpace(id)
	if id == "" || !s.subscriptionsReady() {
		return Subscription{}, chooseSubscriptionError(id == "", ErrInvalidSubscription, ErrSubscriptionsNotConfigured)
	}
	s.subscriptionMu.Lock()
	unlocked := false
	defer func() {
		if !unlocked {
			s.subscriptionMu.Unlock()
		}
	}()
	providers, groups, err := s.readSubscriptionConfig()
	if err != nil {
		return Subscription{}, err
	}
	metadata, err := s.readSubscriptionMetadata()
	if err != nil {
		return Subscription{}, err
	}
	meta, ok := metadata[id]
	providerName := providerPrefix + id
	if ok && meta.Provider != "" {
		providerName = meta.Provider
	}
	provider, ok := providers[providerName]
	if !ok {
		return Subscription{}, ErrSubscriptionNotFound
	}
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if name == "" || len([]rune(name)) > 64 {
			return Subscription{}, ErrInvalidSubscription
		}
		meta.Name = name
	}
	if patch.URL != nil && strings.TrimSpace(*patch.URL) != "" {
		if err := validateSubscription(meta.Name, strings.TrimSpace(*patch.URL)); err != nil {
			return Subscription{}, err
		}
		if hasDuplicateSubscriptionURL(providers, providerName, strings.TrimSpace(*patch.URL)) {
			return Subscription{}, ErrDuplicateSubscription
		}
		provider.URL = strings.TrimSpace(*patch.URL)
	}
	if patch.IntervalSeconds != nil {
		provider.Interval = normalizeSubscriptionInterval(*patch.IntervalSeconds)
	}
	if patch.FetchVia != nil {
		fetchProxy, err := s.subscriptionFetchProxy(*patch.FetchVia)
		if err != nil {
			return Subscription{}, err
		}
		provider.Proxy = fetchProxy
	}
	if provider.Interval == 0 {
		provider.Interval = defaultSubscriptionInterval
	}
	providers[providerName] = provider
	previous, err := os.ReadFile(s.configPath)
	if err != nil {
		return Subscription{}, err
	}
	updatedConfig, err := updateSubscriptionYAML(previous, providers, groups)
	if err != nil {
		return Subscription{}, err
	}
	if err := s.replaceConfig(ctx, previous, updatedConfig); err != nil {
		return Subscription{}, err
	}
	meta.ID = id
	meta.Provider = providerName
	meta.UpdatedAt = s.now().UTC()
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = meta.UpdatedAt
	}
	metadata[id] = meta
	if err := s.writeSubscriptionMetadata(metadata); err != nil {
		return Subscription{}, err
	}
	s.subscriptionMu.Unlock()
	unlocked = true
	return s.subscriptionByID(ctx, id)
}

func (s *Service) DeleteSubscription(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" || !s.subscriptionsReady() {
		return chooseSubscriptionError(id == "", ErrInvalidSubscription, ErrSubscriptionsNotConfigured)
	}
	s.subscriptionMu.Lock()
	defer s.subscriptionMu.Unlock()
	providers, groups, err := s.readSubscriptionConfig()
	if err != nil {
		return err
	}
	metadata, err := s.readSubscriptionMetadata()
	if err != nil {
		return err
	}
	providerName := providerPrefix + id
	if meta, ok := metadata[id]; ok && meta.Provider != "" {
		providerName = meta.Provider
	}
	provider, ok := providers[providerName]
	if !ok {
		return ErrSubscriptionNotFound
	}
	previous, err := os.ReadFile(s.configPath)
	if err != nil {
		return err
	}
	delete(providers, providerName)
	removeProviderFromGroups(groups, providerName)
	updatedConfig, err := updateSubscriptionYAML(previous, providers, groups)
	if err != nil {
		return err
	}
	if err := s.replaceConfig(ctx, previous, updatedConfig); err != nil {
		return err
	}
	delete(metadata, id)
	if err := s.writeSubscriptionMetadata(metadata); err != nil {
		return err
	}
	if provider.Path != "" {
		providerPath := filepath.Join(filepath.Dir(s.configPath), filepath.Clean(provider.Path))
		if filepath.IsAbs(provider.Path) || !withinDirectory(filepath.Dir(s.configPath), providerPath) {
			return nil
		}
		_ = os.Remove(providerPath)
	}
	return nil
}

func (s *Service) UpdateSubscriptionNow(ctx context.Context, id string) (Subscription, error) {
	if !s.subscriptionsReady() {
		return Subscription{}, ErrSubscriptionsNotConfigured
	}
	items, err := s.ListSubscriptions(ctx)
	if err != nil {
		return Subscription{}, err
	}
	var providerName string
	for _, item := range items {
		if item.ID == strings.TrimSpace(id) {
			if item.Builtin {
				return Subscription{}, ErrInvalidSubscription
			}
			providerName = item.Provider
			break
		}
	}
	if providerName == "" {
		return Subscription{}, ErrSubscriptionNotFound
	}
	s.setSubscriptionUpdating(providerName, true, "")
	if err := s.putJSON(ctx, []string{"providers", "proxies", providerName}, nil); err != nil {
		s.setSubscriptionUpdating(providerName, false, "订阅更新失败")
		return Subscription{}, err
	}
	s.setSubscriptionUpdating(providerName, false, "")
	return s.subscriptionByID(ctx, strings.TrimSpace(id))
}

func (s *Service) subscriptionByID(ctx context.Context, id string) (Subscription, error) {
	items, err := s.ListSubscriptions(ctx)
	if err != nil {
		return Subscription{}, err
	}
	for _, item := range items {
		if item.ID == id {
			return item, nil
		}
	}
	return Subscription{}, ErrSubscriptionNotFound
}

func (s *Service) subscriptionsReady() bool {
	return s != nil && strings.TrimSpace(s.configPath) != ""
}

func (s *Service) readSubscriptionConfig() (map[string]subscriptionProviderConfig, map[string][]string, error) {
	data, err := os.ReadFile(s.configPath)
	if err != nil {
		return nil, nil, err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, nil, fmt.Errorf("decode mihomo config: %w", err)
	}
	root := yamlRoot(&document)
	providers := map[string]subscriptionProviderConfig{}
	if node := mappingValue(root, "proxy-providers"); node != nil && node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			var provider subscriptionProviderConfig
			if err := node.Content[i+1].Decode(&provider); err == nil {
				providers[node.Content[i].Value] = provider
			}
		}
	}
	groups := map[string][]string{}
	if node := mappingValue(root, "proxy-groups"); node != nil && node.Kind == yaml.SequenceNode {
		for _, item := range node.Content {
			if item.Kind != yaml.MappingNode {
				continue
			}
			nameNode := mappingValue(item, "name")
			if nameNode == nil {
				continue
			}
			useNode := mappingValue(item, "use")
			if useNode == nil || useNode.Kind != yaml.SequenceNode {
				continue
			}
			for _, value := range useNode.Content {
				if strings.HasPrefix(value.Value, providerPrefix) {
					groups[value.Value] = append(groups[value.Value], nameNode.Value)
				}
			}
		}
	}
	return providers, groups, nil
}

func (s *Service) readSubscriptionMetadata() (map[string]subscriptionMetadata, error) {
	result := map[string]subscriptionMetadata{}
	data, err := os.ReadFile(s.metadataPath)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	var file subscriptionMetadataFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("decode mihomo subscription metadata: %w", err)
	}
	for _, item := range file.Subscriptions {
		if item.ID != "" {
			result[item.ID] = item
		}
	}
	return result, nil
}

func (s *Service) writeSubscriptionMetadata(metadata map[string]subscriptionMetadata) error {
	items := make([]subscriptionMetadata, 0, len(metadata))
	for _, item := range metadata {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	payload, err := json.MarshalIndent(subscriptionMetadataFile{Subscriptions: items}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.metadataPath), 0o700); err != nil {
		return err
	}
	return writePrivateFile(s.metadataPath, append(payload, '\n'))
}

func (s *Service) fetchControllerProviders(ctx context.Context) (map[string]controllerProvider, error) {
	var payload controllerProvidersResponse
	if err := s.getJSON(ctx, []string{"providers", "proxies"}, &payload); err != nil {
		return nil, err
	}
	if payload.Providers == nil {
		payload.Providers = map[string]controllerProvider{}
	}
	return payload.Providers, nil
}

func (s *Service) replaceConfig(ctx context.Context, previous, updated []byte) error {
	if err := writePrivateFile(s.configPath, updated); err != nil {
		return err
	}
	if err := s.reloadConfig(ctx); err != nil {
		_ = writePrivateFile(s.configPath, previous)
		_ = s.reloadConfig(context.Background())
		return err
	}
	return nil
}

func (s *Service) reloadConfig(ctx context.Context) error {
	path := s.reloadPath
	if path == "" {
		path = s.configPath
	}
	return s.putJSONQuery(ctx, []string{"configs"}, url.Values{"force": []string{"true"}}, map[string]string{"path": path, "payload": ""})
}

func updateSubscriptionYAML(data []byte, providers map[string]subscriptionProviderConfig, groups map[string][]string) ([]byte, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode mihomo config: %w", err)
	}
	root := yamlRoot(&document)
	providerMap := ensureMapping(root, "proxy-providers")
	for i := len(providerMap.Content) - 2; i >= 0; i -= 2 {
		if !strings.HasPrefix(providerMap.Content[i].Value, providerPrefix) {
			continue
		}
		deleteMappingKey(providerMap, providerMap.Content[i].Value)
	}
	providerNames := make([]string, 0, len(providers))
	for name := range providers {
		if strings.HasPrefix(name, providerPrefix) {
			providerNames = append(providerNames, name)
		}
	}
	sort.Strings(providerNames)
	for _, name := range providerNames {
		value, err := yamlValueNode(providers[name])
		if err != nil {
			return nil, err
		}
		setMappingValue(providerMap, name, value)
	}
	groupsNode := mappingValue(root, "proxy-groups")
	if groupsNode != nil && groupsNode.Kind == yaml.SequenceNode {
		for _, group := range groupsNode.Content {
			nameNode := mappingValue(group, "name")
			if nameNode == nil {
				continue
			}
			useNode := mappingValue(group, "use")
			targeted := false
			for _, groupNames := range groups {
				if containsString(groupNames, nameNode.Value) {
					targeted = true
					break
				}
			}
			if useNode == nil && targeted {
				useNode = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
				setMappingValue(group, "use", useNode)
			}
			if useNode == nil || useNode.Kind != yaml.SequenceNode {
				continue
			}
			for i := len(useNode.Content) - 1; i >= 0; i-- {
				if strings.HasPrefix(useNode.Content[i].Value, providerPrefix) {
					useNode.Content = append(useNode.Content[:i], useNode.Content[i+1:]...)
				}
			}
			for providerName, groupNames := range groups {
				if containsString(groupNames, nameNode.Value) {
					useNode.Content = append(useNode.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: providerName})
				}
			}
		}
	}
	var output strings.Builder
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return nil, err
	}
	_ = encoder.Close()
	return []byte(output.String()), nil
}

func newProviderConfig(providerName, rawURL string, interval int, healthURL, fetchProxy string) subscriptionProviderConfig {
	value := subscriptionProviderConfig{Type: "http", URL: rawURL, Path: "./providers/" + providerName + ".yaml", Interval: interval, Proxy: fetchProxy}
	value.HealthCheck.Enable = true
	value.HealthCheck.URL = healthURL
	value.HealthCheck.Interval = 300
	return value
}

func (s *Service) builtinSubscription(providers map[string]controllerProvider, controllerErr error) Subscription {
	groupName := "良心云"
	if len(s.managedGroups) > 0 && strings.TrimSpace(s.managedGroups[0]) != "" {
		groupName = s.managedGroups[0]
	}
	item := Subscription{
		ID: builtinSubscriptionID, Name: groupName + "（原有配置）", URLMasked: "原有 Mihomo 静态线路",
		Provider: "default", Status: "pending", Groups: []string{groupName, "自动选择", "故障转移"},
		FetchVia: "builtin", Builtin: true, Editable: false,
	}
	if controllerErr != nil {
		item.Status = "controller_unavailable"
		item.Error = "Mihomo 控制器暂不可用"
		return item
	}
	if defaultProvider, ok := providers["default"]; ok {
		item.NodeCount = countBuiltinProxyNodes(defaultProvider.Proxies)
	} else if group, ok := providers[groupName]; ok {
		item.NodeCount = len(group.Proxies)
	}
	if item.NodeCount > 0 {
		item.Status = "healthy"
	}
	return item
}

func countBuiltinProxyNodes(proxies []json.RawMessage) int {
	keys, _ := controllerProxyNodeKeys(proxies)
	return len(keys)
}

func controllerProxyNodeKeys(proxies []json.RawMessage) (map[string]struct{}, int) {
	keys := make(map[string]struct{}, len(proxies))
	duplicates := 0
	for _, raw := range proxies {
		var proxy struct {
			Name string `json:"name"`
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &proxy); err != nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(proxy.Type)) {
		case "direct", "reject", "selector", "urltest", "fallback", "loadbalance", "relay":
			continue
		}
		if proxy.Name == "" || isInformationalNode(proxy.Name) {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(proxy.Type)) + "\x00" + strings.TrimSpace(proxy.Name)
		if _, exists := keys[key]; exists {
			duplicates++
			continue
		}
		keys[key] = struct{}{}
	}
	return keys, duplicates
}

func sortedManagedProviderNames(providers map[string]subscriptionProviderConfig, metadata map[string]subscriptionMetadata) []string {
	names := make([]string, 0, len(providers))
	for name := range providers {
		if strings.HasPrefix(name, providerPrefix) {
			names = append(names, name)
		}
	}
	sort.SliceStable(names, func(i, j int) bool {
		left := metadata[strings.TrimPrefix(names[i], providerPrefix)].CreatedAt
		right := metadata[strings.TrimPrefix(names[j], providerPrefix)].CreatedAt
		if left.Equal(right) {
			return names[i] < names[j]
		}
		if left.IsZero() {
			return false
		}
		if right.IsZero() {
			return true
		}
		return left.Before(right)
	})
	return names
}

func (s *Service) subscriptionFetchProxy(fetchVia string) (string, error) {
	switch normalizeFetchVia(fetchVia) {
	case fetchViaRule:
		return "", nil
	case fetchViaDirect:
		return "DIRECT", nil
	case fetchViaBaseline:
		if len(s.managedGroups) > 0 && strings.TrimSpace(s.managedGroups[0]) != "" {
			return s.managedGroups[0], nil
		}
		return "良心云", nil
	case fetchViaAuto:
		return "自动选择", nil
	case fetchViaFallback:
		return "故障转移", nil
	default:
		return "", ErrInvalidSubscription
	}
}

func (s *Service) subscriptionFetchVia(proxyName string) string {
	switch strings.TrimSpace(proxyName) {
	case "":
		return fetchViaRule
	case "DIRECT":
		return fetchViaDirect
	case "自动选择":
		return fetchViaAuto
	case "故障转移":
		return fetchViaFallback
	default:
		if len(s.managedGroups) > 0 && proxyName == s.managedGroups[0] {
			return fetchViaBaseline
		}
		return fetchViaRule
	}
}

func normalizeFetchVia(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return fetchViaRule
	}
	return value
}

func addProviderToGroups(groups map[string][]string, providerName string, managed map[string]struct{}) {
	for group := range managed {
		if !containsString(groups[providerName], group) {
			groups[providerName] = append(groups[providerName], group)
		}
	}
	for _, group := range []string{"自动选择", "故障转移"} {
		if !containsString(groups[providerName], group) {
			groups[providerName] = append(groups[providerName], group)
		}
	}
}

func removeProviderFromGroups(groups map[string][]string, providerName string) {
	delete(groups, providerName)
}

func validateSubscription(name, rawURL string) error {
	if name == "" || len([]rune(name)) > 64 || len(rawURL) > 4096 {
		return ErrInvalidSubscription
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ErrInvalidSubscription
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".local") {
		return ErrInvalidSubscription
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()) {
		return ErrInvalidSubscription
	}
	return nil
}

func hasDuplicateSubscriptionURL(providers map[string]subscriptionProviderConfig, excludeProvider, rawURL string) bool {
	target := subscriptionURLFingerprint(rawURL)
	for providerName, provider := range providers {
		if providerName == excludeProvider || !strings.HasPrefix(providerName, providerPrefix) {
			continue
		}
		if subscriptionURLFingerprint(provider.URL) == target {
			return true
		}
	}
	return false
}

func subscriptionURLFingerprint(rawURL string) [32]byte {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return sha256.Sum256([]byte(strings.TrimSpace(rawURL)))
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""
	parsed.RawQuery = parsed.Query().Encode()
	return sha256.Sum256([]byte(parsed.String()))
}

func normalizeSubscriptionInterval(value int) int {
	if value <= 0 {
		return defaultSubscriptionInterval
	}
	if value < minSubscriptionInterval {
		return minSubscriptionInterval
	}
	if value > maxSubscriptionInterval {
		return maxSubscriptionInterval
	}
	return value
}

func maskSubscriptionURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return "已配置订阅"
	}
	return parsed.Scheme + "://" + parsed.Host + "/•••"
}

func newSubscriptionID(seed string) string {
	var random [8]byte
	if _, err := rand.Read(random[:]); err == nil {
		return hex.EncodeToString(random[:])
	}
	hash := sha256.Sum256([]byte(seed + time.Now().String()))
	return hex.EncodeToString(hash[:8])
}

func parseProviderTime(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed
	}
	return time.Time{}
}

func laterTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}

func chooseSubscriptionError(condition bool, whenTrue, whenFalse error) error {
	if condition {
		return whenTrue
	}
	return whenFalse
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func yamlRoot(document *yaml.Node) *yaml.Node {
	if document.Kind == yaml.DocumentNode && len(document.Content) > 0 {
		return document.Content[0]
	}
	return document
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func ensureMapping(mapping *yaml.Node, key string) *yaml.Node {
	if value := mappingValue(mapping, key); value != nil && value.Kind == yaml.MappingNode {
		return value
	}
	value := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setMappingValue(mapping, key, value)
	return value
}

func setMappingValue(mapping *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

func deleteMappingKey(mapping *yaml.Node, key string) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
			return
		}
	}
}

func yamlValueNode(value subscriptionProviderConfig) (*yaml.Node, error) {
	data, err := yaml.Marshal(value)
	if err != nil {
		return nil, err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	return yamlRoot(&document), nil
}

func writePrivateFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pansou-subscription-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return err
		}
		if retryErr := os.Rename(tmpPath, path); retryErr != nil {
			return retryErr
		}
	}
	return nil
}

func subscriptionMetadataPath(configPath string) string {
	if strings.TrimSpace(configPath) == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(configPath), "pansou-subscriptions.json")
}

func (s *Service) subscriptionUpdating(providerName string) bool {
	s.subscriptionStateMu.RLock()
	defer s.subscriptionStateMu.RUnlock()
	return s.subscriptionUpdates[providerName]
}

func (s *Service) subscriptionUpdateError(providerName string) string {
	s.subscriptionStateMu.RLock()
	defer s.subscriptionStateMu.RUnlock()
	return s.subscriptionErrors[providerName]
}

func (s *Service) setSubscriptionUpdating(providerName string, updating bool, updateError string) {
	s.subscriptionStateMu.Lock()
	defer s.subscriptionStateMu.Unlock()
	if updating {
		s.subscriptionUpdates[providerName] = true
	} else {
		delete(s.subscriptionUpdates, providerName)
	}
	if updateError == "" {
		delete(s.subscriptionErrors, providerName)
	} else {
		s.subscriptionErrors[providerName] = updateError
	}
}

func (s *Service) clearSubscriptionUpdateError(providerName string) {
	s.subscriptionStateMu.Lock()
	defer s.subscriptionStateMu.Unlock()
	delete(s.subscriptionErrors, providerName)
}

func withinDirectory(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
