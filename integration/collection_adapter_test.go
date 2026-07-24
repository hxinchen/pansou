package integration

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"pansou/collection"
	"pansou/model"
	"pansou/proxypool"
	"pansou/storage"
)

func TestBatchSnapshotPreservesRuntimeKeywordFields(t *testing.T) {
	cooldown := time.Hour
	next := time.Now().Add(time.Minute)
	input := collection.Keyword{
		ID: 7, Value: "Example", Normalized: "example", KeywordType: "general",
		SourceType: "manual", SourceKey: "operator", Enabled: true, Priority: 8,
		Cooldown: cooldown, NextEligibleAt: &next,
	}
	run := storage.CollectionRun{
		ID: 9,
		Items: []storage.CollectionRunItem{{
			ID: 11, RunID: 9, KeywordID: pointer(int64(7)), Keyword: "Example",
			NormalizedKeyword: "example", KeywordType: "general", Priority: 8,
		}},
	}
	batch := toCollectionBatch(run)
	snapshots := map[string]collection.Keyword{input.Normalized: input}
	for index := range batch.Items {
		if snapshot, exists := snapshots[batch.Items[index].Keyword.Normalized]; exists {
			batch.Items[index].Keyword = snapshot
		}
	}
	got := batch.Items[0].Keyword
	if got.Cooldown != cooldown || got.SourceKey != "operator" || !got.Enabled || got.NextEligibleAt == nil || !got.NextEligibleAt.Equal(next) {
		t.Fatalf("keyword snapshot lost runtime fields: %+v", got)
	}
}

func TestStoredBatchSnapshotRestoresCooldown(t *testing.T) {
	seconds := int64(3600)
	run := storage.CollectionRun{ID: 9, Items: []storage.CollectionRunItem{{
		ID: 11, RunID: 9, Keyword: "Example", NormalizedKeyword: "example",
		KeywordType: "general", CooldownSeconds: &seconds,
	}}}
	batch := toCollectionBatch(run)
	if got := batch.Items[0].Keyword.Cooldown; got != time.Hour {
		t.Fatalf("Cooldown = %v, want 1h", got)
	}
}

func TestClaimPendingRejectsDisabledRepository(t *testing.T) {
	var repository *CollectionRepository
	claimed, err := repository.ClaimPending(context.Background())
	if claimed != nil || err == nil {
		t.Fatalf("ClaimPending() = %+v, %v", claimed, err)
	}
}

func TestCollectionSourceIdentityUsesExecutedSource(t *testing.T) {
	tests := []struct {
		name       string
		source     collection.Source
		wantType   string
		wantSource string
	}{
		{name: "configured plugin key", source: collection.Source{Type: "plugin", Key: "plugin:xdyh"}, wantType: "plugin", wantSource: "plugin:xdyh"},
		{name: "single channel fallback", source: collection.Source{Type: " TG ", Channels: []string{" channel-a "}}, wantType: "tg", wantSource: "channel-a"},
		{name: "single plugin fallback", source: collection.Source{Type: "plugin", Plugins: []string{" pansearch "}}, wantType: "plugin", wantSource: "pansearch"},
		{name: "ambiguous plugins stay empty", source: collection.Source{Type: "plugin", Plugins: []string{"one", "two"}}, wantType: "plugin"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotType, gotSource := collectionSourceIdentity(test.source)
			if gotType != test.wantType || gotSource != test.wantSource {
				t.Fatalf("collectionSourceIdentity() = %q, %q, want %q, %q", gotType, gotSource, test.wantType, test.wantSource)
			}
		})
	}
}

type integrationProxyRepo struct {
	nodes    []storage.ProxyNode
	outcomes []storage.ProxyOutcomeRecord
}

