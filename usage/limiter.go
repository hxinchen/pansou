package usage

import (
	"sync"
	"time"
)

const (
	DefaultRequestsPerSecond = 3
	DefaultRequestsPerMinute = 60
)

type LimitConfig struct {
	RequestsPerSecond int  `json:"requests_per_second"`
	RequestsPerMinute int  `json:"requests_per_minute"`
	Unlimited         bool `json:"unlimited"`
}

func DefaultLimitConfig() LimitConfig {
	return LimitConfig{
		RequestsPerSecond: DefaultRequestsPerSecond,
		RequestsPerMinute: DefaultRequestsPerMinute,
	}
}

type WindowLimit struct {
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	ResetAt   time.Time `json:"reset_at,omitempty"`
}

type LimitDecision struct {
	Allowed    bool          `json:"allowed"`
	Unlimited  bool          `json:"unlimited,omitempty"`
	Remaining  int           `json:"remaining"`
	ResetAt    time.Time     `json:"reset_at,omitempty"`
	RetryAfter time.Duration `json:"retry_after,omitempty"`
	Second     WindowLimit   `json:"second"`
	Minute     WindowLimit   `json:"minute"`
}

type limitState struct {
	config       LimitConfig
	secondWindow time.Time
	secondCount  int
	minuteWindow time.Time
	minuteCount  int
}

// Limiter shares one RPS/RPM budget across every request carrying the same
// user ID. Windows are aligned fixed windows, making reset values stable for
// response headers and distributed configuration updates.
type Limiter struct {
	mu     sync.Mutex
	config LimitConfig
	states map[string]*limitState
	now    func() time.Time
}

func NewLimiter(config LimitConfig) *Limiter {
	return newLimiter(config, time.Now)
}

func newLimiter(config LimitConfig, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{
		config: normalizeLimitConfig(config),
		states: make(map[string]*limitState),
		now:    now,
	}
}

func (l *Limiter) Allow(userID string) LimitDecision {
	return l.AllowWithConfig(userID, l.Config())
}

// AllowWithConfig evaluates a user using their current database-backed limit.
// When that user's configuration changes, only their counters are reset.
func (l *Limiter) AllowWithConfig(userID string, requested LimitConfig) LimitDecision {
	l.mu.Lock()
	defer l.mu.Unlock()

	config := normalizeLimitConfig(requested)
	if config.Unlimited {
		delete(l.states, userID)
		return unlimitedDecision()
	}

	now := l.now()
	secondWindow := now.Truncate(time.Second)
	minuteWindow := now.Truncate(time.Minute)
	state := l.states[userID]
	if state == nil || state.config != config {
		state = &limitState{config: config}
		l.states[userID] = state
	}
	if !state.secondWindow.Equal(secondWindow) {
		state.secondWindow = secondWindow
		state.secondCount = 0
	}
	if !state.minuteWindow.Equal(minuteWindow) {
		state.minuteWindow = minuteWindow
		state.minuteCount = 0
	}

	secondReset := secondWindow.Add(time.Second)
	minuteReset := minuteWindow.Add(time.Minute)
	blockedSecond := state.secondCount >= config.RequestsPerSecond
	blockedMinute := state.minuteCount >= config.RequestsPerMinute
	if blockedSecond || blockedMinute {
		resetAt := time.Time{}
		if blockedSecond {
			resetAt = secondReset
		}
		if blockedMinute && (resetAt.IsZero() || minuteReset.After(resetAt)) {
			resetAt = minuteReset
		}
		return LimitDecision{
			Allowed:    false,
			Remaining:  minInt(remaining(config.RequestsPerSecond, state.secondCount), remaining(config.RequestsPerMinute, state.minuteCount)),
			ResetAt:    resetAt,
			RetryAfter: nonNegativeDuration(resetAt.Sub(now)),
			Second: WindowLimit{
				Limit: config.RequestsPerSecond, Remaining: remaining(config.RequestsPerSecond, state.secondCount), ResetAt: secondReset,
			},
			Minute: WindowLimit{
				Limit: config.RequestsPerMinute, Remaining: remaining(config.RequestsPerMinute, state.minuteCount), ResetAt: minuteReset,
			},
		}
	}

	state.secondCount++
	state.minuteCount++
	secondRemaining := remaining(config.RequestsPerSecond, state.secondCount)
	minuteRemaining := remaining(config.RequestsPerMinute, state.minuteCount)
	resetAt := restrictiveReset(secondRemaining, secondReset, minuteRemaining, minuteReset)
	return LimitDecision{
		Allowed:   true,
		Remaining: minInt(secondRemaining, minuteRemaining),
		ResetAt:   resetAt,
		Second: WindowLimit{
			Limit: config.RequestsPerSecond, Remaining: secondRemaining, ResetAt: secondReset,
		},
		Minute: WindowLimit{
			Limit: config.RequestsPerMinute, Remaining: minuteRemaining, ResetAt: minuteReset,
		},
	}
}

// UpdateConfig applies a hot configuration update and clears all counters so
// requests are never evaluated using a mixture of old and new limits.
func (l *Limiter) UpdateConfig(config LimitConfig) {
	l.mu.Lock()
	l.config = normalizeLimitConfig(config)
	l.states = make(map[string]*limitState)
	l.mu.Unlock()
}

func (l *Limiter) Config() LimitConfig {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.config
}

func (l *Limiter) Reset(userID string) {
	l.mu.Lock()
	delete(l.states, userID)
	l.mu.Unlock()
}

func normalizeLimitConfig(config LimitConfig) LimitConfig {
	if config.Unlimited {
		return LimitConfig{Unlimited: true}
	}
	if config.RequestsPerSecond <= 0 {
		config.RequestsPerSecond = DefaultRequestsPerSecond
	}
	if config.RequestsPerMinute <= 0 {
		config.RequestsPerMinute = DefaultRequestsPerMinute
	}
	return config
}

func unlimitedDecision() LimitDecision {
	window := WindowLimit{Remaining: -1}
	return LimitDecision{
		Allowed: true, Unlimited: true, Remaining: -1,
		Second: window, Minute: window,
	}
}

func remaining(limit, used int) int {
	if value := limit - used; value > 0 {
		return value
	}
	return 0
}

func restrictiveReset(secondRemaining int, secondReset time.Time, minuteRemaining int, minuteReset time.Time) time.Time {
	if secondRemaining < minuteRemaining {
		return secondReset
	}
	if minuteRemaining < secondRemaining {
		return minuteReset
	}
	if minuteReset.After(secondReset) {
		return minuteReset
	}
	return secondReset
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func nonNegativeDuration(value time.Duration) time.Duration {
	if value < 0 {
		return 0
	}
	return value
}
