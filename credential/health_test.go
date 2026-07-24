package credential

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"pansou/storage"
)

type healthRepositoryStub struct {
	candidates []storage.PluginCredential
	records    []storage.CredentialHealthInput
	dueBefore  time.Time
}

func (r *healthRepositoryStub) ListPluginCredentialHealthCandidates(_ context.Context, _ []string, dueBefore time.Time, _ int) ([]storage.PluginCredential, error) {
	r.dueBefore = dueBefore
	return append([]storage.PluginCredential(nil), r.candidates...), nil
}

func (r *healthRepositoryStub) RecordPluginCredentialHealth(_ context.Context, input storage.CredentialHealthInput) error {
	r.records = append(r.records, input)
	return nil
}

type healthAdapterStub struct {
	result HealthCheckResult
	err    error
}

func (a healthAdapterStub) CheckCredentialHealth(context.Context, storage.PluginCredential, Access) (HealthCheckResult, error) {
	return a.result, a.err
}

func TestHealthMonitorRecordsInvalidCredential(t *testing.T) {
	repository := &healthRepositoryStub{candidates: []storage.PluginCredential{{PublicID: "cred-1", PluginKey: "gying"}}}
	cipher, err := NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	monitor := NewHealthMonitor(repository, NewService(&metadataRepository{}, cipher), map[string]HealthCheckAdapter{
		"gying": healthAdapterStub{result: HealthCheckResult{HealthStatus: storage.CredentialHealthInvalid, CredentialStatus: storage.CredentialStatusInvalid, ErrorCode: "auth_failed"}},
	}, HealthMonitorConfig{Interval: 6 * time.Hour, Timeout: time.Second, BatchSize: 10})
	monitor.now = func() time.Time { return now }
	monitor.jitter = func(time.Duration) time.Duration { return 0 }
	if err := monitor.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !repository.dueBefore.Equal(now.Add(-6 * time.Hour)) {
		t.Fatalf("due before = %v", repository.dueBefore)
	}
	if len(repository.records) != 1 || repository.records[0].HealthStatus != storage.CredentialHealthInvalid ||
		repository.records[0].CredentialStatus != storage.CredentialStatusInvalid || repository.records[0].ErrorCode != "auth_failed" {
		t.Fatalf("health records = %#v", repository.records)
	}
}

func TestHealthMonitorKeepsCredentialStatusOnTransientError(t *testing.T) {
	repository := &healthRepositoryStub{candidates: []storage.PluginCredential{{PublicID: "cred-2", PluginKey: "gying"}}}
	cipher, err := NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	monitor := NewHealthMonitor(repository, NewService(&metadataRepository{}, cipher), map[string]HealthCheckAdapter{
		"gying": healthAdapterStub{err: errors.New("temporary upstream failure")},
	}, HealthMonitorConfig{Interval: time.Hour, Timeout: time.Second})
	monitor.jitter = func(time.Duration) time.Duration { return 0 }
	if err := monitor.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repository.records) != 1 || repository.records[0].HealthStatus != storage.CredentialHealthError ||
		repository.records[0].CredentialStatus != "" || repository.records[0].ErrorCode != "health_check_failed" {
		t.Fatalf("health records = %#v", repository.records)
	}
}
