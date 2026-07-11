package sourceconfig

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

var (
	ErrClosed        = errors.New("source snapshot manager is closed")
	ErrInvalidConfig = errors.New("invalid search source config")
)

type Channel struct {
	Key         string `json:"key"`
	DisplayName string `json:"display_name,omitempty"`
	Enabled     bool   `json:"enabled"`
	Order       int    `json:"order"`
}

type PluginConfig struct {
	Enabled bool           `json:"enabled"`
	Order   int            `json:"order"`
	Config  map[string]any `json:"config,omitempty"`
}

type Config struct {
	SchemaVersion       int                     `json:"schema_version"`
	AsyncPluginsEnabled bool                    `json:"async_plugins_enabled"`
	Channels            []Channel               `json:"channels"`
	Plugins             map[string]PluginConfig `json:"plugins"`
}

type PluginDescriptor struct {
	Key               string   `json:"key"`
	DisplayName       string   `json:"display_name"`
	Description       string   `json:"description,omitempty"`
	RequiresAccount   bool     `json:"requires_account"`
	LoginType         string   `json:"login_type,omitempty"`
	AllowedConfigKeys []string `json:"allowed_config_keys,omitempty"`
	BindingConfigKeys []string `json:"binding_config_keys,omitempty"`
}

type Catalog struct {
	plugins map[string]PluginDescriptor
}

func NewCatalog(descriptors []PluginDescriptor) (*Catalog, error) {
	catalog := &Catalog{plugins: make(map[string]PluginDescriptor, len(descriptors))}
	for _, descriptor := range descriptors {
		descriptor.Key = strings.ToLower(strings.TrimSpace(descriptor.Key))
		if descriptor.Key == "" {
			return nil, fmt.Errorf("%w: empty plugin key", ErrInvalidConfig)
		}
		if _, exists := catalog.plugins[descriptor.Key]; exists {
			return nil, fmt.Errorf("%w: duplicate plugin %q", ErrInvalidConfig, descriptor.Key)
		}
		descriptor.AllowedConfigKeys = uniqueStrings(descriptor.AllowedConfigKeys)
		descriptor.BindingConfigKeys = uniqueStrings(descriptor.BindingConfigKeys)
		catalog.plugins[descriptor.Key] = descriptor
	}
	return catalog, nil
}

func DefaultCatalog(pluginNames []string) (*Catalog, error) {
	descriptors := make([]PluginDescriptor, 0, len(pluginNames))
	for _, name := range pluginNames {
		descriptor := PluginDescriptor{Key: name, DisplayName: name, Description: "无需账号，启用后参与实时搜索"}
		switch strings.ToLower(name) {
		case "qqpd":
			descriptor.DisplayName, descriptor.Description = "QQ 频道", "扫码登录后，按账号配置要搜索的频道 ID"
			descriptor.RequiresAccount, descriptor.LoginType = true, "qr"
		case "weibo":
			descriptor.DisplayName, descriptor.Description = "微博", "扫码登录后，按账号配置要跟踪的微博 UID"
			descriptor.RequiresAccount, descriptor.LoginType = true, "qr"
		case "gying":
			descriptor.DisplayName, descriptor.Description = "观影", "账号密码登录，可单独设置 HTTPS 站点地址"
			descriptor.RequiresAccount, descriptor.LoginType = true, "password"
			descriptor.AllowedConfigKeys = []string{"base_url"}
			descriptor.BindingConfigKeys = []string{"base_url"}
		case "panlian":
			descriptor.DisplayName, descriptor.Description = "盘链", "账号密码登录，可配置需要屏蔽的网盘类型"
			descriptor.RequiresAccount, descriptor.LoginType = true, "password"
			descriptor.AllowedConfigKeys = []string{"blocked_pan_types"}
		}
		descriptors = append(descriptors, descriptor)
	}
	return NewCatalog(descriptors)
}

func (c *Catalog) Plugins() []PluginDescriptor {
	if c == nil {
		return nil
	}
	result := make([]PluginDescriptor, 0, len(c.plugins))
	for _, descriptor := range c.plugins {
		result = append(result, descriptor)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result
}

func (c *Catalog) Validate(config Config) (Config, error) {
	if c == nil {
		return Config{}, fmt.Errorf("%w: catalog is nil", ErrInvalidConfig)
	}
	if config.SchemaVersion == 0 {
		config.SchemaVersion = 1
	}
	if config.SchemaVersion != 1 {
		return Config{}, fmt.Errorf("%w: unsupported schema version %d", ErrInvalidConfig, config.SchemaVersion)
	}
	seenChannels := make(map[string]struct{}, len(config.Channels))
	for index := range config.Channels {
		channel := &config.Channels[index]
		channel.Key = normalizeChannel(channel.Key)
		if channel.Key == "" {
			return Config{}, fmt.Errorf("%w: channel %d is empty", ErrInvalidConfig, index+1)
		}
		if _, exists := seenChannels[channel.Key]; exists {
			return Config{}, fmt.Errorf("%w: duplicate channel %q", ErrInvalidConfig, channel.Key)
		}
		seenChannels[channel.Key] = struct{}{}
	}
	normalizedPlugins := make(map[string]PluginConfig, len(config.Plugins))
	for rawKey, settings := range config.Plugins {
		key := strings.ToLower(strings.TrimSpace(rawKey))
		descriptor, exists := c.plugins[key]
		if !exists {
			return Config{}, fmt.Errorf("%w: unknown plugin %q", ErrInvalidConfig, rawKey)
		}
		allowed := make(map[string]struct{}, len(descriptor.AllowedConfigKeys))
		for _, field := range descriptor.AllowedConfigKeys {
			allowed[field] = struct{}{}
		}
		for field, value := range settings.Config {
			if _, exists := allowed[field]; !exists {
				return Config{}, fmt.Errorf("%w: plugin %s field %q is not allowed", ErrInvalidConfig, key, field)
			}
			if field == "base_url" {
				parsed, err := url.Parse(strings.TrimSpace(fmt.Sprint(value)))
				if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
					return Config{}, fmt.Errorf("%w: plugin %s base_url must be an HTTPS origin", ErrInvalidConfig, key)
				}
				settings.Config[field] = parsed.Scheme + "://" + parsed.Host
			}
		}
		normalizedPlugins[key] = settings
	}
	config.Plugins = normalizedPlugins
	sort.SliceStable(config.Channels, func(i, j int) bool {
		if config.Channels[i].Order == config.Channels[j].Order {
			return config.Channels[i].Key < config.Channels[j].Key
		}
		return config.Channels[i].Order < config.Channels[j].Order
	})
	return config, nil
}

func normalizeChannel(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "https://t.me/")
	value = strings.TrimPrefix(value, "http://t.me/")
	value = strings.TrimPrefix(value, "@")
	return strings.ToLower(strings.Trim(value, "/"))
}

func uniqueStrings(values []string) []string {
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
	sort.Strings(result)
	return result
}