func (r *integrationProxyRepo) ImportProxyNodes(context.Context, storage.ProxyImportInput, []storage.ProxyNodeInput) (storage.ProxyImportResult, error) {
	panic("unused")
}
func (r *integrationProxyRepo) ListProxyNodes(context.Context, storage.ProxyNodeFilter) (storage.ProxyNodePage, error) {
	panic("unused")
}
func (r *integrationProxyRepo) GetProxyNode(context.Context, int64) (storage.ProxyNode, error) {
	panic("unused")
}
func (r *integrationProxyRepo) ListRuntimeProxyNodes(context.Context, time.Time, int) ([]storage.ProxyNode, error) {
	return r.nodes, nil
}
func (r *integrationProxyRepo) ListRuntimeProxyTargetStats(context.Context, []int64) ([]storage.ProxyTargetStat, error) {
	return nil, nil
}
func (r *integrationProxyRepo) ListProxyProbeCandidates(context.Context, time.Time, time.Time, int) ([]storage.ProxyNode, error) {
	return nil, nil
}
func (r *integrationProxyRepo) RecordProxyProbe(context.Context, int64, bool, time.Duration, int, time.Duration) error {
	return nil
}
func (r *integrationProxyRepo) RecordProxyOutcome(_ context.Context, outcome storage.ProxyOutcomeRecord) error {
	r.outcomes = append(r.outcomes, outcome)
	return nil
}
func (r *integrationProxyRepo) SetProxyNodeEnabled(context.Context, int64, bool) error {
	return nil
}
func (r *integrationProxyRepo) DeleteProxyNode(context.Context, int64) error { return nil }
func (r *integrationProxyRepo) SetProxyBatchEnabled(context.Context, int64, bool) error {
	return nil
}
func (r *integrationProxyRepo) ListProxyBatches(context.Context, int, int) ([]storage.ProxyImportBatch, int64, error) {
	return nil, 0, nil
}
func (r *integrationProxyRepo) ProxyPoolSummary(context.Context, time.Time) (storage.ProxyPoolSummary, error) {
	return storage.ProxyPoolSummary{}, nil
}
func (r *integrationProxyRepo) ListProxyPolicies(context.Context) ([]storage.ProxyPolicy, error) {
	return nil, nil
}
func (r *integrationProxyRepo) ReplaceProxyPolicies(context.Context, []storage.ProxyPolicy) error {
	return nil
}

func TestProxyPolicyRetriesWithDifferentNodes(t *testing.T) {
	cipher, err := proxypool.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	makeNode := func(id int64, raw string, latency int64) storage.ProxyNode {
		ciphertext, nonce, fingerprint, encryptErr := cipher.Encrypt(raw)
		if encryptErr != nil {
			t.Fatal(encryptErr)
		}
		return storage.ProxyNode{
			ID: id, Scheme: "http", Host: "127.0.0.1", Port: int(8000 + id), Ciphertext: ciphertext, Nonce: nonce,
			Fingerprint: fingerprint, KeyVersion: 1, Status: storage.ProxyStatusHealthy, Enabled: true,
			LatencyMS: latency, ExpiresAt: time.Now().Add(time.Hour),
		}
	}
	repo := &integrationProxyRepo{nodes: []storage.ProxyNode{
		makeNode(1, "http://127.0.0.1:8001", 10),
		makeNode(2, "http://127.0.0.1:8002", 20),
	}}
	pool := proxypool.NewService(repo, cipher, proxypool.Config{Enabled: true, HealthEnabled: false, MaxAttempts: 2})
	if err := pool.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	attempts := make([]string, 0, 2)
	checker := LinkChecker{Pool: pool}
	result, err := checker.checkWithProxyPolicy(context.Background(), collection.LinkCheckCandidate{Platform: "quark", URL: "https://example.test"}, proxypool.ModeProxyOnly,
		func(proxyURL string) (model.CheckResult, error) {
			attempts = append(attempts, proxyURL)
			if len(attempts) == 1 {
				return model.CheckResult{State: "uncertain", Summary: "HTTP状态码: 429"}, nil
			}
			return model.CheckResult{State: "ok"}, nil
		}, model.CheckResult{}, nil)
	if err != nil || result.State != "ok" {
		t.Fatalf("retry result = %+v, %v", result, err)
	}
	if len(attempts) != 2 || attempts[0] == attempts[1] {
		t.Fatalf("proxy attempts = %#v, want two distinct nodes", attempts)
	}
	if len(repo.outcomes) != 2 || repo.outcomes[0].FailureScope != proxypool.FailureScopeTarget || !repo.outcomes[1].Success {
		t.Fatalf("outcomes = %+v", repo.outcomes)
	}
}

func pointer[T any](value T) *T { return &value }
