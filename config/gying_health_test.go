package config

import (
	"testing"
	"time"
)

func TestGyingHealthCheckConfigFromEnvironment(t *testing.T) {
	t.Setenv("GYING_HEALTH_CHECK_ENABLED", "false")
	t.Setenv("GYING_HEALTH_CHECK_INTERVAL_SECONDS", "7200")
	t.Setenv("GYING_HEALTH_CHECK_SCAN_SECONDS", "900")
	t.Setenv("GYING_HEALTH_CHECK_INITIAL_DELAY_SECONDS", "45")
	t.Setenv("GYING_HEALTH_CHECK_TIMEOUT_SECONDS", "20")
	t.Setenv("GYING_HEALTH_CHECK_JITTER_SECONDS", "8")
	t.Setenv("GYING_HEALTH_CHECK_BATCH_SIZE", "25")
	Init()
	if AppConfig.GyingHealthCheckEnabled ||
		AppConfig.GyingHealthCheckInterval != 2*time.Hour ||
		AppConfig.GyingHealthCheckScanInterval != 15*time.Minute ||
		AppConfig.GyingHealthCheckInitialDelay != 45*time.Second ||
		AppConfig.GyingHealthCheckTimeout != 20*time.Second ||
		AppConfig.GyingHealthCheckJitter != 8*time.Second ||
		AppConfig.GyingHealthCheckBatchSize != 25 {
		t.Fatalf("gying health check config = %+v", AppConfig)
	}
}
