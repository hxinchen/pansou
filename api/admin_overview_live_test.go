package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"pansou/storage"
)

type fakeAdminOverviewLiveStore struct {
	mu            sync.Mutex
	activityCalls int
	countersCalls int
	inFlight      int
	maxInFlight   int
	delay         time.Duration
	active        *storage.CollectionRun
	recent        []storage.CollectionRun
	counters      storage.OverviewCounters
}

func (s *fakeAdminOverviewLiveStore) begin(ctx context.Context) error {
	s.mu.Lock()
	s.inFlight++
	if s.inFlight > s.maxInFlight {
		s.maxInFlight = s.inFlight
	}
	delay := s.delay
	s.mu.Unlock()
	if delay > 0 {
		select {
		case <-ctx.Done():
			s.end()
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return nil
}

func (s *fakeAdminOverviewLiveStore) end() {
	s.mu.Lock()
	s.inFlight--
	s.mu.Unlock()
}

func (s *fakeAdminOverviewLiveStore) OverviewActivity(ctx context.Context) (*storage.CollectionRun, []storage.CollectionRun, error) {
	if err := s.begin(ctx); err != nil {
		return nil, nil, err
	}
	defer s.end()
	s.mu.Lock()
	s.activityCalls++
	active := s.active
	recent := append([]storage.CollectionRun(nil), s.recent...)
	s.mu.Unlock()
	return active, recent, nil
}

func (s *fakeAdminOverviewLiveStore) OverviewCounters(ctx context.Context) (storage.OverviewCounters, error) {
	if err := s.begin(ctx); err != nil {
		return storage.OverviewCounters{}, err
	}
	defer s.end()
	s.mu.Lock()
	s.countersCalls++
	counters := s.counters
	s.mu.Unlock()
	return counters, nil
}

func (s *fakeAdminOverviewLiveStore) calls() (int, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activityCalls, s.countersCalls, s.maxInFlight
}

func (s *fakeAdminOverviewLiveStore) setDelay(delay time.Duration) {
	s.mu.Lock()
	s.delay = delay
	s.mu.Unlock()
}

func waitOverviewEvent(t *testing.T, events <-chan adminOverviewStreamEvent, name string) adminOverviewStreamEvent {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("overview event stream closed")
			}
			if event.name == name {
				return event
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s event", name)
		}
	}
}

func TestAdminOverviewLiveHubSharesSamplerAndStopsWithLastSubscriber(t *testing.T) {
	store := &fakeAdminOverviewLiveStore{
		delay:    time.Millisecond,
		active:   &storage.CollectionRun{ID: 9, Status: "running"},
		recent:   []storage.CollectionRun{{ID: 8, Status: "success"}},
		counters: storage.OverviewCounters{ResourceCount: 42, TodayNew: 3},
	}
	hub := newAdminOverviewLiveHubWithConfig(store, adminOverviewLiveHubConfig{
		activityInterval:  10 * time.Millisecond,
		countersInterval:  20 * time.Millisecond,
		heartbeatInterval: time.Hour,
		queryTimeout:      100 * time.Millisecond,
		maxSubscribers:    2,
	})
	first, unsubscribeFirst, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	second, unsubscribeSecond, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	waitOverviewEvent(t, first, "activity")
	waitOverviewEvent(t, first, "counters")
	waitOverviewEvent(t, second, "activity")
	waitOverviewEvent(t, second, "counters")

	time.Sleep(35 * time.Millisecond)
	activityCalls, countersCalls, maxInFlight := store.calls()
	if activityCalls > 6 || countersCalls > 4 {
		t.Fatalf("subscriber count multiplied sampling: activity=%d counters=%d", activityCalls, countersCalls)
	}
	if maxInFlight != 1 {
		t.Fatalf("overlapping live queries = %d, want 1", maxInFlight)
	}

	unsubscribeFirst()
	beforeActivity, _, _ := store.calls()
	time.Sleep(25 * time.Millisecond)
	afterActivity, _, _ := store.calls()
	if afterActivity <= beforeActivity {
		t.Fatal("sampler stopped while one subscriber remained")
	}
	unsubscribeSecond()
	time.Sleep(15 * time.Millisecond)
	stoppedActivity, stoppedCounters, _ := store.calls()
	time.Sleep(35 * time.Millisecond)
	finalActivity, finalCounters, _ := store.calls()
	if finalActivity != stoppedActivity || finalCounters != stoppedCounters {
		t.Fatalf("sampler continued after final unsubscribe: before=%d/%d after=%d/%d", stoppedActivity, stoppedCounters, finalActivity, finalCounters)
	}
}

func TestAdminOverviewLiveHubOnlyBroadcastsChangedState(t *testing.T) {
	store := &fakeAdminOverviewLiveStore{counters: storage.OverviewCounters{ResourceCount: 5}}
	hub := newAdminOverviewLiveHubWithConfig(store, adminOverviewLiveHubConfig{
		activityInterval:  5 * time.Millisecond,
		countersInterval:  5 * time.Millisecond,
		heartbeatInterval: time.Hour,
		queryTimeout:      100 * time.Millisecond,
	})
	events, unsubscribe, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	waitOverviewEvent(t, events, "activity")
	waitOverviewEvent(t, events, "counters")
	select {
	case event := <-events:
		t.Fatalf("unchanged state produced event: %+v", event)
	case <-time.After(25 * time.Millisecond):
	}
}

