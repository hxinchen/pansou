package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pansou/model"
)

func newPostgresTestStore(t *testing.T, now time.Time) *Store {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = os.Getenv("PANSOU_TEST_DATABASE_URL")
	}
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL or PANSOU_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open integration database: %v", err)
	}
	schema := fmt.Sprintf("pansou_storage_test_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		admin.Close()
		t.Fatalf("create integration schema: %v", err)
	}
	store, err := Open(ctx, databaseURL,
		WithPoolConfig(func(config *pgxpool.Config) {
			config.ConnConfig.RuntimeParams["search_path"] = schema
			config.MaxConns = 8
		}),
		func(options *storeOptions) { options.now = func() time.Time { return now } },
	)
	if err != nil {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE")
		admin.Close()
		t.Fatalf("open integration store: %v", err)
	}
	t.Cleanup(func() {
		store.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupCtx, "DROP SCHEMA "+identifier+" CASCADE")
		admin.Close()
	})
	return store
}

func TestPostgresStorageLifecycle(t *testing.T) {
	now := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	store := newPostgresTestStore(t, now)
	ctx := context.Background()

	var migrated bool
	if err := store.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM schema_migrations WHERE version=1)`).Scan(&migrated); err != nil || !migrated {
		t.Fatalf("migration ledger: migrated=%v err=%v", migrated, err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM schema_migrations WHERE version=10)`).Scan(&migrated); err != nil || !migrated {
		t.Fatalf("detail pagination migration: migrated=%v err=%v", migrated, err)
	}
	var legacyRunSummaryColumn bool
	if err := store.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema=current_schema() AND table_name='collection_runs' AND column_name='source_summary')`).Scan(&legacyRunSummaryColumn); err != nil || legacyRunSummaryColumn {
		t.Fatalf("legacy batch source_summary present=%v err=%v", legacyRunSummaryColumn, err)
	}

	alphaCooldown := int64(3600)
	alpha, err := store.CreateKeyword(ctx, CreateKeywordInput{Keyword: "Alpha", Priority: 10, CooldownSeconds: &alphaCooldown})
	if err != nil {
		t.Fatalf("CreateKeyword(alpha): %v", err)
	}
	beta, err := store.CreateKeyword(ctx, CreateKeywordInput{Keyword: "Beta", Priority: 5})
	if err != nil {
		t.Fatalf("CreateKeyword(beta): %v", err)
	}
	if _, err := store.CreateKeyword(ctx, CreateKeywordInput{Keyword: " alpha "}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate CreateKeyword error = %v, want ErrConflict", err)
	}

	inputs := []ResourceInput{
		{URL: "https://pan.example/share/item?pwd=111&utm_source=one", Title: "alpha title", Content: "resource list private content", DiscoveredAt: now,
			Source: ResourceSourceInput{SourceType: "tg", SourceKey: "channel-a", SourceIdentity: "message-a",
				Content: "source list private content", Metadata: map[string]any{"token": "private"}}, Keyword: "Alpha"},
		{URL: "https://PAN.example/share/item?pwd=222&utm_source=two", Title: "a more complete alpha title", DiscoveredAt: now.Add(time.Minute),
			Source: ResourceSourceInput{SourceType: "plugin", SourceKey: "pansearch", SourceIdentity: "result-a",
				Content: "second source private content", Metadata: map[string]any{"token": "private-two"}}, Keyword: "Beta"},
	}
	var wait sync.WaitGroup
	errorsByUpsert := make(chan error, len(inputs))
	for _, input := range inputs {
		input := input
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := store.UpsertResource(ctx, input)
			errorsByUpsert <- err
		}()
	}
	wait.Wait()
	close(errorsByUpsert)
	for err := range errorsByUpsert {
		if err != nil {
			t.Fatalf("concurrent UpsertResource: %v", err)
		}
	}
	page, err := store.ListResourceSummaries(ctx, ResourceFilter{IncludeInvalid: true})
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if page.Total != 1 || page.Items[0].DiscoveryCount != 2 || page.Items[0].SourceCount != 2 ||
		page.Items[0].KeywordCount != 2 || len(page.Items[0].SourcePreview) != 2 ||
		len(page.Items[0].Sources) != 0 || len(page.Items[0].Keywords) != 0 ||
		page.Items[0].Password != "" || page.Items[0].Content != "" {
		t.Fatalf("concurrent upsert summary = %+v", page.Items[0])
	}
	resourceSources, err := store.ListResourceSources(ctx, page.Items[0].ID, ResourceAssociationFilter{PageSize: 1})
	if err != nil || resourceSources.Total != 2 || len(resourceSources.Items) != 1 {
		t.Fatalf("resource sources page = %+v err=%v", resourceSources, err)
	}
	if resourceSources.Items[0].Content != "" || resourceSources.Items[0].SourceMetadata != nil {
		t.Fatalf("resource source page leaked large fields: %+v", resourceSources.Items[0])
	}
	fullPage, err := store.ListResources(ctx, ResourceFilter{IncludeInvalid: true})
	if err != nil || len(fullPage.Items) != 1 || fullPage.Items[0].Password == "" ||
		fullPage.Items[0].Content == "" || len(fullPage.Items[0].Sources) != 2 {
		t.Fatalf("full resource search shape = %+v err=%v", fullPage, err)
	}
	resourceKeywords, err := store.ListResourceKeywords(ctx, page.Items[0].ID, ResourceAssociationFilter{PageSize: 1})
	if err != nil || resourceKeywords.Total != 2 || len(resourceKeywords.Items) != 1 {
		t.Fatalf("resource keywords page = %+v err=%v", resourceKeywords, err)
	}

	eventAt := now.AddDate(-1, 0, 0)
	pluginURL := "https://pan.example/share/plugin-result"
	response := model.SearchResponse{
		Results: []model.SearchResult{{
			UniqueID: "pansearch-42", Title: "beta plugin", Datetime: eventAt,
			Links: []model.Link{{Type: "quark", URL: pluginURL, Datetime: eventAt}},
		}},
		MergedByType: model.MergedLinks{"quark": {{URL: pluginURL, Note: "beta plugin", Datetime: eventAt, Source: "plugin:pansearch"}}},
	}
	if _, err := store.UpsertSearchResponse(ctx, "Beta", DefaultKeywordType, "external", response); err != nil {
		t.Fatalf("UpsertSearchResponse: %v", err)
	}
	pluginPage, err := store.ListResources(ctx, ResourceFilter{Keyword: "Beta", Query: "plugin-result", IncludeInvalid: true})
	if err != nil || len(pluginPage.Items) != 1 {
		t.Fatalf("plugin ListResources: items=%d err=%v", len(pluginPage.Items), err)
	}
	pluginResource := pluginPage.Items[0]
	if !pluginResource.FirstSeenAt.Equal(now) || pluginResource.LinkDatetime == nil || !pluginResource.LinkDatetime.Equal(eventAt) {
		t.Fatalf("discovery/link timestamps = %v/%v, want %v/%v", pluginResource.FirstSeenAt, pluginResource.LinkDatetime, now, eventAt)
	}
	if len(pluginResource.Sources) != 1 || pluginResource.Sources[0].SourceType != "plugin" || !pluginResource.Sources[0].DiscoveredAt.Equal(now) {
		t.Fatalf("plugin source = %+v", pluginResource.Sources)
	}

	if _, err := store.UpsertResource(ctx, ResourceInput{URL: "https://pan.example/share/beta-only", Title: "beta only", DiscoveredAt: now}); err != nil {
		t.Fatalf("upsert beta-only resource: %v", err)
	}
	includePage, err := store.ListResources(ctx, ResourceFilter{Include: []string{"alpha", "beta"}, IncludeInvalid: true})
	if err != nil || includePage.Total != 3 {
		t.Fatalf("OR include filter total=%d err=%v, want 3", includePage.Total, err)
	}

	run, err := store.CreateRun(ctx, CreateRunInput{Trigger: "manual", Force: true, KeywordIDs: []int64{alpha.ID, beta.ID}})
	if err != nil || len(run.Items) != 2 {
		t.Fatalf("CreateRun: items=%d err=%v", len(run.Items), err)
	}
	first, second := run.Items[0], run.Items[1]
	if first.KeywordID != nil && *first.KeywordID == alpha.ID && (first.CooldownSeconds == nil || *first.CooldownSeconds != alphaCooldown) {
		t.Fatalf("run snapshot cooldown = %v, want %d", first.CooldownSeconds, alphaCooldown)
	}
	if err := store.MarkRunItemRunning(ctx, run.ID, first.ID, now); err != nil {
		t.Fatalf("MarkRunItemRunning(first): %v", err)
	}
	exactNext := now.Add(3 * time.Hour)
	if _, err := store.CompleteRunItem(ctx, first.ID, CompleteRunItemInput{
		Status: RunSuccess, FoundCount: 3, NewCount: 2, DuplicateCount: 1,
		CompletedAt: now.Add(time.Minute), NextEligibleAt: &exactNext, SourceSummary: map[string]any{
			"pansearch": map[string]any{"key": "pansearch", "type": "plugin", "status": "success", "attempts": 1,
				"result_count": 3, "new_count": 2, "duplicate_count": 1, "duration_ms": 120},
			"channel-a": map[string]any{"key": "channel-a", "type": "tg", "status": "success_empty", "attempts": 1,
				"result_count": 0, "new_count": 0, "duplicate_count": 0, "duration_ms": 80},
		},
	}); err != nil {
		t.Fatalf("CompleteRunItem(first): %v", err)
	}
	updatedAlpha, err := store.GetKeyword(ctx, alpha.ID)
	if err != nil || updatedAlpha.NextEligibleAt == nil || !updatedAlpha.NextEligibleAt.Equal(exactNext) {
		t.Fatalf("exact cooldown = %v err=%v, want %v", updatedAlpha.NextEligibleAt, err, exactNext)
	}
	runItems, err := store.ListRunItems(ctx, run.ID, RunItemFilter{Query: "Alpha", PageSize: 30})
	if err != nil || runItems.Total != 1 || len(runItems.Items) != 1 || runItems.Items[0].SourceTotal != 2 ||
		runItems.Items[0].SourceSuccess != 1 || runItems.Items[0].SourceEmpty != 1 || runItems.Items[0].SourceFailed != 0 ||
		runItems.Items[0].SourceSummary != nil {
		t.Fatalf("run items page = %+v err=%v", runItems, err)
	}
	runSources, err := store.ListRunItemSources(ctx, run.ID, first.ID, RunSourceFilter{Types: []string{"plugin"}, PageSize: 50})
	if err != nil || runSources.Total != 1 || len(runSources.Items) != 1 || runSources.Items[0].Key != "pansearch" ||
		runSources.Items[0].DurationMS != 120 {
		t.Fatalf("run sources page = %+v err=%v", runSources, err)
	}
	if err := store.MarkRunItemRunning(ctx, run.ID, second.ID, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("MarkRunItemRunning(second): %v", err)
	}
	if recovered, err := store.RecoverRunningItems(ctx); err != nil || recovered != 1 {
		t.Fatalf("RecoverRunningItems = %d, %v", recovered, err)
	}
	claimed, err := store.ClaimNextRunItem(ctx)
	if err != nil || claimed == nil || claimed.ID != second.ID {
		t.Fatalf("ClaimNextRunItem = %+v, %v", claimed, err)
	}
	if _, err := store.CompleteRunItem(ctx, second.ID, CompleteRunItemInput{Status: RunSuccessEmpty, CompletedAt: now.Add(3 * time.Minute)}); err != nil {
		t.Fatalf("CompleteRunItem(second): %v", err)
	}
	if err := store.CompleteRun(ctx, run.ID, RunSuccess, now.Add(4*time.Minute)); err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}
	completed, err := store.GetRun(ctx, run.ID)
	if err != nil || completed.CompletedItems != 2 || completed.Status != RunSuccess {
		t.Fatalf("completed run = %+v err=%v", completed, err)
	}

	overview, err := store.Overview(ctx)
	if err != nil || overview.ResourceCount != 3 || overview.KeywordCount != 2 || len(overview.RecentRuns) != 1 {
		t.Fatalf("Overview = %+v err=%v", overview, err)
	}
	trends, err := store.Trends(ctx, 7)
	if err != nil || len(trends) != 7 || trends[len(trends)-1].Discoveries != 3 {
		t.Fatalf("Trends = %+v err=%v", trends, err)
	}
}

func TestPostgresRunErrorSummaryCountsOnlyFailedItems(t *testing.T) {
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	store := newPostgresTestStore(t, now)
	ctx := context.Background()
	keywords := make([]RunKeywordInput, 8)
	for index := range keywords {
		keywords[index] = RunKeywordInput{Keyword: fmt.Sprintf("failure-summary-%d", index+1)}
	}
	run, err := store.CreateRun(ctx, CreateRunInput{Trigger: "manual", Force: true, Keywords: keywords})
	if err != nil || len(run.Items) != len(keywords) {
		t.Fatalf("CreateRun: items=%d err=%v", len(run.Items), err)
	}
	for index, item := range run.Items {
		startedAt := now.Add(time.Duration(index) * time.Second)
		if err := store.MarkRunItemRunning(ctx, run.ID, item.ID, startedAt); err != nil {
			t.Fatalf("MarkRunItemRunning(%d): %v", index, err)
		}
		status := RunFailed
		message := fmt.Sprintf("failed item %d", index+1)
		if index == 0 {
			message = ""
		}
		if index == len(run.Items)-1 {
			status = RunSuccess
			message = "successful item warning must not count"
		}
		if _, err := store.CompleteRunItem(ctx, item.ID, CompleteRunItemInput{
			Status: status, ErrorMessage: message, CompletedAt: startedAt.Add(time.Second),
		}); err != nil {
			t.Fatalf("CompleteRunItem(%d): %v", index, err)
		}
	}
	summary, err := store.GetRunSummary(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRunSummary: %v", err)
	}
	if !strings.Contains(summary.ErrorMessage, runMissingErrorDetailText) ||
		!strings.Contains(summary.ErrorMessage, "... and 2 more") ||
		strings.Contains(summary.ErrorMessage, "successful item warning") {
		t.Fatalf("error summary = %q", summary.ErrorMessage)
	}
}
