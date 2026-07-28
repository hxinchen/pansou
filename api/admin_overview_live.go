package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"reflect"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"pansou/model"
	"pansou/storage"
)

const (
	defaultAdminOverviewActivityInterval  = time.Second
	defaultAdminOverviewCountersInterval  = 5 * time.Second
	defaultAdminOverviewHeartbeatInterval = 20 * time.Second
	defaultAdminOverviewLiveQueryTimeout  = 2 * time.Second
	defaultAdminOverviewMaxSubscribers    = 8
)

var errAdminOverviewSubscriberLimit = errors.New("overview stream subscriber limit reached")

type adminOverviewLiveStore interface {
	OverviewCounters(context.Context) (storage.OverviewCounters, error)
	OverviewActivity(context.Context) (*storage.CollectionRun, []storage.CollectionRun, error)
}

type adminOverviewActivityPayload struct {
	ActiveRun  *storage.CollectionRun  `json:"active_run,omitempty"`
	RecentRuns []storage.CollectionRun `json:"recent_runs"`
	ObservedAt time.Time               `json:"observed_at"`
}

type adminOverviewCountersPayload struct {
	storage.OverviewCounters
	ObservedAt time.Time `json:"observed_at"`
}

type adminOverviewLivePayload struct {
	Activity adminOverviewActivityPayload `json:"activity"`
	Counters adminOverviewCountersPayload `json:"counters"`
}

type adminOverviewStreamStatus struct {
	Scope      string    `json:"scope"`
	State      string    `json:"state"`
	ObservedAt time.Time `json:"observed_at"`
}

type adminOverviewHeartbeat struct {
	ObservedAt time.Time `json:"observed_at"`
}

type adminOverviewStreamEvent struct {
	id   uint64
	name string
	data any
}

type adminOverviewLiveHubConfig struct {
	activityInterval  time.Duration
	countersInterval  time.Duration
	heartbeatInterval time.Duration
	queryTimeout      time.Duration
	maxSubscribers    int
	now               func() time.Time
	onError           func(error)
}

type adminOverviewLiveHub struct {
	store adminOverviewLiveStore
	cfg   adminOverviewLiveHubConfig

	queryMu          sync.Mutex
	mu               sync.Mutex
	subscribers      map[uint64]chan adminOverviewStreamEvent
	nextSubscriberID uint64
	nextEventID      uint64
	running          bool
	cancel           context.CancelFunc
	latestActivity   *adminOverviewActivityPayload
	latestCounters   *adminOverviewCountersPayload
	activityDegraded bool
	countersDegraded bool
}

func newAdminOverviewLiveHub(store adminOverviewLiveStore) *adminOverviewLiveHub {
	return newAdminOverviewLiveHubWithConfig(store, adminOverviewLiveHubConfig{})
}

func newAdminOverviewLiveHubWithConfig(store adminOverviewLiveStore, cfg adminOverviewLiveHubConfig) *adminOverviewLiveHub {
	if store == nil {
		return nil
	}
	if cfg.activityInterval <= 0 {
		cfg.activityInterval = defaultAdminOverviewActivityInterval
	}
	if cfg.countersInterval <= 0 {
		cfg.countersInterval = defaultAdminOverviewCountersInterval
	}
	if cfg.heartbeatInterval <= 0 {
		cfg.heartbeatInterval = defaultAdminOverviewHeartbeatInterval
	}
	if cfg.queryTimeout <= 0 {
		cfg.queryTimeout = defaultAdminOverviewLiveQueryTimeout
	}
	if cfg.maxSubscribers <= 0 {
		cfg.maxSubscribers = defaultAdminOverviewMaxSubscribers
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	if cfg.onError == nil {
		cfg.onError = func(err error) { log.Printf("实时概览: %v", err) }
	}
	return &adminOverviewLiveHub{
		store:       store,
		cfg:         cfg,
		subscribers: make(map[uint64]chan adminOverviewStreamEvent),
	}
}

func (h *adminOverviewLiveHub) subscribe() (<-chan adminOverviewStreamEvent, func(), error) {
	if h == nil || h.store == nil {
		return nil, nil, fmt.Errorf("overview live hub is disabled")
	}
	h.mu.Lock()
	if len(h.subscribers) >= h.cfg.maxSubscribers {
		h.mu.Unlock()
		return nil, nil, errAdminOverviewSubscriberLimit
	}
	h.nextSubscriberID++
	subscriberID := h.nextSubscriberID
	stream := make(chan adminOverviewStreamEvent, 8)
	h.subscribers[subscriberID] = stream
	if h.latestActivity != nil {
		stream <- h.newEventLocked("activity", *h.latestActivity)
	}
	if h.latestCounters != nil {
		stream <- h.newEventLocked("counters", *h.latestCounters)
	}
	if !h.running {
		ctx, cancel := context.WithCancel(context.Background())
		h.cancel = cancel
		h.running = true
		go h.run(ctx)
	}
	h.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			h.mu.Lock()
			if current, ok := h.subscribers[subscriberID]; ok {
				delete(h.subscribers, subscriberID)
				close(current)
			}
			if len(h.subscribers) == 0 && h.running {
				h.running = false
				if h.cancel != nil {
					h.cancel()
					h.cancel = nil
				}
			}
			h.mu.Unlock()
		})
	}
	return stream, unsubscribe, nil
}

