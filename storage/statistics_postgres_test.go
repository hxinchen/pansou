package storage

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestPostgresOverviewSnapshotActivityAndTrends(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load test location: %v", err)
	}
	now := time.Date(2026, time.July, 13, 10, 30, 0, 0, location)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	store := newPostgresTestStore(t, now)
	ctx := context.Background()

	var migrated bool
	if err := store.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM schema_migrations WHERE version = 9)`).Scan(&migrated); err != nil || !migrated {
		t.Fatalf("migration 9: migrated=%v err=%v", migrated, err)
	}
	var indexCount int
	if err := store.pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_indexes
		WHERE schemaname = current_schema()
			AND indexname = ANY($1::text[])`, []string{
		"resources_valid_first_seen_idx",
		"collection_run_items_completed_found_idx",
		"resource_sources_contribution_idx",
	}).Scan(&indexCount); err != nil || indexCount != 3 {
		t.Fatalf("overview indexes = %d, err=%v", indexCount, err)
	}

	type resourceFixture struct {
		status string
		seenAt time.Time
	}
	fixtures := []resourceFixture{
		{status: CheckValid, seenAt: today.AddDate(0, 0, -8)},
		{status: CheckInvalid, seenAt: today.AddDate(0, 0, -8).Add(time.Hour)},
		{status: CheckUnsupported, seenAt: today.AddDate(0, 0, -8).Add(2 * time.Hour)},
		{status: CheckValid, seenAt: today.AddDate(0, 0, -2)},
		{status: CheckPending, seenAt: today.AddDate(0, 0, -2).Add(23*time.Hour + 59*time.Minute)},
		{status: CheckUnknown, seenAt: today},
		{status: CheckValid, seenAt: today.AddDate(0, 0, 1)},
	}
	resourceIDs := make([]int64, 0, len(fixtures))
	for i, fixture := range fixtures {
		var id int64
		key := fmt.Sprintf("overview-resource-%d", i)
		if err := store.pool.QueryRow(ctx, `
			INSERT INTO resources (
				normalized_url, url, check_status, first_seen_at, last_seen_at
			) VALUES ($1, $2, $3, $4, $4)
			RETURNING id`, key, "https://example.com/"+key, fixture.status, fixture.seenAt).Scan(&id); err != nil {
			t.Fatalf("insert resource %d: %v", i, err)
		}
		resourceIDs = append(resourceIDs, id)
	}

	if _, err := store.pool.Exec(ctx, `
		INSERT INTO keywords (keyword, normalized_keyword, enabled) VALUES
			('Enabled', 'enabled', TRUE),
			('Disabled', 'disabled', FALSE)`); err != nil {
		t.Fatalf("insert keywords: %v", err)
	}
	for i, source := range []struct {
		resourceID int64
		sourceType string
		sourceKey  string
		count      int64
	}{
		{resourceID: resourceIDs[0], sourceType: "tg", sourceKey: "alpha", count: 2},
		{resourceID: resourceIDs[1], sourceType: "tg", sourceKey: "alpha", count: 3},
		{resourceID: resourceIDs[0], sourceType: "plugin", sourceKey: "beta", count: 10},
	} {
		if _, err := store.pool.Exec(ctx, `
			INSERT INTO resource_sources (
				resource_id, source_type, source_key, source_identity,
				discovered_at, first_seen_at, last_seen_at, discovery_count
			) VALUES ($1, $2, $3, $4, $5, $5, $5, $6)`,
			source.resourceID, source.sourceType, source.sourceKey,
			fmt.Sprintf("overview-source-%d", i), now, source.count); err != nil {
			t.Fatalf("insert source %d: %v", i, err)
		}
	}

	var historyRunID int64
	if err := store.pool.QueryRow(ctx, `
		INSERT INTO collection_runs (trigger, status, created_at, completed_at)
		VALUES ('scheduled', 'success', $1, $2)
		RETURNING id`, today.AddDate(0, 0, -3), today.Add(-time.Hour)).Scan(&historyRunID); err != nil {
		t.Fatalf("insert history run: %v", err)
	}
	for i, item := range []struct {
		completedAt *time.Time
		found       int
	}{
		{completedAt: timePointer(today.AddDate(0, 0, -2)), found: 5},
		{completedAt: timePointer(today.Add(2 * time.Hour)), found: 7},
		{completedAt: timePointer(today.AddDate(0, 0, 1)), found: 9},
		{completedAt: nil, found: 11},
	} {
		status := RunSuccess
		if item.completedAt == nil {
			status = RunPending
		}
		if _, err := store.pool.Exec(ctx, `
			INSERT INTO collection_run_items (
				run_id, keyword, normalized_keyword, status, found_count, completed_at
			) VALUES ($1, $2, $2, $3, $4, $5)`, historyRunID,
			fmt.Sprintf("trend-item-%d", i), status, item.found, item.completedAt); err != nil {
			t.Fatalf("insert run item %d: %v", i, err)
		}
	}

	var earliestActiveID, laterActiveID int64
	if err := store.pool.QueryRow(ctx, `
		INSERT INTO collection_runs (trigger, status, created_at)
		VALUES ('manual', 'pending', $1) RETURNING id`, today.Add(-2*time.Hour)).Scan(&earliestActiveID); err != nil {
		t.Fatalf("insert pending run: %v", err)
	}
	if err := store.pool.QueryRow(ctx, `
		INSERT INTO collection_runs (trigger, status, created_at, started_at)
		VALUES ('manual', 'running', $1, $1) RETURNING id`, today.Add(-time.Hour)).Scan(&laterActiveID); err != nil {
		t.Fatalf("insert running run: %v", err)
	}

	snapshot, err := store.OverviewSnapshot(ctx)
	if err != nil {
		t.Fatalf("OverviewSnapshot: %v", err)
	}
	if snapshot.ResourceCount != 7 || snapshot.TodayNew != 1 || snapshot.LastSevenDaysNew != 3 {
		t.Fatalf("snapshot resource counters = %+v", snapshot)
	}
	if snapshot.KeywordCount != 2 || snapshot.EnabledKeywordCount != 1 {
		t.Fatalf("snapshot keyword counters = %+v", snapshot)
	}
	wantStatuses := StatusCounts{
		CheckPending: 1, CheckValid: 3, CheckInvalid: 1, CheckUnknown: 1, CheckUnsupported: 1,
	}
	for status, want := range wantStatuses {
		if got := snapshot.StatusCounts[status]; got != want {
			t.Errorf("status %s = %d, want %d", status, got, want)
		}
	}
	if snapshot.ActiveRun != nil || snapshot.RecentRuns != nil {
		t.Fatalf("snapshot unexpectedly contains activity: %+v", snapshot)
	}
	if len(snapshot.TopSources) != 2 || snapshot.TopSources[0] != (SourceContribution{
		SourceType: "tg", SourceKey: "alpha", ResourceCount: 2, DiscoveryCount: 5,
	}) {
		t.Fatalf("snapshot top sources = %+v", snapshot.TopSources)
	}
	if got := snapshot.SourceTypeTotals["plugin"]; got != (SourceContributionTotal{
		SourceType: "plugin", ResourceCount: 1, DiscoveryCount: 10,
	}) {
		t.Fatalf("plugin source total = %+v", got)
	}
	if got := snapshot.SourceTypeTotals["tg"]; got != (SourceContributionTotal{
		SourceType: "tg", ResourceCount: 2, DiscoveryCount: 5,
	}) {
		t.Fatalf("tg source total = %+v", got)
	}
	if got := snapshot.TopSourcesByType["plugin"]; len(got) != 1 || got[0].SourceKey != "beta" {
		t.Fatalf("plugin top sources = %+v", got)
	}
	if got := snapshot.TopSourcesByType["tg"]; len(got) != 1 || got[0].SourceKey != "alpha" {
		t.Fatalf("tg top sources = %+v", got)
	}

	active, recent, err := store.OverviewActivity(ctx)
	if err != nil {
		t.Fatalf("OverviewActivity: %v", err)
	}
	if active == nil || active.ID != earliestActiveID {
		t.Fatalf("active run = %+v, want id %d (later id %d)", active, earliestActiveID, laterActiveID)
	}
	if len(recent) != 3 || recent[0].ID != laterActiveID || recent[1].ID != earliestActiveID {
		t.Fatalf("recent runs = %+v", recent)
	}

	overview, err := store.Overview(ctx)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if overview.ResourceCount != snapshot.ResourceCount || overview.ActiveRun == nil ||
		overview.ActiveRun.ID != earliestActiveID || len(overview.RecentRuns) != len(recent) {
		t.Fatalf("merged overview = %+v", overview)
	}

	trends, err := store.Trends(ctx, 3)
	if err != nil {
		t.Fatalf("Trends: %v", err)
	}
	if len(trends) != 3 {
		t.Fatalf("trend count = %d, want 3", len(trends))
	}
	wantNew := []int64{2, 0, 1}
	wantDiscoveries := []int64{5, 0, 7}
	wantValid := []int64{2, 2, 2}
	for i, point := range trends {
		wantDate := today.AddDate(0, 0, i-2)
		if !point.Date.Equal(wantDate) || point.NewCount != wantNew[i] ||
			point.NewResources != wantNew[i] || point.Discoveries != wantDiscoveries[i] ||
			point.ValidCount != wantValid[i] || point.ValidResources != wantValid[i] {
			t.Errorf("trend[%d] = %+v, want date=%v new=%d discoveries=%d valid=%d",
				i, point, wantDate, wantNew[i], wantDiscoveries[i], wantValid[i])
		}
	}
}
