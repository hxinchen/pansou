package config

import (
	"testing"
	"time"
)

func TestLinkCheckConfigFromEnvironment(t *testing.T) {
	t.Setenv("LINK_CHECK_WORKERS", "12")
	t.Setenv("LINK_CHECK_TIMEOUT_SECONDS", "14")
	t.Setenv("LINK_CHECK_PER_PLATFORM", "3")
	t.Setenv("LINK_CHECK_CIRCUIT_FAILURES", "4")
	t.Setenv("LINK_CHECK_CIRCUIT_COOLDOWN_SECONDS", "90")
	t.Setenv("LINK_CHECK_BACKLOG_INTERVAL_SECONDS", "240")
	t.Setenv("LINK_CHECK_WRITE_BATCH_SIZE", "24")
	t.Setenv("LINK_CHECK_WRITE_FLUSH_SECONDS", "2")
	Init()
	if AppConfig.LinkCheckWorkers != 12 ||
		AppConfig.LinkCheckTimeout != 14*time.Second ||
		AppConfig.LinkCheckPerPlatform != 3 ||
		AppConfig.LinkCheckCircuitFailures != 4 ||
		AppConfig.LinkCheckCircuitCooldown != 90*time.Second ||
		AppConfig.LinkCheckBacklogInterval != 240*time.Second ||
		AppConfig.LinkCheckWriteBatchSize != 24 ||
		AppConfig.LinkCheckWriteFlushInterval != 2*time.Second {
		t.Fatalf("link check config = %+v", AppConfig)
	}
}