func (h *adminOverviewLiveHub) run(ctx context.Context) {
	h.sampleActivity(ctx)
	h.sampleCounters(ctx)
	activityTicker := time.NewTicker(h.cfg.activityInterval)
	countersTicker := time.NewTicker(h.cfg.countersInterval)
	heartbeatTicker := time.NewTicker(h.cfg.heartbeatInterval)
	defer activityTicker.Stop()
	defer countersTicker.Stop()
	defer heartbeatTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-activityTicker.C:
			h.sampleActivity(ctx)
		case <-countersTicker.C:
			h.sampleCounters(ctx)
		case <-heartbeatTicker.C:
			h.broadcast(adminOverviewStreamEvent{name: "heartbeat", data: adminOverviewHeartbeat{ObservedAt: h.cfg.now()}})
		}
	}
}

func (h *adminOverviewLiveHub) sampleActivity(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, h.cfg.queryTimeout)
	defer cancel()
	h.queryMu.Lock()
	active, recent, err := h.store.OverviewActivity(ctx)
	h.queryMu.Unlock()
	if err != nil {
		h.setScopeState("activity", true, fmt.Errorf("load activity: %w", err))
		return
	}
	payload := adminOverviewActivityPayload{ActiveRun: active, RecentRuns: recent, ObservedAt: h.cfg.now()}
	h.mu.Lock()
	changed := h.latestActivity == nil || !reflect.DeepEqual(h.latestActivity.ActiveRun, payload.ActiveRun) ||
		!reflect.DeepEqual(h.latestActivity.RecentRuns, payload.RecentRuns)
	h.latestActivity = &payload
	h.mu.Unlock()
	h.setScopeState("activity", false, nil)
	if changed {
		h.broadcast(adminOverviewStreamEvent{name: "activity", data: payload})
	}
}

func (h *adminOverviewLiveHub) sampleCounters(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, h.cfg.queryTimeout)
	defer cancel()
	h.queryMu.Lock()
	counters, err := h.store.OverviewCounters(ctx)
	h.queryMu.Unlock()
	if err != nil {
		h.setScopeState("counters", true, fmt.Errorf("load counters: %w", err))
		return
	}
	payload := adminOverviewCountersPayload{OverviewCounters: counters, ObservedAt: h.cfg.now()}
	h.mu.Lock()
	changed := h.latestCounters == nil || !reflect.DeepEqual(h.latestCounters.OverviewCounters, payload.OverviewCounters)
	h.latestCounters = &payload
	h.mu.Unlock()
	h.setScopeState("counters", false, nil)
	if changed {
		h.broadcast(adminOverviewStreamEvent{name: "counters", data: payload})
	}
}

