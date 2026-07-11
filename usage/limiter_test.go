package usage

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

func TestLimiterDefaultsAndSharedUserWindows(t *testing.T) {
	start := time.Date(2026, 7, 11, 10, 0, 0, int(250*time.Millisecond), time.UTC)
	clock := &fakeClock{now: start}
	limiter := newLimiter(LimitConfig{}, clock.Now)
	if got := limiter.Config(); got.RequestsPerSecond != 3 || got.RequestsPerMinute != 60 {
		t.Fatalf("default config = %+v", got)
	}

	for remaining := 2; remaining >= 0; remaining-- {
		decision := limiter.Allow("user-a")
		if !decision.Allowed || decision.Remaining != remaining || decision.Second.Remaining != remaining {
			t.Fatalf("decision with %d remaining = %+v", remaining, decision)
		}
	}
	denied := limiter.Allow("user-a")
	if denied.Allowed || denied.Remaining != 0 || denied.RetryAfter != 750*time.Millisecond {
		t.Fatalf("denied decision = %+v", denied)
	}
	if !denied.ResetAt.Equal(start.Truncate(time.Second).Add(time.Second)) {
		t.Fatalf("reset at = %v", denied.ResetAt)
	}
	if other := limiter.Allow("user-b"); !other.Allowed || other.Remaining != 2 {
		t.Fatalf("independent user decision = %+v", other)
	}

	clock.Advance(750 * time.Millisecond)
	if reset := limiter.Allow("user-a"); !reset.Allowed || reset.Second.Remaining != 2 || reset.Minute.Remaining != 56 {
		t.Fatalf("second-window reset decision = %+v", reset)
	}
}

func TestLimiterMinuteWindowRetry(t *testing.T) {
	start := time.Date(2026, 7, 11, 10, 0, 30, 0, time.UTC)
	clock := &fakeClock{now: start}
	limiter := newLimiter(LimitConfig{RequestsPerSecond: 100, RequestsPerMinute: 2}, clock.Now)
	if !limiter.Allow("user").Allowed || !limiter.Allow("user").Allowed {
		t.Fatal("first two requests should be allowed")
	}
	denied := limiter.Allow("user")
	if denied.Allowed || denied.RetryAfter != 30*time.Second || !denied.ResetAt.Equal(start.Truncate(time.Minute).Add(time.Minute)) {
		t.Fatalf("minute denial = %+v", denied)
	}
	clock.Advance(30 * time.Second)
	if next := limiter.Allow("user"); !next.Allowed || next.Minute.Remaining != 1 {
		t.Fatalf("minute reset decision = %+v", next)
	}
}

func TestLimiterHotUpdateClearsStateAndSupportsUnlimited(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)}
	limiter := newLimiter(LimitConfig{RequestsPerSecond: 1, RequestsPerMinute: 1}, clock.Now)
	if !limiter.Allow("user").Allowed || limiter.Allow("user").Allowed {
		t.Fatal("initial limit was not enforced")
	}

	limiter.UpdateConfig(LimitConfig{Unlimited: true})
	for i := 0; i < 100; i++ {
		decision := limiter.Allow("user")
		if !decision.Allowed || !decision.Unlimited || decision.Remaining != -1 || decision.RetryAfter != 0 {
			t.Fatalf("unlimited decision = %+v", decision)
		}
	}

	limiter.UpdateConfig(LimitConfig{RequestsPerSecond: 1, RequestsPerMinute: 1})
	if fresh := limiter.Allow("user"); !fresh.Allowed || fresh.Remaining != 0 {
		t.Fatalf("hot-updated fresh decision = %+v", fresh)
	}
	if limiter.Allow("user").Allowed {
		t.Fatal("updated limit was not enforced")
	}
	limiter.UpdateConfig(LimitConfig{RequestsPerSecond: 1, RequestsPerMinute: 1})
	if !limiter.Allow("user").Allowed {
		t.Fatal("reapplying config did not clear state")
	}
}

func TestLimiterConcurrentRequestsShareUserBudget(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)}
	limiter := newLimiter(LimitConfig{RequestsPerSecond: 10, RequestsPerMinute: 10}, clock.Now)
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if limiter.Allow("shared-user").Allowed {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := allowed.Load(); got != 10 {
		t.Fatalf("allowed requests = %d, want 10", got)
	}
}

func TestLimiterAllowWithPerUserConfig(t *testing.T) {
	now := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	limiter := newLimiter(DefaultLimitConfig(), func() time.Time { return now })

	strict := LimitConfig{RequestsPerSecond: 1, RequestsPerMinute: 2}
	if decision := limiter.AllowWithConfig("strict", strict); !decision.Allowed {
		t.Fatal("first strict request should be allowed")
	}
	if decision := limiter.AllowWithConfig("strict", strict); decision.Allowed {
		t.Fatal("second strict request in the same second should be rejected")
	}

	wide := LimitConfig{RequestsPerSecond: 5, RequestsPerMinute: 10}
	for i := 0; i < 5; i++ {
		if decision := limiter.AllowWithConfig("wide", wide); !decision.Allowed {
			t.Fatalf("wide request %d unexpectedly rejected", i+1)
		}
	}
}

func TestLimiterChangingUserConfigResetsOnlyThatUser(t *testing.T) {
	now := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	limiter := newLimiter(DefaultLimitConfig(), func() time.Time { return now })
	strict := LimitConfig{RequestsPerSecond: 1, RequestsPerMinute: 2}
	wide := LimitConfig{RequestsPerSecond: 2, RequestsPerMinute: 4}

	limiter.AllowWithConfig("alice", strict)
	limiter.AllowWithConfig("bob", strict)
	if decision := limiter.AllowWithConfig("alice", wide); !decision.Allowed || decision.Second.Remaining != 1 {
		t.Fatalf("alice decision after config change = %#v", decision)
	}
	if decision := limiter.AllowWithConfig("bob", strict); decision.Allowed {
		t.Fatal("bob counters should not reset when alice changes")
	}
}
