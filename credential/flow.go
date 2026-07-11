package credential

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

var ErrFlowNotFound = errors.New("login flow not found")
var ErrRateLimited = errors.New("login flow rate limited")

type LoginFlow struct {
	ID, PluginKey, Scope string
	UserID               int64
	Value                any
	CreatedAt, ExpiresAt time.Time
}
type flowEntry struct {
	flow  LoginFlow
	polls []time.Time
}
type FlowStore struct {
	mu       sync.Mutex
	ttl      time.Duration
	maxPolls int
	window   time.Duration
	now      func() time.Time
	flows    map[string]*flowEntry
}

func NewFlowStore(ttl time.Duration, maxPolls int, window time.Duration) *FlowStore {
	return &FlowStore{ttl: ttl, maxPolls: maxPolls, window: window, now: time.Now, flows: map[string]*flowEntry{}}
}
func (s *FlowStore) Create(userID int64, plugin, scope string, value any) (LoginFlow, error) {
	b := make([]byte, 24)
	if _, e := rand.Read(b); e != nil {
		return LoginFlow{}, e
	}
	now := s.now()
	f := LoginFlow{ID: base64.RawURLEncoding.EncodeToString(b), UserID: userID, PluginKey: plugin, Scope: scope, Value: value, CreatedAt: now, ExpiresAt: now.Add(s.ttl)}
	s.mu.Lock()
	s.flows[f.ID] = &flowEntry{flow: f}
	s.mu.Unlock()
	return f, nil
}
func (s *FlowStore) Get(id string, userID int64) (LoginFlow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.flows[id]
	now := s.now()
	if !ok || e.flow.UserID != userID || !now.Before(e.flow.ExpiresAt) {
		delete(s.flows, id)
		return LoginFlow{}, ErrFlowNotFound
	}
	cut := now.Add(-s.window)
	i := 0
	for i < len(e.polls) && e.polls[i].Before(cut) {
		i++
	}
	e.polls = append(e.polls[i:], now)
	if s.maxPolls > 0 && len(e.polls) > s.maxPolls {
		return LoginFlow{}, ErrRateLimited
	}
	return e.flow, nil
}
func (s *FlowStore) Delete(id string) { s.mu.Lock(); delete(s.flows, id); s.mu.Unlock() }