func TestAdminOverviewLiveHubEnforcesSubscriberLimit(t *testing.T) {
	store := &fakeAdminOverviewLiveStore{}
	hub := newAdminOverviewLiveHubWithConfig(store, adminOverviewLiveHubConfig{
		activityInterval: time.Hour, countersInterval: time.Hour, heartbeatInterval: time.Hour, maxSubscribers: 1,
	})
	_, unsubscribe, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	if _, _, err := hub.subscribe(); err != errAdminOverviewSubscriberLimit {
		t.Fatalf("second subscriber error = %v", err)
	}
}

func TestAdminOverviewLiveHubReportsTimeoutAndRecovery(t *testing.T) {
	store := &fakeAdminOverviewLiveStore{delay: 20 * time.Millisecond}
	hub := newAdminOverviewLiveHubWithConfig(store, adminOverviewLiveHubConfig{
		activityInterval:  5 * time.Millisecond,
		countersInterval:  time.Hour,
		heartbeatInterval: 10 * time.Millisecond,
		queryTimeout:      5 * time.Millisecond,
		onError:           func(error) {},
	})
	events, unsubscribe, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	degraded := waitOverviewEvent(t, events, "status")
	degradedPayload := degraded.data.(adminOverviewStreamStatus)
	if degradedPayload.Scope != "activity" || degradedPayload.State != "degraded" {
		t.Fatalf("unexpected degraded event: %+v", degradedPayload)
	}
	store.setDelay(0)
	for {
		event := waitOverviewEvent(t, events, "status")
		payload := event.data.(adminOverviewStreamStatus)
		if payload.Scope == "activity" && payload.State == "healthy" {
			break
		}
	}
	heartbeat := waitOverviewEvent(t, events, "heartbeat")
	if heartbeat.id == 0 || heartbeat.data.(adminOverviewHeartbeat).ObservedAt.IsZero() {
		t.Fatalf("invalid heartbeat: %+v", heartbeat)
	}
}

func TestAdminOverviewLiveHubDoesNotBlockOnSlowSubscriber(t *testing.T) {
	store := &fakeAdminOverviewLiveStore{}
	hub := newAdminOverviewLiveHubWithConfig(store, adminOverviewLiveHubConfig{
		activityInterval: time.Millisecond, countersInterval: time.Millisecond, heartbeatInterval: time.Millisecond,
		queryTimeout: 50 * time.Millisecond,
	})
	_, unsubscribe, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	unsubscribe()
	activityCalls, countersCalls, _ := store.calls()
	if activityCalls < 5 || countersCalls < 5 {
		t.Fatalf("slow subscriber blocked sampler: activity=%d counters=%d", activityCalls, countersCalls)
	}
}

func TestAdminOverviewLiveEndpointReturnsLightweightPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &fakeAdminOverviewLiveStore{
		active:   &storage.CollectionRun{ID: 12, Status: "running"},
		recent:   []storage.CollectionRun{{ID: 11, Status: "partial"}},
		counters: storage.OverviewCounters{ResourceCount: 99, KeywordCount: 7},
	}
	handler := &AdminHandler{overviewLiveHub: newAdminOverviewLiveHub(store)}
	router := gin.New()
	router.GET("/overview/live", handler.overviewLive)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/overview/live", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Data adminOverviewLivePayload `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Activity.ActiveRun == nil || payload.Data.Activity.ActiveRun.ID != 12 || payload.Data.Counters.ResourceCount != 99 {
		t.Fatalf("unexpected live payload: %+v", payload.Data)
	}
	_, _, maxInFlight := store.calls()
	if maxInFlight != 1 {
		t.Fatalf("live endpoint query overlap = %d", maxInFlight)
	}
}

func TestAdminOverviewStreamHeadersAndFraming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &fakeAdminOverviewLiveStore{counters: storage.OverviewCounters{ResourceCount: 2}}
	handler := &AdminHandler{overviewLiveHub: newAdminOverviewLiveHubWithConfig(store, adminOverviewLiveHubConfig{
		activityInterval: time.Hour, countersInterval: time.Hour, heartbeatInterval: time.Hour,
	})}
	router := gin.New()
	router.GET("/overview/stream", handler.overviewStream)
	server := httptest.NewServer(router)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/overview/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("Content-Type=%q", got)
	}
	if got := response.Header.Get("Cache-Control"); !strings.Contains(got, "no-store") || !strings.Contains(got, "no-transform") {
		t.Fatalf("Cache-Control=%q", got)
	}
	if got := response.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering=%q", got)
	}

	scanner := bufio.NewScanner(response.Body)
	var lines []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" && strings.Contains(strings.Join(lines, "\n"), "event: counters") {
			break
		}
		lines = append(lines, line)
	}
	cancel()
	body := strings.Join(lines, "\n")
	if !strings.Contains(body, ": connected") || !strings.Contains(body, "event: activity") || !strings.Contains(body, "event: counters") || !strings.Contains(body, "observed_at") {
		t.Fatalf("unexpected stream body: %s", body)
	}
}

func TestWriteAdminOverviewSSEIncludesIDNameAndTime(t *testing.T) {
	var builder strings.Builder
	observedAt := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	err := writeAdminOverviewSSE(&builder, adminOverviewStreamEvent{
		id: 4, name: "heartbeat", data: adminOverviewHeartbeat{ObservedAt: observedAt},
	})
	if err != nil {
		t.Fatal(err)
	}
	output := builder.String()
	for _, fragment := range []string{"id: 4", "event: heartbeat", `"observed_at":"2026-07-28T12:00:00Z"`} {
		if !strings.Contains(output, fragment) {
			t.Fatalf("SSE output %q missing %q", output, fragment)
		}
	}
}
