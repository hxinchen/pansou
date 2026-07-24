package proxypool

import (
	"sort"
	"strings"
	"time"

	"pansou/storage"
)

func (s *Service) cleanupStickyLocked(now time.Time, force bool) {
	interval := 5 * time.Minute
	if halfTTL := s.cfg.StickyTTL / 2; halfTTL > 0 && halfTTL < interval {
		interval = halfTTL
	}
	if !force && len(s.sticky) <= s.cfg.StickyMaxEntries && !s.lastStickyCleanup.IsZero() && now.Sub(s.lastStickyCleanup) < interval {
		return
	}
	for key, entry := range s.sticky {
		target := key
		if index := strings.IndexByte(key, '\x00'); index >= 0 {
			target = key[:index]
		}
		node := s.nodes[entry.nodeID]
		if !entry.expiresAt.After(now) || !s.stickyNodeUsableLocked(node, target, now) {
			delete(s.sticky, key)
		}
	}
	if overflow := len(s.sticky) - s.cfg.StickyMaxEntries; overflow > 0 {
		type stickyItem struct {
			key      string
			lastUsed time.Time
		}
		items := make([]stickyItem, 0, len(s.sticky))
		for key, entry := range s.sticky {
			items = append(items, stickyItem{key: key, lastUsed: entry.lastUsed})
		}
		sort.Slice(items, func(i, j int) bool { return items[i].lastUsed.Before(items[j].lastUsed) })
		for i := 0; i < overflow; i++ {
			delete(s.sticky, items[i].key)
		}
	}
	s.lastStickyCleanup = now
}

func (s *Service) stickyNodeUsableLocked(node *runtimeProxy, target string, now time.Time) bool {
	if node == nil || !node.node.Enabled || !node.node.ExpiresAt.After(now) {
		return false
	}
	if node.node.Status != storage.ProxyStatusHealthy && node.node.Status != storage.ProxyStatusCooling {
		return false
	}
	if node.node.CooldownUntil != nil && node.node.CooldownUntil.After(now) {
		return false
	}
	if node.node.Status == storage.ProxyStatusCooling && node.node.CooldownUntil == nil {
		return false
	}
	if state := node.targets[target]; state != nil && state.cooldownUntil.After(now) {
		return false
	}
	return true
}

func (s *Service) invalidateStickyLocked(nodeID int64, target string) {
	prefix := target + "\x00"
	for key, entry := range s.sticky {
		if entry.nodeID != nodeID {
			continue
		}
		if target == "" || strings.HasPrefix(key, prefix) {
			delete(s.sticky, key)
		}
	}
}

func (s *Service) nextFailureState(consecutive int, current *time.Time, retryAfter time.Duration, now time.Time) (int, *time.Time) {
	if current != nil && current.After(now) {
		remaining := current.Sub(now)
		if retryAfter > remaining {
			until := now.Add(retryAfter)
			return consecutive, &until
		}
		return consecutive, copyTime(current)
	}
	consecutive++
	wait := s.failureCooldown(consecutive)
	if retryAfter > wait {
		wait = retryAfter
	}
	if wait <= 0 {
		return consecutive, nil
	}
	until := now.Add(wait)
	return consecutive, &until
}

func (s *Service) nextTargetFailureState(state *runtimeTarget, retryAfter time.Duration, now time.Time) (int, time.Time) {
	current := timePointer(state.cooldownUntil)
	consecutive, until := s.nextFailureState(state.consecutiveFailure, current, retryAfter, now)
	if until == nil {
		return consecutive, time.Time{}
	}
	return consecutive, *until
}

func (s *Service) failureCooldown(consecutive int) time.Duration {
	if consecutive < s.cfg.FailureThreshold {
		return 0
	}
	wait := s.cfg.Cooldown
	for level := consecutive - s.cfg.FailureThreshold; level > 0 && wait < s.cfg.CooldownMax; level-- {
		if wait > s.cfg.CooldownMax/2 {
			wait = s.cfg.CooldownMax
			break
		}
		wait *= 2
	}
	if wait > s.cfg.CooldownMax {
		wait = s.cfg.CooldownMax
	}
	if s.jitter != nil {
		wait = s.jitter(wait, s.cfg.CooldownJitter)
	}
	if wait > s.cfg.CooldownMax {
		wait = s.cfg.CooldownMax
	}
	return wait
}

func copyTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func timePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copyValue := value
	return &copyValue
}
