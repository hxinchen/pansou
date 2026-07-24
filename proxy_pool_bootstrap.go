package main

import (
	"context"
	"os"
	"strings"

	"pansou/config"
	"pansou/proxypool"
	"pansou/storage"
)

func bootstrapProxyPool(ctx context.Context, store *storage.Store) (*proxypool.Service, error) {
	if store == nil {
		return nil, nil
	}
	encodedKey := strings.TrimSpace(os.Getenv("PLUGIN_CREDENTIAL_MASTER_KEY"))
	if encodedKey == "" {
		return nil, nil
	}
	cipher, err := proxypool.NewCipher(encodedKey)
	if err != nil {
		return nil, err
	}
	pool := proxypool.NewService(store, cipher, proxypool.Config{
		Enabled:           config.AppConfig.ProxyPoolEnabled,
		HealthEnabled:     config.AppConfig.ProxyPoolHealthEnabled,
		HealthWorkers:     config.AppConfig.ProxyPoolHealthWorkers,
		ProbeTimeout:      config.AppConfig.ProxyPoolProbeTimeout,
		ProbeInterval:     config.AppConfig.ProxyPoolProbeInterval,
		NodeRefresh:       config.AppConfig.ProxyPoolNodeRefresh,
		FailureThreshold:  config.AppConfig.ProxyPoolFailureThreshold,
		Cooldown:          config.AppConfig.ProxyPoolCooldown,
		CooldownMax:       config.AppConfig.ProxyPoolCooldownMax,
		CooldownJitter:    config.AppConfig.ProxyPoolCooldownJitter,
		MaxHotNodes:       config.AppConfig.ProxyPoolMaxHotNodes,
		MaxPerNode:        config.AppConfig.ProxyPoolMaxPerNode,
		MaxAttempts:       config.AppConfig.ProxyPoolMaxAttempts,
		StickyTTL:         config.AppConfig.ProxyPoolStickyTTL,
		StickyMaxEntries:  config.AppConfig.ProxyPoolStickyMaxEntries,
		SelectionStrategy: config.AppConfig.ProxyPoolSelectionStrategy,
		ProbeURLs:         config.AppConfig.ProxyPoolProbeURLs,
	})
	if err := pool.Start(ctx); err != nil {
		return nil, err
	}
	return pool, nil
}
