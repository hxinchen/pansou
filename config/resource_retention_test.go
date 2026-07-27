package config

import (
	"testing"
	"time"
)

func TestResourceCleanupConfigFromEnvironment(t *testing.T) {
	t.Setenv("RESOURCE_CLEANUP_INTERVAL_SECONDS", "7200")
	t.Setenv("RESOURCE_CLEANUP_BATCH_SIZE", "250")
	Init()
	if AppConfig.ResourceCleanupInterval != 2*time.Hour ||
		AppConfig.ResourceCleanupBatchSize != 250 {
		t.Fatalf("resource cleanup config = %+v", AppConfig)
	}
}
