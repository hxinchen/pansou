package api

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"pansou/collection"
	"pansou/model"
	"pansou/storage"
)

const adminLinkCheckStatusCacheTTL = 5 * time.Second

type linkCheckRuntime interface {
	ObserveBacklog(int64, time.Time, string)
	Snapshot() collection.LinkCheckQueueSnapshot
}

type adminLinkCheckStatusCache struct {
	mu         sync.Mutex
	valid      bool
	expiresAt  time.Time
	policy     storage.LinkCheckPolicy
	dueCount   int64
	observedAt time.Time
}

func (cache *adminLinkCheckStatusCache) snapshot(ctx context.Context, store *storage.Store, at time.Time) (storage.LinkCheckPolicy, int64, time.Time, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.valid && at.Before(cache.expiresAt) {
		return cache.policy, cache.dueCount, cache.observedAt, nil
	}
	policy, err := store.GetLinkCheckPolicy(ctx)
	if err != nil {
		return storage.LinkCheckPolicy{}, 0, time.Time{}, err
	}
	dueCount, err := store.CountResourcesDueForCheck(ctx, policy, at)
	if err != nil {
		return storage.LinkCheckPolicy{}, 0, time.Time{}, err
	}
	cache.valid = true
	cache.expiresAt = at.Add(adminLinkCheckStatusCacheTTL)
	cache.policy = policy
	cache.dueCount = dueCount
	cache.observedAt = at
	return policy, dueCount, at, nil
}

func (cache *adminLinkCheckStatusCache) invalidate() {
	if cache == nil {
		return
	}
	cache.mu.Lock()
	cache.valid = false
	cache.mu.Unlock()
}

type adminLinkCheckStatusResponse struct {
	QueueStarted               bool      `json:"queue_started"`
	DueCount                   int64     `json:"due_count"`
	QueuedCount                int       `json:"queued_count"`
	ActiveCount                int       `json:"active_count"`
	WorkerCount                int       `json:"worker_count"`
	CompletedLastFiveMinutes   int64     `json:"completed_last_5m"`
	FailedLastFiveMinutes      int64     `json:"failed_last_5m"`
	ThroughputPerMinute        float64   `json:"throughput_per_minute"`
	NetDrainPerMinute          *float64  `json:"net_drain_per_minute,omitempty"`
	ETASeconds                 *int64    `json:"eta_seconds,omitempty"`
	ETAState                   string    `json:"eta_state"`
	MetricsWindowSeconds       int64     `json:"metrics_window_seconds"`
	MetricsSampleWindowSeconds int64     `json:"metrics_sample_window_seconds"`
	BacklogSampleWindowSeconds int64     `json:"backlog_sample_window_seconds"`
	ObservedAt                 time.Time `json:"observed_at"`
}

func (h *AdminHandler) getLinkCheckStatus(c *gin.Context) {
	if h == nil || h.store == nil {
		c.JSON(http.StatusServiceUnavailable, model.NewErrorResponse(http.StatusServiceUnavailable, "资源库未配置"))
		return
	}
	if h.linkChecks == nil {
		c.JSON(http.StatusServiceUnavailable, model.NewErrorResponse(http.StatusServiceUnavailable, "资源检测队列未配置"))
		return
	}
	at := time.Now().UTC()
	if h.now != nil {
		at = h.now().UTC()
	}
	policy, dueCount, observedAt, err := h.linkCheckStatusCache.snapshot(c.Request.Context(), h.store, at)
	if err != nil {
		respondAdminError(c, err)
		return
	}

	h.linkChecks.ObserveBacklog(dueCount, observedAt, policy.Revision())
	runtime := h.linkChecks.Snapshot()
	c.JSON(http.StatusOK, model.NewSuccessResponse(adminLinkCheckStatusResponse{
		QueueStarted:               runtime.Started,
		DueCount:                   dueCount,
		QueuedCount:                runtime.Queued,
		ActiveCount:                runtime.Active,
		WorkerCount:                runtime.Workers,
		CompletedLastFiveMinutes:   runtime.CompletedLastFiveMinutes,
		FailedLastFiveMinutes:      runtime.FailedLastFiveMinutes,
		ThroughputPerMinute:        runtime.ThroughputPerMinute,
		NetDrainPerMinute:          runtime.NetDrainPerMinute,
		ETASeconds:                 runtime.ETASeconds,
		ETAState:                   runtime.ETAState,
		MetricsWindowSeconds:       int64(runtime.MetricsWindow / time.Second),
		MetricsSampleWindowSeconds: int64(runtime.MetricsSampleWindow / time.Second),
		BacklogSampleWindowSeconds: int64(runtime.BacklogSampleWindow / time.Second),
		ObservedAt:                 observedAt,
	}))
}
