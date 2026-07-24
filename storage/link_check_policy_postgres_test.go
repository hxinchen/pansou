package storage

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"pansou/model"
)

func TestPostgresLinkCheckPolicyMigrationAndUpdate(t *testing.T) {
	now := time.Date(2026, time.July, 15, 8, 0, 0, 0, time.UTC)
	store := newPostgresTestStore(t, now)
	ctx := context.Background()

	var migrationCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations
		WHERE version BETWEEN 14 AND 18`).Scan(&migrationCount); err != nil || migrationCount != 5 {
		t.Fatalf("link-check migrations: count=%v err=%v", migrationCount, err)
	}
	var candidateColumns int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema=current_schema() AND table_name='resources'
			AND column_name IN ('candidate_check_status','candidate_checked_at')`).Scan(&candidateColumns); err != nil || candidateColumns != 2 {
		t.Fatalf("candidate columns = %d, err=%v", candidateColumns, err)
	}
	var candidateIndexExists bool
	if err := store.pool.QueryRow(ctx, `SELECT to_regclass('resources_candidate_check_due_idx') IS NOT NULL`).Scan(&candidateIndexExists); err != nil || !candidateIndexExists {
		t.Fatalf("candidate due index: exists=%v err=%v", candidateIndexExists, err)
	}

	policy, err := store.GetLinkCheckPolicy(ctx)
	if err != nil {
		t.Fatalf("GetLinkCheckPolicy(default): %v", err)
	}
	if policy.Enabled || policy.IntervalSeconds != DefaultLinkCheckIntervalSeconds ||
		!reflect.DeepEqual(policy.Statuses, []string{CheckValid, CheckUnknown}) {
		t.Fatalf("default policy = %+v", policy)
	}

	updated, err := store.UpdateLinkCheckPolicy(ctx, UpdateLinkCheckPolicyInput{
		Enabled:         true,
		Statuses:        []string{" locked ", CheckValid, CheckUnknown, CheckValid},
		IntervalSeconds: 24 * 3600,
	})
	if err != nil {
		t.Fatalf("UpdateLinkCheckPolicy: %v", err)
	}
	if !updated.Enabled || updated.IntervalSeconds != 24*3600 ||
		!reflect.DeepEqual(updated.Statuses, []string{CheckValid, CheckUnknown, CheckLocked}) || !updated.UpdatedAt.Equal(now) {
		t.Fatalf("updated policy = %+v", updated)
	}
	got, err := store.GetLinkCheckPolicy(ctx)
	if err != nil {
		t.Fatalf("GetLinkCheckPolicy(updated): %v", err)
	}
	if !reflect.DeepEqual(got, updated) {
		t.Fatalf("persisted policy = %+v, want %+v", got, updated)
	}

	if _, err := store.UpdateLinkCheckPolicy(ctx, UpdateLinkCheckPolicyInput{
		Enabled: true, IntervalSeconds: DefaultLinkCheckIntervalSeconds,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("enabled empty statuses error = %v, want ErrInvalid", err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE link_check_policy SET interval_seconds=3601 WHERE singleton=TRUE`); err == nil {
		t.Fatal("database accepted a partial-hour interval")
	}
	if _, err := store.pool.Exec(ctx, `UPDATE link_check_policy SET statuses=ARRAY['unsupported']::text[] WHERE singleton=TRUE`); err == nil {
		t.Fatal("database accepted an unsupported status")
	}
	if _, err := store.pool.Exec(ctx, `UPDATE link_check_policy SET enabled=TRUE, statuses=ARRAY[]::text[] WHERE singleton=TRUE`); err == nil {
		t.Fatal("database accepted an enabled policy without statuses")
	}
}

func TestPostgresListResourcesDueForCheck(t *testing.T) {
	at := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	store := newPostgresTestStore(t, at)
	ctx := context.Background()

	type dueFixture struct {
		url                string
		status             string
		lastCheckedAt      *time.Time
		candidateStatus    *string
		candidateCheckedAt *time.Time
		firstSeenAt        time.Time
	}
	timePtr := func(value time.Time) *time.Time { return &value }
	stringPtr := func(value string) *string { return &value }
	fixtures := []dueFixture{
		{url: "https://due.example/pending", status: CheckPending, firstSeenAt: at.Add(-time.Hour)},
		{url: "https://due.example/pending-confirmation", status: CheckPending, lastCheckedAt: timePtr(at.Add(-time.Hour)), candidateStatus: stringPtr(CheckInvalid), candidateCheckedAt: timePtr(at.Add(-time.Hour)), firstSeenAt: at.Add(-2 * time.Hour)},
		{url: "https://due.example/confirmation", status: CheckValid, lastCheckedAt: timePtr(at.Add(-time.Hour)), candidateStatus: stringPtr(CheckInvalid), candidateCheckedAt: timePtr(at.Add(-time.Hour)), firstSeenAt: at.Add(-30 * 24 * time.Hour)},
		{url: "https://due.example/never", status: CheckUnknown, firstSeenAt: at.Add(-20 * 24 * time.Hour)},
		{url: "https://due.example/oldest", status: CheckValid, lastCheckedAt: timePtr(at.Add(-9 * 24 * time.Hour)), firstSeenAt: at.Add(-20 * 24 * time.Hour)},
		{url: "https://due.example/cutoff", status: CheckUnknown, lastCheckedAt: timePtr(at.Add(-7 * 24 * time.Hour)), firstSeenAt: at.Add(-20 * 24 * time.Hour)},
		{url: "https://due.example/recent", status: CheckValid, lastCheckedAt: timePtr(at.Add(-6 * 24 * time.Hour)), firstSeenAt: at.Add(-20 * 24 * time.Hour)},
		{url: "https://due.example/unselected", status: CheckLocked, lastCheckedAt: timePtr(at.Add(-30 * 24 * time.Hour)), firstSeenAt: at.Add(-40 * 24 * time.Hour)},
		{url: "https://due.example/early-confirmation", status: CheckValid, lastCheckedAt: timePtr(at.Add(-59 * time.Minute)), candidateStatus: stringPtr(CheckExpired), candidateCheckedAt: timePtr(at.Add(-59 * time.Minute)), firstSeenAt: at.Add(-30 * 24 * time.Hour)},
		{url: "https://due.example/early-pending-confirmation", status: CheckPending, lastCheckedAt: timePtr(at.Add(-59 * time.Minute)), candidateStatus: stringPtr(CheckExpired), candidateCheckedAt: timePtr(at.Add(-59 * time.Minute)), firstSeenAt: at.Add(-2 * time.Hour)},
	}
	for _, fixture := range fixtures {
		if _, err := store.pool.Exec(ctx, `INSERT INTO resources (
			normalized_url,url,check_status,last_checked_at,candidate_check_status,candidate_checked_at,
			first_seen_at,last_seen_at
		) VALUES($1,$1,$2,$3,$4,$5,$6,$6)`, fixture.url, fixture.status, fixture.lastCheckedAt,
			fixture.candidateStatus, fixture.candidateCheckedAt, fixture.firstSeenAt); err != nil {
			t.Fatalf("insert %s: %v", fixture.url, err)
		}
	}

	policy := LinkCheckPolicy{
		Enabled: true, Statuses: []string{CheckUnknown, CheckValid}, IntervalSeconds: DefaultLinkCheckIntervalSeconds,
	}
	dueCount, err := store.CountResourcesDueForCheck(ctx, policy, at)
	if err != nil {
		t.Fatalf("CountResourcesDueForCheck: %v", err)
	}
	if dueCount != 6 {
		t.Fatalf("due count = %d, want 6", dueCount)
	}
	limited, err := store.ListResourcesDueForCheck(ctx, policy, 2, at)
	if err != nil {
		t.Fatalf("ListResourcesDueForCheck(limit=2): %v", err)
	}
	if len(limited) != 2 || dueCount != 6 {
		t.Fatalf("limited due resources = %d, exact count = %d; want 2 and 6", len(limited), dueCount)
	}
	due, err := store.ListResourcesDueForCheck(ctx, policy, 500, at)
	if err != nil {
		t.Fatalf("ListResourcesDueForCheck: %v", err)
	}
	gotURLs := make([]string, len(due))
	for index := range due {
		gotURLs[index] = due[index].URL
	}
	wantURLs := []string{
		"https://due.example/pending",
		"https://due.example/pending-confirmation",
		"https://due.example/confirmation",
		"https://due.example/never",
		"https://due.example/oldest",
		"https://due.example/cutoff",
	}
	if !reflect.DeepEqual(gotURLs, wantURLs) {
		t.Fatalf("due URLs = %v, want %v", gotURLs, wantURLs)
	}
	if due[2].CandidateCheckStatus == nil || *due[2].CandidateCheckStatus != CheckInvalid ||
		due[2].CandidateCheckedAt == nil || !due[2].CandidateCheckedAt.Equal(at.Add(-time.Hour)) {
		t.Fatalf("confirmation candidate not scanned: %+v", due[2])
	}

	disabled := LinkCheckPolicy{
		Enabled: false, Statuses: []string{CheckValid, CheckUnknown}, IntervalSeconds: DefaultLinkCheckIntervalSeconds,
	}
	disabledCount, err := store.CountResourcesDueForCheck(ctx, disabled, at)
	if err != nil {
		t.Fatalf("CountResourcesDueForCheck(disabled): %v", err)
	}
	if disabledCount != 2 {
		t.Fatalf("disabled due count = %d, want 2", disabledCount)
	}
	due, err = store.ListResourcesDueForCheck(ctx, disabled, 500, at)
	if err != nil {
		t.Fatalf("ListResourcesDueForCheck(disabled): %v", err)
	}
	if len(due) != 2 || due[0].URL != "https://due.example/pending" || due[1].URL != "https://due.example/pending-confirmation" {
		t.Fatalf("disabled due resources = %+v", due)
	}
}

func TestPostgresCompleteResourceCheckConfirmation(t *testing.T) {
	base := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	store := newPostgresTestStore(t, base)
	ctx := context.Background()
	initialCheck := base.Add(-24 * time.Hour)
	result, err := store.UpsertResource(ctx, ResourceInput{
		URL: "https://confirm.example/item", CheckStatus: CheckValid,
		LastCheckedAt: &initialCheck, DiscoveredAt: base.Add(-30 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("UpsertResource: %v", err)
	}
	id := result.Resource.ID

	if err := store.CompleteResourceCheck(ctx, id, CheckInvalid, base); err != nil {
		t.Fatalf("CompleteResourceCheck(first negative): %v", err)
	}
	resource := mustGetResource(t, store, id)
	assertResourceCheckState(t, resource, CheckValid, CheckInvalid, base, base)

	early := base.Add(30 * time.Minute)
	if err := store.CompleteResourceCheck(ctx, id, CheckExpired, early); err != nil {
		t.Fatalf("CompleteResourceCheck(early negative): %v", err)
	}
	resource = mustGetResource(t, store, id)
	assertResourceCheckState(t, resource, CheckValid, CheckExpired, base, early)

	confirmedAt := base.Add(time.Hour)
	if err := store.CompleteResourceCheck(ctx, id, CheckViolation, confirmedAt); err != nil {
		t.Fatalf("CompleteResourceCheck(confirm): %v", err)
	}
	resource = mustGetResource(t, store, id)
	assertResourceCheckState(t, resource, CheckViolation, "", time.Time{}, confirmedAt)

	negativeUpdateAt := confirmedAt.Add(time.Minute)
	if err := store.CompleteResourceCheck(ctx, id, CheckCancelled, negativeUpdateAt); err != nil {
		t.Fatalf("CompleteResourceCheck(already negative): %v", err)
	}
	resource = mustGetResource(t, store, id)
	assertResourceCheckState(t, resource, CheckCancelled, "", time.Time{}, negativeUpdateAt)

	recoveredAt := confirmedAt.Add(2 * time.Minute)
	if err := store.CompleteResourceCheck(ctx, id, CheckValid, recoveredAt); err != nil {
		t.Fatalf("CompleteResourceCheck(recover): %v", err)
	}
	resource = mustGetResource(t, store, id)
	assertResourceCheckState(t, resource, CheckValid, "", time.Time{}, recoveredAt)

	secondCandidateAt := confirmedAt.Add(3 * time.Minute)
	if err := store.CompleteResourceCheck(ctx, id, CheckInvalid, secondCandidateAt); err != nil {
		t.Fatalf("CompleteResourceCheck(second candidate): %v", err)
	}
	unknownAt := confirmedAt.Add(4 * time.Minute)
	if err := store.CompleteResourceCheck(ctx, id, CheckUnknown, unknownAt); err != nil {
		t.Fatalf("CompleteResourceCheck(unknown): %v", err)
	}
	resource = mustGetResource(t, store, id)
	assertResourceCheckState(t, resource, CheckUnknown, "", time.Time{}, unknownAt)

	thirdCandidateAt := confirmedAt.Add(5 * time.Minute)
	if err := store.CompleteResourceCheck(ctx, id, CheckInvalid, thirdCandidateAt); err != nil {
		t.Fatalf("CompleteResourceCheck(third candidate): %v", err)
	}
	directAt := confirmedAt.Add(6 * time.Minute)
	if err := store.UpdateResourceCheck(ctx, id, CheckLocked, directAt); err != nil {
		t.Fatalf("UpdateResourceCheck: %v", err)
	}
	resource = mustGetResource(t, store, id)
	assertResourceCheckState(t, resource, CheckLocked, "", time.Time{}, directAt)

	pendingResult, err := store.UpsertResource(ctx, ResourceInput{
		URL: "https://confirm.example/pending", DiscoveredAt: base,
	})
	if err != nil {
		t.Fatalf("UpsertResource(pending): %v", err)
	}
	pendingCheckedAt := base.Add(7 * time.Minute)
	if err := store.CompleteResourceCheck(ctx, pendingResult.Resource.ID, CheckInvalid, pendingCheckedAt); err != nil {
		t.Fatalf("CompleteResourceCheck(pending negative): %v", err)
	}
	pendingResource := mustGetResource(t, store, pendingResult.Resource.ID)
	assertResourceCheckState(t, pendingResource, CheckPending, CheckInvalid, pendingCheckedAt, pendingCheckedAt)
	pendingConfirmedAt := pendingCheckedAt.Add(time.Hour)
	if err := store.CompleteResourceCheck(ctx, pendingResult.Resource.ID, CheckExpired, pendingConfirmedAt); err != nil {
		t.Fatalf("CompleteResourceCheck(pending confirm): %v", err)
	}
	pendingResource = mustGetResource(t, store, pendingResult.Resource.ID)
	assertResourceCheckState(t, pendingResource, CheckExpired, "", time.Time{}, pendingConfirmedAt)
}

func TestPostgresCompleteResourceChecksBatchesAndRollsBackAtomically(t *testing.T) {
	base := time.Date(2026, time.July, 18, 18, 0, 0, 0, time.UTC)
	store := newPostgresTestStore(t, base)
	ctx := context.Background()
	first, err := store.UpsertResource(ctx, ResourceInput{
		URL: "https://batch-check.example/first", CheckStatus: CheckValid, DiscoveredAt: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.UpsertResource(ctx, ResourceInput{
		URL: "https://batch-check.example/second", CheckStatus: CheckPending, DiscoveredAt: base,
	})
	if err != nil {
		t.Fatal(err)
	}

	checkedAt := base.Add(time.Minute)
	if err := store.CompleteResourceChecks(ctx, []ResourceCheckCompletion{
		{ResourceID: first.Resource.ID, Status: CheckInvalid, CheckedAt: checkedAt},
		{ResourceID: second.Resource.ID, Status: CheckValid, CheckedAt: checkedAt},
	}); err != nil {
		t.Fatal(err)
	}
	firstResource := mustGetResource(t, store, first.Resource.ID)
	assertResourceCheckState(t, firstResource, CheckValid, CheckInvalid, checkedAt, checkedAt)
	secondResource := mustGetResource(t, store, second.Resource.ID)
	assertResourceCheckState(t, secondResource, CheckValid, "", time.Time{}, checkedAt)

	rollbackAt := checkedAt.Add(time.Minute)
	err = store.CompleteResourceChecks(ctx, []ResourceCheckCompletion{
		{ResourceID: second.Resource.ID, Status: CheckUnknown, CheckedAt: rollbackAt},
		{ResourceID: 0, Status: CheckValid, CheckedAt: rollbackAt},
	})
	if err == nil {
		t.Fatal("batch with invalid resource id unexpectedly succeeded")
	}
	secondResource = mustGetResource(t, store, second.Resource.ID)
	assertResourceCheckState(t, secondResource, CheckValid, "", time.Time{}, checkedAt)
}

func TestPostgresSearchResponseReturnsNewPendingCheckCandidates(t *testing.T) {
	base := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	store := newPostgresTestStore(t, base)
	ctx := context.Background()
	response := model.SearchResponse{Results: []model.SearchResult{{
		Title: "new resource",
		Links: []model.Link{{URL: "https://candidate.example/new", Type: "baidu"}},
	}}}

	first, err := store.UpsertSearchResponse(ctx, "candidate", DefaultKeywordType, "scheduled", response)
	if err != nil {
		t.Fatalf("UpsertSearchResponse(first): %v", err)
	}
	if first.Inserted != 1 || len(first.CheckCandidates) != 1 || first.CheckCandidates[0].CheckStatus != CheckPending {
		t.Fatalf("first summary = %+v", first)
	}
	second, err := store.UpsertSearchResponse(ctx, "candidate", DefaultKeywordType, "scheduled", response)
	if err != nil {
		t.Fatalf("UpsertSearchResponse(second): %v", err)
	}
	if second.Inserted != 0 || len(second.CheckCandidates) != 0 {
		t.Fatalf("second summary = %+v", second)
	}
}

func TestPostgresResourceRediscoveryDoesNotResetCheckStatus(t *testing.T) {
	base := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	store := newPostgresTestStore(t, base)
	ctx := context.Background()
	checkedAt := base.Add(-8 * 24 * time.Hour)
	first, err := store.UpsertResource(ctx, ResourceInput{
		URL: "https://rediscovery.example/item", CheckStatus: CheckInvalid,
		LastCheckedAt: &checkedAt, DiscoveredAt: checkedAt,
	})
	if err != nil {
		t.Fatalf("initial UpsertResource: %v", err)
	}
	rediscovered, err := store.UpsertResource(ctx, ResourceInput{
		URL: "https://rediscovery.example/item", DiscoveredAt: base,
	})
	if err != nil {
		t.Fatalf("rediscovery UpsertResource: %v", err)
	}
	if rediscovered.Resource.ID != first.Resource.ID || rediscovered.Resource.CheckStatus != CheckInvalid {
		t.Fatalf("rediscovered resource = %+v, want existing invalid resource", rediscovered.Resource)
	}
}

func mustGetResource(t *testing.T, store *Store, id int64) Resource {
	t.Helper()
	resource, err := store.GetResource(context.Background(), id)
	if err != nil {
		t.Fatalf("GetResource(%d): %v", id, err)
	}
	return resource
}

func assertResourceCheckState(t *testing.T, resource Resource, status, candidateStatus string, candidateAt, lastCheckedAt time.Time) {
	t.Helper()
	if resource.CheckStatus != status {
		t.Fatalf("check status = %q, want %q", resource.CheckStatus, status)
	}
	if resource.LastCheckedAt == nil || !resource.LastCheckedAt.Equal(lastCheckedAt) {
		t.Fatalf("last checked at = %v, want %v", resource.LastCheckedAt, lastCheckedAt)
	}
	if candidateStatus == "" {
		if resource.CandidateCheckStatus != nil || resource.CandidateCheckedAt != nil {
			t.Fatalf("candidate = %v at %v, want cleared", resource.CandidateCheckStatus, resource.CandidateCheckedAt)
		}
		return
	}
	if resource.CandidateCheckStatus == nil || *resource.CandidateCheckStatus != candidateStatus {
		t.Fatalf("candidate status = %v, want %q", resource.CandidateCheckStatus, candidateStatus)
	}
	if resource.CandidateCheckedAt == nil || !resource.CandidateCheckedAt.Equal(candidateAt) {
		t.Fatalf("candidate checked at = %v, want %v", resource.CandidateCheckedAt, candidateAt)
	}
}