func (h *adminOverviewLiveHub) setScopeState(scope string, degraded bool, err error) {
	h.mu.Lock()
	current := &h.activityDegraded
	if scope == "counters" {
		current = &h.countersDegraded
	}
	changed := *current != degraded
	*current = degraded
	h.mu.Unlock()
	if !changed {
		return
	}
	if err != nil {
		h.cfg.onError(err)
	}
	h.broadcast(adminOverviewStreamEvent{name: "status", data: adminOverviewStreamStatus{
		Scope: scope, State: map[bool]string{true: "degraded", false: "healthy"}[degraded], ObservedAt: h.cfg.now(),
	}})
}

func (h *adminOverviewLiveHub) broadcast(event adminOverviewStreamEvent) {
	h.mu.Lock()
	event = h.newEventLocked(event.name, event.data)
	for _, subscriber := range h.subscribers {
		select {
		case subscriber <- event:
		default:
			// Events carry complete state, so a slow client can safely wait for the next sample.
		}
	}
	h.mu.Unlock()
}

func (h *adminOverviewLiveHub) newEventLocked(name string, data any) adminOverviewStreamEvent {
	h.nextEventID++
	return adminOverviewStreamEvent{id: h.nextEventID, name: name, data: data}
}

func (h *adminOverviewLiveHub) load(ctx context.Context) (adminOverviewLivePayload, error) {
	h.queryMu.Lock()
	defer h.queryMu.Unlock()
	activityCtx, cancelActivity := context.WithTimeout(ctx, h.cfg.queryTimeout)
	active, recent, err := h.store.OverviewActivity(activityCtx)
	cancelActivity()
	if err != nil {
		return adminOverviewLivePayload{}, fmt.Errorf("load overview activity: %w", err)
	}
	countersCtx, cancelCounters := context.WithTimeout(ctx, h.cfg.queryTimeout)
	counters, err := h.store.OverviewCounters(countersCtx)
	cancelCounters()
	if err != nil {
		return adminOverviewLivePayload{}, fmt.Errorf("load overview counters: %w", err)
	}
	observedAt := h.cfg.now()
	return adminOverviewLivePayload{
		Activity: adminOverviewActivityPayload{ActiveRun: active, RecentRuns: recent, ObservedAt: observedAt},
		Counters: adminOverviewCountersPayload{OverviewCounters: counters, ObservedAt: observedAt},
	}, nil
}

func (h *AdminHandler) overviewLive(c *gin.Context) {
	if h == nil || h.overviewLiveHub == nil {
		c.JSON(http.StatusServiceUnavailable, model.NewErrorResponse(http.StatusServiceUnavailable, "实时概览不可用"))
		return
	}
	payload, err := h.overviewLiveHub.load(c.Request.Context())
	if err != nil {
		respondAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(payload))
}

func (h *AdminHandler) overviewStream(c *gin.Context) {
	if h == nil || h.overviewLiveHub == nil {
		c.JSON(http.StatusServiceUnavailable, model.NewErrorResponse(http.StatusServiceUnavailable, "实时概览不可用"))
		return
	}
	events, unsubscribe, err := h.overviewLiveHub.subscribe()
	if errors.Is(err, errAdminOverviewSubscriberLimit) {
		c.JSON(http.StatusTooManyRequests, model.NewErrorResponse(http.StatusTooManyRequests, "实时概览连接过多"))
		return
	}
	if err != nil {
		respondAdminError(c, err)
		return
	}
	defer unsubscribe()

	header := c.Writer.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-cache, no-store, no-transform")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	header.Set("X-Content-Type-Options", "nosniff")
	c.Status(http.StatusOK)
	_, _ = io.WriteString(c.Writer, ": connected\n\n")
	c.Writer.Flush()

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			if err := writeAdminOverviewSSE(c.Writer, event); err != nil {
				return
			}
			c.Writer.Flush()
		}
	}
}

func writeAdminOverviewSSE(writer io.Writer, event adminOverviewStreamEvent) error {
	data, err := json.Marshal(event.data)
	if err != nil {
		return fmt.Errorf("encode overview stream event: %w", err)
	}
	_, err = fmt.Fprintf(writer, "id: %d\nevent: %s\ndata: %s\n\n", event.id, event.name, data)
	return err
}
