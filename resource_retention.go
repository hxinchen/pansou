package main

import (
	"context"
	"log"
	"time"

	"pansou/config"
	"pansou/storage"
)

const resourceCleanupMaxBatches = 100

func startResourceRetentionCleanup(ctx context.Context, store *storage.Store) {
	if store == nil {
		return
	}
	interval := config.AppConfig.ResourceCleanupInterval
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	go func() {
		run := func() {
			cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			defer cancel()
			deleted, err := store.CleanupTerminalResources(cleanupCtx, config.AppConfig.ResourceCleanupBatchSize,
				resourceCleanupMaxBatches)
			if err != nil {
				log.Printf("终态资源清理: %v", err)
				return
			}
			if deleted > 0 {
				log.Printf("终态资源清理: 删除 %d 条终态资源", deleted)
			}
		}

		run()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
}
