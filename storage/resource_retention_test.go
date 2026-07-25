package storage

import (
	"testing"
	"time"
)

func TestTerminalResourceRetentionCutoff(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	if got, want := TerminalResourceRetentionCutoff(now, 30*24*time.Hour), now.Add(-30*24*time.Hour); !got.Equal(want) {
		t.Fatalf("cutoff = %v, want %v", got, want)
	}
	if got, want := TerminalResourceRetentionCutoff(now, 0), now.Add(-60*24*time.Hour); !got.Equal(want) {
		t.Fatalf("default cutoff = %v, want %v", got, want)
	}
}
