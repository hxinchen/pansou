package storage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestPostgresKeywordAPISourcesLifecycleAndSync(t *testing.T) {
	now := time.Date(2026, time.July, 12, 10, 0, 0, 0, time.UTC)
	store := newPostgresTestStore(t, now)
	ctx := context.Background()

	var migrated bool
	if err := store.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version=7)").Scan(&migrated); err != nil || !migrated {
		t.Fatalf("migration 7: migrated=%v err=%v", migrated, err)
	}
	var defaultID int64
	var iterationEnabled, iterationUnlimited bool
	var iterationLocation, iterationPath, lastStatus string
	var iterationStart, iterationStep int64
	var iterationCount, iterationDelay, noKeywordStopCount, randomDelayMin, randomDelayMax int
	var requestCount, successCount, failureCount int
	if err := store.pool.QueryRow(ctx, `INSERT INTO keyword_api_sources (name) VALUES ('Migration defaults')
		RETURNING id, iteration_enabled, iteration_location, iteration_path, iteration_start,
		iteration_step, iteration_count, iteration_delay_seconds, iteration_unlimited,
		iteration_no_keyword_stop_count, iteration_random_delay_min_seconds,
		iteration_random_delay_max_seconds, last_status,
		last_request_count, last_success_count, last_failure_count`).Scan(
		&defaultID, &iterationEnabled, &iterationLocation, &iterationPath, &iterationStart,
		&iterationStep, &iterationCount, &iterationDelay, &iterationUnlimited,
		&noKeywordStopCount, &randomDelayMin, &randomDelayMax, &lastStatus,
		&requestCount, &successCount, &failureCount,
	); err != nil {
		t.Fatalf("migration 7 defaults: %v", err)
	}
	if iterationEnabled || iterationLocation != "query" || iterationPath != "" || iterationStart != 0 ||
		iterationStep != 20 || iterationCount != 1 || iterationDelay != 0 || iterationUnlimited ||
		noKeywordStopCount != 0 || randomDelayMin != 0 || randomDelayMax != 0 || lastStatus != KeywordAPISourceStatusPending ||
		requestCount != 0 || successCount != 0 || failureCount != 0 {
		t.Fatalf("migration 7 defaults = enabled:%v location:%q path:%q start:%d step:%d count:%d delay:%d unlimited:%v stop:%d random:%d..%d status:%q stats:%d/%d/%d",
			iterationEnabled, iterationLocation, iterationPath, iterationStart, iterationStep, iterationCount, iterationDelay,
			iterationUnlimited, noKeywordStopCount, randomDelayMin, randomDelayMax, lastStatus, requestCount, successCount, failureCount)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO keyword_api_sources
		(name, iteration_enabled, iteration_path, iteration_unlimited)
		VALUES ('Invalid unlimited', TRUE, 'page', TRUE)`); err == nil {
		t.Fatal("migration allowed unlimited iteration without a no-keyword stop count")
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO keyword_api_sources
		(name, iteration_random_delay_min_seconds, iteration_random_delay_max_seconds)
		VALUES ('Invalid random range', 1, 0)`); err == nil {
		t.Fatal("migration allowed a reversed random delay range")
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO keyword_api_sources
		(name, iteration_no_keyword_stop_count) VALUES ('Invalid stop count', 101)`); err == nil {
		t.Fatal("migration allowed an out-of-range no-keyword stop count")
	}
	if _, err := store.pool.Exec(ctx, "DELETE FROM keyword_api_sources WHERE id=$1", defaultID); err != nil {
		t.Fatalf("delete migration default row: %v", err)
	}
	cooldown := int64(1800)
	source, err := store.CreateKeywordAPISource(ctx, CreateKeywordAPISourceInput{
		Name: "Primary API", Enabled: true, RequestMethod: "POST", RequestURL: "https://example.test/keywords",
		RequestHeaders: map[string]string{"Authorization": "Bearer secret"}, QueryParams: map[string]string{"lang": "zh"},
		BodyType: "json", RequestBody: `{"active":true}`, ProxyURL: "socks5h://proxy.example.test:1080",
		ResponsePath: "data.items[].name", SyncIntervalSeconds: 3600, DefaultKeywordType: "movie",
		DefaultPriority: 23, DefaultCooldownSeconds: &cooldown, IterationEnabled: true,
		IterationLocation: "body", IterationPath: "pagination.offset", IterationStart: 0,
		IterationStep: 20, IterationCount: 10, IterationDelaySeconds: 1,
		IterationUnlimited: true, IterationNoKeywordStopCount: 3,
		IterationRandomDelayMinSeconds: -1, IterationRandomDelayMaxSeconds: 2,
	})
	if err != nil {
		t.Fatalf("CreateKeywordAPISource(): %v", err)
	}
	if source.NextSyncAt == nil || !source.NextSyncAt.Equal(now) || source.RequestHeaders["Authorization"] != "Bearer secret" ||
		!source.IterationEnabled || source.IterationLocation != "body" || source.IterationPath != "pagination.offset" ||
		source.IterationStep != 20 || source.IterationCount != 10 || source.IterationDelaySeconds != 1 ||
		!source.IterationUnlimited || source.IterationNoKeywordStopCount != 3 ||
		source.IterationRandomDelayMinSeconds != -1 || source.IterationRandomDelayMaxSeconds != 2 {
		t.Fatalf("created source = %+v", source)
	}
	newName := "Updated API"
	updatedStopCount, updatedRandomDelayMin, updatedRandomDelayMax := 4, -2, 3
	updated, err := store.UpdateKeywordAPISource(ctx, source.ID, UpdateKeywordAPISourceInput{
		Name: &newName, IterationNoKeywordStopCount: &updatedStopCount,
		IterationRandomDelayMinSeconds: &updatedRandomDelayMin,
		IterationRandomDelayMaxSeconds: &updatedRandomDelayMax,
	})
	if err != nil || updated.Name != newName || updated.DefaultPriority != 23 ||
		updated.IterationNoKeywordStopCount != updatedStopCount ||
		updated.IterationRandomDelayMinSeconds != updatedRandomDelayMin ||
		updated.IterationRandomDelayMaxSeconds != updatedRandomDelayMax {
		t.Fatalf("UpdateKeywordAPISource() = %+v, %v", updated, err)
	}
	page, err := store.ListKeywordAPISources(ctx, KeywordAPISourceFilter{Query: "Updated", Page: 1, PageSize: 10})
	if err != nil || page.Total != 1 || len(page.Items) != 1 ||
		page.Items[0].IterationNoKeywordStopCount != updatedStopCount ||
		page.Items[0].IterationRandomDelayMinSeconds != updatedRandomDelayMin ||
		page.Items[0].IterationRandomDelayMaxSeconds != updatedRandomDelayMax {
		t.Fatalf("ListKeywordAPISources() = %+v, %v", page, err)
	}

	manualCooldown := int64(99)
	manualEnabled := false
	manual, err := store.CreateKeyword(ctx, CreateKeywordInput{
		Keyword: "Alpha", KeywordType: "manual-type", SourceType: "manual", Enabled: &manualEnabled,
		Priority: 88, CooldownSeconds: &manualCooldown,
	})
	if err != nil {
		t.Fatalf("CreateKeyword(manual): %v", err)
	}

	claims := make(chan *KeywordAPISource, 2)
	errorsByClaim := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			claimed, claimErr := store.ClaimDueKeywordAPISource(ctx, now)
			claims <- claimed
			errorsByClaim <- claimErr
		}()
	}
	wait.Wait()
	close(claims)
	close(errorsByClaim)
	claimCount := 0
	for claimErr := range errorsByClaim {
		if claimErr != nil {
			t.Fatalf("ClaimDueKeywordAPISource(): %v", claimErr)
		}
	}
	for claimed := range claims {
		if claimed != nil {
			claimCount++
			if claimed.LastStatus != KeywordAPISourceStatusRunning {
				t.Fatalf("claimed status = %q", claimed.LastStatus)
			}
		}
	}
	if claimCount != 1 {
		t.Fatalf("claim count = %d, want 1", claimCount)
	}

	firstSyncAt := now.Add(time.Minute)
	result, err := store.CompleteKeywordAPISourceSync(ctx, KeywordAPISourceSyncInput{
		SourceID: source.ID, Values: []string{" Alpha ", "alpha", "Beta", ""}, SyncedAt: firstSyncAt,
	})
	if err != nil || result.Seen != 4 || result.Unique != 2 || result.InsertedKeywords != 1 || result.ExistingKeywords != 1 {
		t.Fatalf("CompleteKeywordAPISourceSync() = %+v, %v", result, err)
	}
	if result.Source.LastRequestCount != 1 || result.Source.LastSuccessCount != 1 || result.Source.LastFailureCount != 0 {
		t.Fatalf("legacy sync stats = %+v", result.Source)
	}
	unchanged, err := store.GetKeyword(ctx, manual.ID)
	if err != nil || unchanged.KeywordType != "manual-type" || unchanged.SourceType != "manual" || unchanged.Enabled || unchanged.Priority != 88 || unchanged.CooldownSeconds == nil || *unchanged.CooldownSeconds != manualCooldown {
		t.Fatalf("existing keyword was overwritten: %+v, %v", unchanged, err)
	}
	beta, err := store.GetKeywordByNormalized(ctx, "beta")
	if err != nil || beta.KeywordType != "movie" || beta.SourceType != "api" || !beta.Enabled || beta.Priority != 23 || beta.CooldownSeconds == nil || *beta.CooldownSeconds != cooldown {
		t.Fatalf("new API keyword = %+v, %v", beta, err)
	}
	items, err := store.ListKeywordAPISourceItems(ctx, source.ID)
	if err != nil || len(items) != 2 {
		t.Fatalf("source items = %+v, %v", items, err)
	}
	firstSeen := map[string]time.Time{}
	for _, item := range items {
		firstSeen[item.NormalizedValue] = item.FirstSeenAt
	}

	if _, err := store.ClaimKeywordAPISourceForSync(ctx, source.ID, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("ClaimKeywordAPISourceForSync(): %v", err)
	}
	secondSyncAt := now.Add(3 * time.Minute)
	partialResult, err := store.CompleteKeywordAPISourceSync(ctx, KeywordAPISourceSyncInput{
		SourceID: source.ID, Values: []string{"Beta"}, SyncedAt: secondSyncAt,
		Status: KeywordAPISourceStatusPartial, ErrorMessage: "round 2 failed",
		RequestCount: 2, SuccessCount: 1, FailureCount: 1,
	})
	if err != nil {
		t.Fatalf("second CompleteKeywordAPISourceSync(): %v", err)
	}
	if partialResult.Source.LastStatus != KeywordAPISourceStatusPartial || partialResult.Source.LastError != "round 2 failed" ||
		partialResult.Source.LastRequestCount != 2 || partialResult.Source.LastSuccessCount != 1 || partialResult.Source.LastFailureCount != 1 {
		t.Fatalf("partial sync = %+v", partialResult.Source)
	}
	items, err = store.ListKeywordAPISourceItems(ctx, source.ID)
	if err != nil || len(items) != 2 {
		t.Fatalf("disappeared item was removed: %+v, %v", items, err)
	}
	for _, item := range items {
		if item.NormalizedValue == "alpha" && (!item.LastSeenAt.Equal(firstSyncAt) || !item.FirstSeenAt.Equal(firstSeen["alpha"])) {
			t.Fatalf("disappeared alpha timestamps changed: %+v", item)
		}
		if item.NormalizedValue == "beta" && (!item.LastSeenAt.Equal(secondSyncAt) || !item.FirstSeenAt.Equal(firstSeen["beta"])) {
			t.Fatalf("beta timestamps = %+v", item)
		}
	}

	if _, err := store.ClaimKeywordAPISourceForSync(ctx, source.ID, now.Add(4*time.Minute)); err != nil {
		t.Fatalf("claim before failure: %v", err)
	}
	failed, err := store.FailKeywordAPISourceSyncWithStats(ctx, source.ID, "sanitized failure", now.Add(5*time.Minute), 3, 3)
	if err != nil || failed.LastStatus != KeywordAPISourceStatusFailed || failed.LastError != "sanitized failure" || failed.NextSyncAt == nil || !failed.NextSyncAt.Equal(now.Add(65*time.Minute)) ||
		failed.LastRequestCount != 3 || failed.LastSuccessCount != 0 || failed.LastFailureCount != 3 {
		t.Fatalf("FailKeywordAPISourceSync() = %+v, %v", failed, err)
	}
	copy, err := store.CopyKeywordAPISource(ctx, source.ID)
	if err != nil {
		t.Fatalf("CopyKeywordAPISource(): %v", err)
	}
	if copy.Name != updated.Name+" 副本" || copy.Enabled || copy.NextSyncAt != nil || copy.LastSyncedAt != nil ||
		copy.LastStatus != KeywordAPISourceStatusPending || copy.LastError != "" || copy.LastItemCount != 0 ||
		copy.LastRequestCount != 0 || copy.LastSuccessCount != 0 || copy.LastFailureCount != 0 ||
		copy.RequestURL != source.RequestURL || copy.RequestHeaders["Authorization"] != "Bearer secret" ||
		!copy.IterationEnabled || copy.IterationPath != source.IterationPath || copy.IterationCount != source.IterationCount ||
		!copy.IterationUnlimited || copy.IterationNoKeywordStopCount != updatedStopCount ||
		copy.IterationRandomDelayMinSeconds != updatedRandomDelayMin ||
		copy.IterationRandomDelayMaxSeconds != updatedRandomDelayMax {
		t.Fatalf("copied source = %+v", copy)
	}
	if err := store.DeleteKeywordAPISource(ctx, copy.ID); err != nil {
		t.Fatalf("DeleteKeywordAPISource(copy): %v", err)
	}
	if err := store.DeleteKeywordAPISource(ctx, source.ID); err != nil {
		t.Fatalf("DeleteKeywordAPISource(): %v", err)
	}
	if _, err := store.GetKeywordAPISource(ctx, source.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted source error = %v", err)
	}
	if _, err := store.GetKeyword(ctx, manual.ID); err != nil {
		t.Fatalf("deleting source deleted existing keyword: %v", err)
	}
	if _, err := store.GetKeyword(ctx, beta.ID); err != nil {
		t.Fatalf("deleting source deleted generated keyword: %v", err)
	}
}

func TestPostgresKeywordAPISourceConcurrentGlobalDedupe(t *testing.T) {
	now := time.Date(2026, time.July, 12, 11, 0, 0, 0, time.UTC)
	store := newPostgresTestStore(t, now)
	ctx := context.Background()
	create := func(name string) KeywordAPISource {
		source, err := store.CreateKeywordAPISource(ctx, CreateKeywordAPISourceInput{Name: name})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return source
	}
	first, second := create("First"), create("Second")
	results := make(chan KeywordAPISourceSyncResult, 2)
	errs := make(chan error, 2)
	for _, sourceID := range []int64{first.ID, second.ID} {
		go func(id int64) {
			result, err := store.CompleteKeywordAPISourceSync(ctx, KeywordAPISourceSyncInput{SourceID: id, Values: []string{"Gamma"}, SyncedAt: now})
			results <- result
			errs <- err
		}(sourceID)
	}
	inserted := 0
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent sync: %v", err)
		}
		inserted += (<-results).InsertedKeywords
	}
	if inserted != 1 {
		t.Fatalf("inserted keywords = %d, want 1", inserted)
	}
	gamma, err := store.GetKeywordByNormalized(ctx, "gamma")
	if err != nil {
		t.Fatalf("GetKeywordByNormalized(gamma): %v", err)
	}
	for _, sourceID := range []int64{first.ID, second.ID} {
		items, err := store.ListKeywordAPISourceItems(ctx, sourceID)
		if err != nil || len(items) != 1 || items[0].KeywordID != gamma.ID {
			t.Fatalf("source %d items = %+v, %v", sourceID, items, err)
		}
	}
}
