package storage

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestCleanupTerminalResources(t *testing.T) {
	now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	store := newPostgresTestStore(t, now)
	ctx := context.Background()
	old := now.Add(-61 * 24 * time.Hour)
	recent := now.Add(-time.Hour)

	type fixture struct {
		name       string
		status     string
		checkedAt  time.Time
		lastSeenAt time.Time
	}
	fixtures := []fixture{
		{name: "old-invalid", status: CheckInvalid, checkedAt: old, lastSeenAt: old},
		{name: "old-expired", status: CheckExpired, checkedAt: old, lastSeenAt: old},
		{name: "rediscovered", status: CheckInvalid, checkedAt: old, lastSeenAt: recent},
		{name: "recent-invalid", status: CheckInvalid, checkedAt: recent, lastSeenAt: old},
		{name: "old-valid", status: CheckValid, checkedAt: old, lastSeenAt: old},
	}

	for index, item := range fixtures {
		url := fmt.Sprintf("https://retention.example/%d", index)
		var resourceID int64
		if err := store.pool.QueryRow(ctx, `INSERT INTO resources (
			normalized_url, url, check_status, last_checked_at, first_seen_at, last_seen_at
		) VALUES ($1, $1, $2, $3, $4, $4) RETURNING id`, url, item.status, item.checkedAt, item.lastSeenAt).Scan(&resourceID); err != nil {
			t.Fatalf("insert %s: %v", item.name, err)
		}
		if _, err := store.pool.Exec(ctx, `INSERT INTO resource_sources (
			resource_id, source_type, source_key, source_identity, discovered_at, first_seen_at, last_seen_at
		) VALUES ($1, 'plugin', 'retention', $2, $3, $3, $3)`, resourceID, item.name, item.lastSeenAt); err != nil {
			t.Fatalf("insert source %s: %v", item.name, err)
		}
	}

	deleted, err := store.CleanupTerminalResources(ctx, now, 60*24*time.Hour, 1, 10)
	if err != nil || deleted != 2 {
		t.Fatalf("cleanup deleted=%d err=%v, want 2", deleted, err)
	}
	var resources, sources int
	if err := store.pool.QueryRow(ctx, "SELECT count(*) FROM resources").Scan(&resources); err != nil {
		t.Fatalf("count resources: %v", err)
	}
	if err := store.pool.QueryRow(ctx, "SELECT count(*) FROM resource_sources").Scan(&sources); err != nil {
		t.Fatalf("count sources: %v", err)
	}
	if resources != 3 || sources != 3 {
		t.Fatalf("remaining resources/sources = %d/%d, want 3/3", resources, sources)
	}
}
