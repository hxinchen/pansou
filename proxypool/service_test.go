package proxypool

import (
	"context"
	"encoding/base64"
	"errors"
	"sync"
	"testing"
	"time"

	"pansou/storage"
)

type fakeRepository struct {
	mu       sync.Mutex
	nodes    []storage.ProxyNode
	targets  []storage.ProxyTargetStat
	policies []storage.ProxyPolicy
	outcomes []storage.ProxyOutcomeRecord
}

func (f *fakeRepository) ImportProxyNodes(context.Context, storage.ProxyImportInput, []storage.ProxyNodeInput) (storage.ProxyImportResult, error) {
	panic("unused")
}
func (f *fakeRepository) ListProxyNodes(context.Context, storage.ProxyNodeFilter) (storage.ProxyNodePage, error) {
	panic("unused")
}
func (f *fakeRepository) GetProxyNode(context.Context, int64) (storage.ProxyNode, error) {
	panic("unused")
}
func (f *fakeRepository) ListRuntimeProxyNodes(context.Context, time.Time, int) ([]storage.ProxyNode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]storage.ProxyNode(nil), f.nodes...), nil
}
func (f *fakeRepository) ListRuntimeProxyTargetStats(context.Context, []int64) ([]storage.ProxyTargetStat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]storage.ProxyTargetStat(nil), f.targets...), nil
}
func (f *fakeRepository) ListProxyProbeCandidates(context.Context, time.Time, time.Time, int) ([]storage.ProxyNode, error) {
	return nil, nil
}
func (f *fakeRepository) RecordProxyProbe(context.Context, int64, bool, time.Duration, int, time.Duration) error {
	return nil
}
func (f *fakeRepository) RecordProxyOutcome(_ context.Context, outcome storage.ProxyOutcomeRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.outcomes = append(f.outcomes, outcome)
	return nil
}
func (f *fakeRepository) SetProxyNodeEnabled(context.Context, int64, bool) error  { return nil }
func (f *fakeRepository) DeleteProxyNode(context.Context, int64) error            { return nil }
func (f *fakeRepository) SetProxyBatchEnabled(context.Context, int64, bool) error { return nil }
func (f *fakeRepository) ListProxyBatches(context.Context, int, int) ([]storage.ProxyImportBatch, int64, error) {
	return nil, 0, nil
}
func (f *fakeRepository) ProxyPoolSummary(context.Context, time.Time) (storage.ProxyPoolSummary, error) {
	return storage.ProxyPoolSummary{}, nil
}
func (f *fakeRepository) ListProxyPolicies(context.Context) ([]storage.ProxyPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]storage.ProxyPolicy(nil), f.policies...), nil
}

func (f *fakeRepository) setNodes(nodes []storage.ProxyNode) {
	f.mu.Lock()
	f.nodes = append([]storage.ProxyNode(nil), nodes...)
	f.mu.Unlock()
}

func (f *fakeRepository) recordedOutcomes() []storage.ProxyOutcomeRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]storage.ProxyOutcomeRecord(nil), f.outcomes...)
}

func testProxyNode(t *testing.T, cipher *Cipher, id int64, raw string, latency, success, failure int64) storage.ProxyNode {
	t.Helper()
	ciphertext, nonce, fingerprint, err := cipher.Encrypt(raw)
	if err != nil {
		t.Fatal(err)
	}
	return storage.ProxyNode{
		ID: id, Scheme: "http", Host: "127.0.0.1", Port: int(8000 + id), Ciphertext: ciphertext, Nonce: nonce,
		Fingerprint: fingerprint, KeyVersion: 1, Status: storage.ProxyStatusHealthy, Enabled: true,
		LatencyMS: latency, SuccessCount: success, FailureCount: failure, ExpiresAt: time.Now().Add(time.Hour),
	}
}
func (f *fakeRepository) ReplaceProxyPolicies(context.Context, []storage.ProxyPolicy) error {
	return nil
}

func TestServiceAcquireHonorsPerNodeLimitAndStickyKey(t *testing.T) {
	cipher, err := NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, nonce, fingerprint, err := cipher.Encrypt("http://8.8.8.8:8080")
	if err != nil {
		t.Fatal(err)
	}
	repo := &fakeRepository{nodes: []storage.ProxyNode{{ID: 1, Scheme: "http", Host: "8.8.8.8", Port: 8080, Ciphertext: ciphertext, Nonce: nonce, Fingerprint: fingerprint, KeyVersion: 1, Status: storage.ProxyStatusHealthy, Enabled: true, ExpiresAt: time.Now().Add(time.Hour)}}}
	service := NewService(repo, cipher, Config{Enabled: true, HealthEnabled: false, MaxPerNode: 1})
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	first, err := service.Acquire(context.Background(), ProxyRequest{TargetType: "platform", TargetKey: "quark", StickyKey: "resource-a"})
	if err != nil {
		t.Fatal(err)
	}
	if first.URL() != "http://8.8.8.8:8080" {
		t.Fatalf("lease URL = %q", first.URL())
	}
	if _, err := service.Acquire(context.Background(), ProxyRequest{TargetType: "platform", TargetKey: "quark"}); err != ErrNoProxy {
		t.Fatalf("second acquire err = %v, want ErrNoProxy", err)
	}
	first.Release(ProxyOutcome{Success: true})
	second, err := service.Acquire(context.Background(), ProxyRequest{TargetType: "platform", TargetKey: "quark", StickyKey: "resource-a"})
	if err != nil {
		t.Fatal(err)
	}
	second.Release(ProxyOutcome{Success: true})
}

