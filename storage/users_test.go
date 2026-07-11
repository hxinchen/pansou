package storage

import (
	"testing"
	"time"
)

func TestNormalizeUsername(t *testing.T) {
	t.Parallel()
	if got := NormalizeUsername("  Alice\tSmith  "); got != "alice smith" {
		t.Fatalf("NormalizeUsername() = %q, want alice smith", got)
	}
}

func TestUserIsActiveAt(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	deleted := now
	tests := []struct {
		name string
		user User
		want bool
	}{
		{name: "active", user: User{Enabled: true}, want: true},
		{name: "future expiry", user: User{Enabled: true, ExpiresAt: &future}, want: true},
		{name: "expired", user: User{Enabled: true, ExpiresAt: &past}, want: false},
		{name: "disabled", user: User{Enabled: false}, want: false},
		{name: "deleted", user: User{Enabled: true, DeletedAt: &deleted}, want: false},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := test.user.IsActiveAt(now); got != test.want {
				t.Fatalf("IsActiveAt() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestUsageBucketAndRetentionDefaults(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	from, to, bucket := normalizeUsageRange(UsageStatsFilter{}, now)
	if !from.Equal(now.Add(-24*time.Hour)) || !to.Equal(now) || bucket != UsageBucketHour {
		t.Fatalf("normalizeUsageRange() = %v, %v, %q", from, to, bucket)
	}
	if got := APIRequestLogRetentionCutoff(now, 0); !got.Equal(now.Add(-30 * 24 * time.Hour)) {
		t.Fatalf("APIRequestLogRetentionCutoff() = %v", got)
	}
}
