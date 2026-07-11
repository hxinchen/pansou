package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"pansou/config"
	"pansou/credential"
	"pansou/plugin"
	"pansou/sourceconfig"
	"pansou/storage"
)

const legacyCredentialMigrationKey = "legacy_plugin_credentials_v1"

var accountPluginKeys = []string{"gying", "panlian", "qqpd", "weibo"}

func bootstrapCredentialService(ctx context.Context, store *storage.Store, sources *sourceconfig.Service) (*credential.Service, error) {
	if store == nil {
		return nil, nil
	}
	credentialCount, err := store.CountPluginCredentials(ctx)
	if err != nil {
		return nil, err
	}
	legacyFiles, err := discoverLegacyCredentialFiles(config.AppConfig.CachePath)
	if err != nil {
		return nil, err
	}
	keyRequired := credentialCount > 0 || len(legacyFiles) > 0 || accountPluginsEnabled(sources)
	encodedKey := strings.TrimSpace(os.Getenv("PLUGIN_CREDENTIAL_MASTER_KEY"))
	if encodedKey == "" {
		if keyRequired {
			return nil, errors.New("PLUGIN_CREDENTIAL_MASTER_KEY is required for enabled or stored account plugins")
		}
		return nil, nil
	}
	cipher, err := credential.NewCipher(encodedKey)
	if err != nil {
		return nil, err
	}
	service := credential.NewService(store, cipher)

	if _, err := store.GetDataMigration(ctx, legacyCredentialMigrationKey); errors.Is(err, storage.ErrNotFound) {
		if err := migrateLegacyCredentials(ctx, store, service, sources, legacyFiles); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if err := validateStoredCredentials(ctx, store, service); err != nil {
		return nil, err
	}
	return service, nil
}

func accountPluginsEnabled(sources *sourceconfig.Service) bool {
	if sources == nil || sources.Manager == nil {
		return false
	}
	lease, err := sources.Manager.Acquire()
	if err != nil {
		return false
	}
	defer lease.Release()
	snapshot := lease.Snapshot()
	if snapshot == nil {
		return false
	}
	for _, key := range accountPluginKeys {
		if snapshot.Config.Plugins[key].Enabled {
			return true
		}
	}
	return false
}

type legacyCredentialFile struct {
	PluginKey string
	Path      string
}

func discoverLegacyCredentialFiles(cachePath string) ([]legacyCredentialFile, error) {
	result := make([]legacyCredentialFile, 0)
	for _, key := range accountPluginKeys {
		directory := filepath.Join(cachePath, key+"_users")
		entries, err := os.ReadDir(directory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("scan legacy %s credentials: %w", key, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") || legacyAuxiliaryFile(entry.Name()) {
				continue
			}
			path := filepath.Join(directory, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("read legacy credential %s: %w", filepath.Base(path), err)
			}
			if len(bytes.TrimSpace(data)) == 0 {
				continue
			}
			result = append(result, legacyCredentialFile{PluginKey: key, Path: path})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func legacyAuxiliaryFile(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return name == "gying_config.json" || name == "panlian_config.json"
}

func migrateLegacyCredentials(ctx context.Context, store *storage.Store, service *credential.Service, sources *sourceconfig.Service, files []legacyCredentialFile) error {
	parsers := make(map[string]credential.LegacyCredentialParser, len(accountPluginKeys))
	closers := make([]plugin.AsyncSearchPlugin, 0, len(accountPluginKeys))
	defer func() {
		for _, instance := range closers {
			if closeable, ok := instance.(plugin.CloseablePlugin); ok {
				_ = closeable.Close()
			}
		}
	}()
	for _, key := range accountPluginKeys {
		instance, err := configuredCredentialPlugin(key, sources)
		if err != nil {
			return err
		}
		parser, ok := instance.(credential.LegacyCredentialParser)
		if !ok {
			return fmt.Errorf("plugin %s does not support legacy credential migration", key)
		}
		parsers[key] = parser
		closers = append(closers, instance)
	}

	prepared := make([]storage.CreatePluginCredentialInput, 0, len(files))
	counts := make(map[string]int, len(accountPluginKeys))
	for _, file := range files {
		data, err := os.ReadFile(file.Path)
		if err != nil {
			return fmt.Errorf("read legacy credential %s: %w", filepath.Base(file.Path), err)
		}
		material, err := parsers[file.PluginKey].ParseLegacyCredential(data)
		if err != nil {
			return fmt.Errorf("parse legacy credential %s: %w", filepath.Base(file.Path), err)
		}
		input, err := service.Prepare(credential.CreateInput{
			PluginKey: file.PluginKey, Scope: storage.CredentialScopeAdminPrivate,
			DisplayName: material.DisplayName, PublicMetadata: material.PublicMetadata,
			Secret: material.Secret, StableID: material.StableID, ConfigBinding: material.ConfigBinding,
			Status: material.Status, ExpiresAt: material.ExpiresAt,
		})
		for index := range material.Secret {
			material.Secret[index] = 0
		}
		if err != nil {
			return fmt.Errorf("encrypt legacy credential %s: %w", filepath.Base(file.Path), err)
		}
		prepared = append(prepared, input)
		counts[file.PluginKey]++
	}
	summary := map[string]any{"total": len(prepared), "counts": counts, "legacy_files_retained": true}
	result, err := store.ImportPluginCredentialsAndCompleteMigration(ctx, storage.ImportPluginCredentialsInput{
		MigrationKey: legacyCredentialMigrationKey, Credentials: prepared, Summary: summary, CompletedAt: time.Now(),
	})
	if err != nil {
		return err
	}
	if result.Applied && result.Imported > 0 {
		fmt.Printf("已迁移 %d 个旧插件账号；旧文件保留不变，请在验证和备份后手工清理\n", result.Imported)
	}
	return nil
}

func configuredCredentialPlugin(key string, sources *sourceconfig.Service) (plugin.AsyncSearchPlugin, error) {
	instance, err := plugin.CreateRegisteredPlugin(key)
	if err != nil {
		return nil, err
	}
	if managed, ok := instance.(plugin.ManagedCredentialPlugin); ok {
		managed.SetManagedCredentialMode(true)
	}
	var values map[string]any
	if sources != nil && sources.Manager != nil {
		lease, acquireErr := sources.Manager.Acquire()
		if acquireErr == nil {
			if snapshot := lease.Snapshot(); snapshot != nil {
				values = snapshot.Config.Plugins[key].Config
			}
			lease.Release()
		}
	}
	if configurable, ok := instance.(plugin.RuntimeConfigurablePlugin); ok {
		if err := configurable.ApplyRuntimeConfig(values); err != nil {
			return nil, fmt.Errorf("configure credential plugin %s: %w", key, err)
		}
	}
	if initializable, ok := instance.(plugin.InitializablePlugin); ok {
		if err := initializable.Initialize(); err != nil {
			return nil, fmt.Errorf("initialize credential plugin %s: %w", key, err)
		}
	}
	return instance, nil
}

func credentialAdapterMap(manager *plugin.PluginManager) map[string]any {
	adapters := make(map[string]any)
	if manager == nil {
		return adapters
	}
	for _, instance := range manager.GetPlugins() {
		if _, password := instance.(credential.PasswordLoginAdapter); password {
			adapters[strings.ToLower(instance.Name())] = instance
			continue
		}
		if _, qr := instance.(credential.QRLoginAdapter); qr {
			adapters[strings.ToLower(instance.Name())] = instance
		}
	}
	return adapters
}

func validateStoredCredentials(ctx context.Context, store *storage.Store, service *credential.Service) error {
	for page := 1; ; page++ {
		items, err := store.ListPluginCredentials(ctx, storage.PluginCredentialFilter{IncludeSecrets: true, Page: page, PageSize: 200})
		if err != nil {
			return err
		}
		for _, item := range items.Items {
			plaintext, err := service.OpenStored(item)
			if err != nil {
				return fmt.Errorf("credential integrity validation failed for %s: %w", item.PublicID, err)
			}
			for index := range plaintext {
				plaintext[index] = 0
			}
		}
		if int64(page*items.PageSize) >= items.Total {
			return nil
		}
	}
}
