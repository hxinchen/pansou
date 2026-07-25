package config

import (
	"testing"
	"time"
)

func TestResourceRetentionConfigFromEnvironment(t *testing.T) {
	t.Setenv("RESOURCE_RETENTION_DAYS", "45")
	t.Setenv("RESOURCE_CLEANUP_INTERVAL_SECONDS", "7200")
	t.Setenv("RESOURCE_CLEANUP_BATCH_SIZE", "250")
	Init()
	if AppConfig.ResourceRetention != 45*24*time.Hour ||
		AppConfig.ResourceCleanupInterval != 2*time.Hour ||
		AppConfig.ResourceCleanupBatchSize != 250 {
		t.Fatalf("resource retention config = %+v", AppConfig)
	}
}