func TestRouteModeFallsBackToGlobal(t *testing.T) {
	cipher, _ := NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	repo := &fakeRepository{policies: []storage.ProxyPolicy{{TargetType: "global", TargetKey: "*", Mode: ModeProxyFirst}, {TargetType: "platform", TargetKey: "quark", Mode: ModeProxyOnly}}}
	service := NewService(repo, cipher, Config{HealthEnabled: false})
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := service.RouteMode("platform", "quark"); got != ModeProxyOnly {
		t.Fatalf("quark mode = %q", got)
	}
	if got := service.RouteMode("platform", "baidu"); got != ModeProxyFirst {
		t.Fatalf("baidu mode = %q", got)
	}
}

func TestRefreshLoopAddsNewHealthyNodes(t *testing.T) {
	cipher, _ := NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	repo := &fakeRepository{}
	service := NewService(repo, cipher, Config{Enabled: true, HealthEnabled: false, NodeRefresh: 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := service.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer service.Stop()
	repo.setNodes([]storage.ProxyNode{testProxyNode(t, cipher, 1, "http://127.0.0.1:8001", 50, 1, 0)})
	deadline := time.Now().Add(time.Second)
	for {
		lease, err := service.Acquire(context.Background(), ProxyRequest{TargetType: "platform", TargetKey: "quark"})
		if err == nil {
			lease.Release(ProxyOutcome{Success: true})
			break
		}
		if !errors.Is(err, ErrNoProxy) || time.Now().After(deadline) {
			t.Fatalf("periodic refresh did not add healthy node: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestNodeFailureImmediatelyCoolsRuntimeNode(t *testing.T) {
	cipher, _ := NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	repo := &fakeRepository{nodes: []storage.ProxyNode{testProxyNode(t, cipher, 1, "http://127.0.0.1:8001", 50, 1, 0)}}
	service := NewService(repo, cipher, Config{Enabled: true, HealthEnabled: false, FailureThreshold: 1, Cooldown: time.Minute, CooldownMax: time.Minute})
	service.jitter = func(wait, _ time.Duration) time.Duration { return wait }
	now := time.Now()
	service.now = func() time.Time { return now }
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	lease, err := service.Acquire(context.Background(), ProxyRequest{TargetType: "platform", TargetKey: "quark"})
	if err != nil {
		t.Fatal(err)
	}
	lease.Release(ProxyOutcome{FailureScope: FailureScopeNode})
	if _, err := service.Acquire(context.Background(), ProxyRequest{TargetType: "platform", TargetKey: "baidu"}); !errors.Is(err, ErrNoProxy) {
		t.Fatalf("cooled node remained selectable: %v", err)
	}
	now = now.Add(2 * time.Minute)
	recovered, err := service.Acquire(context.Background(), ProxyRequest{TargetType: "platform", TargetKey: "baidu"})
	if err != nil {
		t.Fatalf("expired cooldown did not recover node: %v", err)
	}
	recovered.Release(ProxyOutcome{Success: true, Latency: time.Millisecond})
	outcomes := repo.recordedOutcomes()
	if len(outcomes) != 2 || outcomes[0].CooldownUntil == nil || outcomes[0].FailureScope != FailureScopeNode || !outcomes[1].Success {
		t.Fatalf("unexpected persisted outcome: %+v", outcomes)
	}
}

func TestTargetCooldownDoesNotBlockOtherTargets(t *testing.T) {
	cipher, _ := NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	repo := &fakeRepository{nodes: []storage.ProxyNode{testProxyNode(t, cipher, 1, "http://127.0.0.1:8001", 50, 1, 0)}}
	service := NewService(repo, cipher, Config{Enabled: true, HealthEnabled: false, FailureThreshold: 1, Cooldown: time.Minute, CooldownMax: time.Minute})
	service.jitter = func(wait, _ time.Duration) time.Duration { return wait }
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	lease, err := service.Acquire(context.Background(), ProxyRequest{TargetType: "platform", TargetKey: "quark"})
	if err != nil {
		t.Fatal(err)
	}
	lease.Release(ProxyOutcome{FailureScope: FailureScopeTarget})
	if _, err := service.Acquire(context.Background(), ProxyRequest{TargetType: "platform", TargetKey: "quark"}); !errors.Is(err, ErrNoProxy) {
		t.Fatalf("target cooldown was not enforced: %v", err)
	}
	other, err := service.Acquire(context.Background(), ProxyRequest{TargetType: "platform", TargetKey: "baidu"})
	if err != nil {
		t.Fatalf("target cooldown leaked to another platform: %v", err)
	}
	other.Release(ProxyOutcome{Success: true})
}

func TestTargetStatsInfluenceSelection(t *testing.T) {
	cipher, _ := NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	repo := &fakeRepository{
		nodes: []storage.ProxyNode{
			testProxyNode(t, cipher, 1, "http://127.0.0.1:8001", 10, 100, 0),
			testProxyNode(t, cipher, 2, "http://127.0.0.1:8002", 100, 1, 1),
		},
		targets: []storage.ProxyTargetStat{
			{ProxyID: 1, TargetKey: "platform:quark", LatencyMS: 1000, SuccessCount: 0, FailureCount: 20},
			{ProxyID: 2, TargetKey: "platform:quark", LatencyMS: 40, SuccessCount: 20, FailureCount: 0},
		},
	}
	service := NewService(repo, cipher, Config{Enabled: true, HealthEnabled: false})
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	lease, err := service.Acquire(context.Background(), ProxyRequest{TargetType: "platform", TargetKey: "quark"})
	if err != nil {
		t.Fatal(err)
	}
	if lease.ID() != 2 {
		t.Fatalf("target-aware selection chose proxy %d, want 2", lease.ID())
	}
	lease.Release(ProxyOutcome{Success: true})
}

func TestRoundRobinSelectionStrategy(t *testing.T) {
	cipher, _ := NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	repo := &fakeRepository{nodes: []storage.ProxyNode{
		testProxyNode(t, cipher, 1, "http://127.0.0.1:8001", 10, 10, 0),
		testProxyNode(t, cipher, 2, "http://127.0.0.1:8002", 10, 10, 0),
	}}
	service := NewService(repo, cipher, Config{Enabled: true, HealthEnabled: false, SelectionStrategy: SelectionRoundRobin})
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 0, 3)
	for range 3 {
		lease, err := service.Acquire(context.Background(), ProxyRequest{TargetType: "platform", TargetKey: "quark"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, lease.ID())
		lease.Release(ProxyOutcome{FailureScope: FailureScopeNone})
	}
	if ids[0] != 1 || ids[1] != 2 || ids[2] != 1 {
		t.Fatalf("round-robin order = %v", ids)
	}
	if outcomes := repo.recordedOutcomes(); len(outcomes) != 0 {
		t.Fatalf("neutral outcomes were persisted: %+v", outcomes)
	}
}

func TestStickyBindingExpiresAndReselects(t *testing.T) {
	cipher, _ := NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	repo := &fakeRepository{nodes: []storage.ProxyNode{
		testProxyNode(t, cipher, 1, "http://127.0.0.1:8001", 10, 10, 0),
		testProxyNode(t, cipher, 2, "http://127.0.0.1:8002", 100, 10, 0),
	}}
	service := NewService(repo, cipher, Config{Enabled: true, HealthEnabled: false, StickyTTL: time.Minute})
	now := time.Now()
	service.now = func() time.Time { return now }
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	request := ProxyRequest{TargetType: "platform", TargetKey: "quark", StickyKey: "resource-a"}
	first, err := service.Acquire(context.Background(), request)
	if err != nil || first.ID() != 1 {
		t.Fatalf("first sticky selection = %v, id=%d", err, first.ID())
	}
	first.Release(ProxyOutcome{Success: true})
	now = now.Add(2 * time.Minute)
	service.mu.Lock()
	service.nodes[1].node.LatencyMS = 5000
	service.nodes[1].node.SuccessCount = 0
	service.nodes[1].node.FailureCount = 20
	service.nodes[2].node.LatencyMS = 10
	service.nodes[2].node.SuccessCount = 20
	service.nodes[2].node.FailureCount = 0
	service.mu.Unlock()
	second, err := service.Acquire(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID() != 2 {
		t.Fatalf("expired sticky binding selected proxy %d, want 2", second.ID())
	}
	second.Release(ProxyOutcome{Success: true})
}

func TestCooldownEscalatesOnlyAfterWindowExpires(t *testing.T) {
	service := NewService(nil, nil, Config{FailureThreshold: 1, Cooldown: time.Second, CooldownMax: 8 * time.Second})
	service.jitter = func(wait, _ time.Duration) time.Duration { return wait }
	now := time.Now()
	level, first := service.nextFailureState(0, nil, 0, now)
	if level != 1 || first == nil || !first.Equal(now.Add(time.Second)) {
		t.Fatalf("first cooldown = level %d until %v", level, first)
	}
	level, same := service.nextFailureState(level, first, 0, now.Add(500*time.Millisecond))
	if level != 1 || same == nil || !same.Equal(*first) {
		t.Fatalf("in-window failure escalated: level %d until %v", level, same)
	}
	level, second := service.nextFailureState(level, first, 0, now.Add(2*time.Second))
	if level != 2 || second == nil || !second.Equal(now.Add(4*time.Second)) {
		t.Fatalf("post-window cooldown = level %d until %v", level, second)
	}
}
