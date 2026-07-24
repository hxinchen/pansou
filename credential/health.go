package credential

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"pansou/storage"
)

type HealthRepository interface {
	ListPluginCredentialHealthCandidates(context.Context, []string, time.Time, int) ([]storage.PluginCredential, error)
	RecordPluginCredentialHealth(context.Context, storage.CredentialHealthInput) error
}

type HealthMonitorConfig struct {
	Enabled      bool
	Interval     time.Duration
	ScanInterval time.Duration
	InitialDelay time.Duration
	Timeout      time.Duration
	Jitter       time.Duration
	BatchSize    int
	OnError      func(error)
}

type HealthMonitor struct {
	repository HealthRepository
	service    *Service
	adapters   map[string]HealthCheckAdapter
	config     HealthMonitorConfig
	now        func() time.Time
	jitter     func(time.Duration) time.Duration
}

func NewHealthMonitor(repository HealthRepository, service *Service, adapters map[string]HealthCheckAdapter, config HealthMonitorConfig) *HealthMonitor {
	if config.Interval <= 0 {
		config.Interval = 6 * time.Hour
	}
	if config.ScanInterval <= 0 {
		config.ScanInterval = 30 * time.Minute
	}
	if config.InitialDelay < 0 {
		config.InitialDelay = 0
	}
	if config.Timeout <= 0 {
		config.Timeout = 30 * time.Second
	}
	if config.Jitter < 0 {
		config.Jitter = 0
	}
	if config.BatchSize <= 0 || config.BatchSize > 200 {
		config.BatchSize = 50
	}
	normalizedAdapters := make(map[string]HealthCheckAdapter, len(adapters))
	for key, adapter := range adapters {
		key = strings.ToLower(strings.TrimSpace(key))
		if key != "" && adapter != nil {
			normalizedAdapters[key] = adapter
		}
	}
	return &HealthMonitor{
		repository: repository,
		service:    service,
		adapters:   normalizedAdapters,
		config:     config,
		now:        time.Now,
		jitter:     credentialHealthJitter,
	}
}

func (m *HealthMonitor) Start(ctx context.Context) error {
	if m == nil || !m.config.Enabled || len(m.adapters) == 0 {
		return nil
	}
	if m.repository == nil || m.service == nil {
		return errors.New("credential health monitor is unavailable")
	}
	go m.loop(ctx)
	return nil
}

func (m *HealthMonitor) loop(ctx context.Context) {
	if !waitCredentialHealth(ctx, m.config.InitialDelay) {
		return
	}
	for {
		if err := m.runOnce(ctx); err != nil {
			m.report(err)
		}
		if !waitCredentialHealth(ctx, m.config.ScanInterval) {
			return
		}
	}
}

func (m *HealthMonitor) runOnce(ctx context.Context) error {
	keys := make([]string, 0, len(m.adapters))
	for key := range m.adapters {
		key = strings.ToLower(strings.TrimSpace(key))
		if key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return nil
	}
	dueBefore := m.now().Add(-m.config.Interval)
	candidates, err := m.repository.ListPluginCredentialHealthCandidates(ctx, keys, dueBefore, m.config.BatchSize)
	if err != nil {
		return err
	}
	access := Access{Open: m.service.OpenStored, Refresh: m.service.Refresh}
	for index, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		adapter := m.adapters[strings.ToLower(strings.TrimSpace(candidate.PluginKey))]
		if adapter == nil {
			continue
		}
		if index > 0 && !waitCredentialHealth(ctx, m.jitter(m.config.Jitter)) {
			return ctx.Err()
		}
		checkCtx, cancel := context.WithTimeout(ctx, m.config.Timeout)
		result, checkErr := adapter.CheckCredentialHealth(checkCtx, candidate, access)
		cancel()
		if checkErr != nil {
			result.HealthStatus = storage.CredentialHealthError
			result.CredentialStatus = ""
			if strings.TrimSpace(result.ErrorCode) == "" {
				result.ErrorCode = "health_check_failed"
			}
		}
		if !validHealthResult(result) {
			result = HealthCheckResult{HealthStatus: storage.CredentialHealthError, ErrorCode: "health_check_invalid_result"}
		}
		recordErr := m.repository.RecordPluginCredentialHealth(ctx, storage.CredentialHealthInput{
			PublicID: candidate.PublicID, HealthStatus: result.HealthStatus,
			CredentialStatus: result.CredentialStatus, ErrorCode: result.ErrorCode,
			CheckedAt: m.now(),
		})
		if recordErr != nil {
			m.report(fmt.Errorf("record %s credential health: %w", candidate.PluginKey, recordErr))
		}
		if checkErr != nil {
			m.report(fmt.Errorf("check %s credential %s: %w", candidate.PluginKey, candidate.PublicID, checkErr))
		}
	}
	return nil
}

func validHealthResult(result HealthCheckResult) bool {
	switch result.HealthStatus {
	case storage.CredentialHealthHealthy:
		return result.CredentialStatus == "" || result.CredentialStatus == storage.CredentialStatusActive
	case storage.CredentialHealthError:
		return result.CredentialStatus == ""
	case storage.CredentialHealthInvalid:
		return result.CredentialStatus == storage.CredentialStatusInvalid
	default:
		return false
	}
}

func (m *HealthMonitor) report(err error) {
	if err != nil && m != nil && m.config.OnError != nil {
		m.config.OnError(err)
	}
}

func waitCredentialHealth(ctx context.Context, duration time.Duration) bool {
	if duration <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func credentialHealthJitter(maximum time.Duration) time.Duration {
	if maximum <= 0 {
		return 0
	}
	var buffer [8]byte
	if _, err := cryptorand.Read(buffer[:]); err != nil {
		return maximum / 2
	}
	return time.Duration(binary.LittleEndian.Uint64(buffer[:]) % uint64(maximum+1))
}
