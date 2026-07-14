package storage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPostgresKeywordAPISyncRunLifecycle(t *testing.T) {
	now := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	store := newPostgresTestStore(t, now)
	ctx := context.Background()
	var migrated bool
	if err := store.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version=8)").Scan(&migrated); err != nil || !migrated {
		t.Fatalf("migration 8: migrated=%v err=%v", migrated, err)
	}

	source, err := store.CreateKeywordAPISource(ctx, CreateKeywordAPISourceInput{
		Name: "History source", RequestMethod: "POST",
		RequestURL:     "https://user:secret@example.test/items?token=secret-query",
		RequestHeaders: map[string]string{"Authorization": "Bearer secret-header"},
		QueryParams:    map[string]string{"api_key": "secret-query-map"},
		BodyType:       "json", RequestBody: `{"token":"secret-body"}`,
		ProxyURL:     "socks5h://user:secret-proxy@proxy.example.test:1080",
		ResponsePath: "items[]", IterationEnabled: true, IterationLocation: "query",
		IterationPath: "offset", IterationStart: 0, IterationStep: 20, IterationCount: 3,
	})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	if source.SyncConfigRevision != 1 || source.LastAppliedConfigRevision != 0 || source.ResultStale {
		t.Fatalf("source revisions = %+v", source)
	}

	run, alreadyActive, err := store.EnqueueKeywordAPISourceSync(ctx, source.ID, KeywordAPISyncTriggerManual, now)
	if err != nil || alreadyActive || run.TotalIterations == nil || *run.TotalIterations != 3 || run.Unlimited {
		t.Fatalf("enqueue = %+v active=%v err=%v", run, alreadyActive, err)
	}
	encoded, _ := json.Marshal(run.RequestSummary)
	for _, secret := range []string{"secret-query", "secret-header", "secret-query-map", "secret-body", "secret-proxy"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("run request summary leaked %q: %s", secret, encoded)
		}
	}
	duplicate, alreadyActive, err := store.EnqueueKeywordAPISourceSync(ctx, source.ID, KeywordAPISyncTriggerSave, now.Add(time.Second))
	if err != nil || !alreadyActive || duplicate.ID != run.ID {
		t.Fatalf("duplicate enqueue = %+v active=%v err=%v", duplicate, alreadyActive, err)
	}
	detail, err := store.GetKeywordAPISyncRun(ctx, run.ID)
	if err != nil || len(detail.Iterations) != 3 {
		t.Fatalf("queued detail = %+v err=%v", detail, err)
	}
	summary, err := store.GetKeywordAPISyncRunSummary(ctx, run.ID)
	if err != nil || len(summary.Iterations) != 0 || summary.IterationRecordsTotal != 3 || !summary.IterationsTruncated {
		t.Fatalf("queued summary = %+v err=%v", summary, err)
	}
	iterationPage, err := store.ListKeywordAPISyncRunIterations(ctx, run.ID, KeywordAPISyncIterationFilter{Page: 1, PageSize: 2})
	if err != nil || iterationPage.Total != 3 || iterationPage.Page != 1 || len(iterationPage.Items) != 2 ||
		iterationPage.Items[0].Sequence != 3 || iterationPage.Items[1].Sequence != 2 {
		t.Fatalf("latest iteration page = %+v err=%v", iterationPage, err)
	}
	iterationPage, err = store.ListKeywordAPISyncRunIterations(ctx, run.ID, KeywordAPISyncIterationFilter{Page: 2, PageSize: 2})
	if err != nil || iterationPage.Total != 3 || iterationPage.Page != 2 || len(iterationPage.Items) != 1 ||
		iterationPage.Items[0].Sequence != 1 {
		t.Fatalf("iteration page = %+v err=%v", iterationPage, err)
	}
	for index, iteration := range detail.Iterations {
		if iteration.Sequence != index+1 || iteration.IterationValue != int64(index*20) || iteration.Status != KeywordAPISyncIterationStatusQueued {
			t.Fatalf("precreated iteration %d = %+v", index, iteration)
		}
	}

	claim, err := store.ClaimNextKeywordAPISyncRun(ctx, "worker-a", "token-a", now.Add(2*time.Second), now.Add(47*time.Second))
	if err != nil || claim == nil || claim.Run.ID != run.ID || claim.Source.ID != source.ID {
		t.Fatalf("claim = %+v err=%v", claim, err)
	}
	if err := store.RenewKeywordAPISyncRunLease(ctx, run.ID, "worker-a", "wrong-token", now.Add(60*time.Second), now.Add(10*time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong-token renewal error = %v", err)
	}
	if err := store.RenewKeywordAPISyncRunLease(ctx, run.ID, "worker-a", "token-a", now.Add(60*time.Second), now.Add(10*time.Second)); err != nil {
		t.Fatalf("renew lease: %v", err)
	}
	completeIteration := func(sequence int, value int64, raw, unique, crossNew int) {
		startedAt := now.Add(time.Duration(10+sequence) * time.Second)
		input := KeywordAPISyncIterationInput{
			RunID: run.ID, LeaseOwner: "worker-a", LeaseToken: "token-a", Sequence: sequence,
			IterationValue: value, StartedAt: startedAt,
		}
		if _, err := store.BeginKeywordAPISyncIteration(ctx, input); err != nil {
			t.Fatalf("begin iteration %d: %v", sequence, err)
		}
		input.Status = KeywordAPISyncIterationStatusSuccess
		input.RawItemCount, input.UniqueItemCount, input.CrossIterationNew = raw, unique, crossNew
		input.Samples = []string{"Alpha", strings.Repeat("界", 130), "three", "four", "five", "six"}
		input.CompletedAt = startedAt.Add(time.Second)
		if _, err := store.CompleteKeywordAPISyncIteration(ctx, input); err != nil {
			t.Fatalf("complete iteration %d: %v", sequence, err)
		}
	}
	completeIteration(1, 0, 3, 2, 2)
	completeIteration(2, 20, 1, 1, 0)
	finalRun, result, err := store.FinalizeKeywordAPISyncRun(ctx, KeywordAPISyncFinalizeInput{
		RunID: run.ID, LeaseOwner: "worker-a", LeaseToken: "token-a",
		Values: []string{"Alpha", "Beta"}, ValueSequences: []int{1, 1}, SyncedAt: now.Add(time.Minute),
		Status: KeywordAPISyncRunStatusSuccess, RawItemCount: 4,
		RequestCount: 2, SuccessCount: 2,
	})
	if err != nil || finalRun.Status != KeywordAPISyncRunStatusSuccess || result.Seen != 4 || result.Unique != 2 ||
		finalRun.RawItemCount != 4 || finalRun.UniqueItemCount != 2 || finalRun.NewKeywordCount+finalRun.ExistingKeywordCount != 2 {
		t.Fatalf("finalize = run:%+v result:%+v err=%v", finalRun, result, err)
	}
	detail, err = store.GetKeywordAPISyncRun(ctx, run.ID)
	if err != nil || detail.CompletedIterations != 2 || detail.SuccessIterations != 2 || detail.FailedIterations != 0 ||
		len(detail.Iterations) != 3 || detail.Iterations[2].Status != KeywordAPISyncIterationStatusSkipped ||
		len(detail.Iterations[0].Samples) != 5 || len([]rune(detail.Iterations[0].Samples[1])) != 120 ||
		detail.Iterations[0].NewKeywordCount+detail.Iterations[0].ExistingKeywordCount != 2 ||
		detail.Iterations[1].NewKeywordCount != 0 || detail.Iterations[1].ExistingKeywordCount != 0 {
		t.Fatalf("completed detail = %+v err=%v", detail, err)
	}
	iterationPage, err = store.ListKeywordAPISyncRunIterations(ctx, run.ID, KeywordAPISyncIterationFilter{Page: 1, PageSize: 3})
	if err != nil || len(iterationPage.Items) != 3 || iterationPage.Items[0].Sequence != 2 ||
		iterationPage.Items[1].Sequence != 1 || iterationPage.Items[2].Sequence != 3 {
		t.Fatalf("completed iteration order = %+v err=%v", iterationPage, err)
	}

	repeatRun, _, err := store.EnqueueKeywordAPISourceSync(ctx, source.ID, KeywordAPISyncTriggerManual, now.Add(70*time.Second))
	if err != nil {
		t.Fatalf("enqueue repeat run: %v", err)
	}
	repeatClaim, err := store.ClaimNextKeywordAPISyncRun(ctx, "worker-repeat", "token-repeat", now.Add(71*time.Second), now.Add(116*time.Second))
	if err != nil || repeatClaim == nil || repeatClaim.Run.ID != repeatRun.ID {
		t.Fatalf("claim repeat run = %+v err=%v", repeatClaim, err)
	}
	repeatIteration := KeywordAPISyncIterationInput{
		RunID: repeatRun.ID, LeaseOwner: "worker-repeat", LeaseToken: "token-repeat",
		Sequence: 1, IterationValue: 0, StartedAt: now.Add(72 * time.Second),
	}
	if _, err := store.BeginKeywordAPISyncIteration(ctx, repeatIteration); err != nil {
		t.Fatalf("begin repeat iteration: %v", err)
	}
	repeatIteration.Status = KeywordAPISyncIterationStatusSuccess
	repeatIteration.RawItemCount, repeatIteration.UniqueItemCount, repeatIteration.CrossIterationNew = 2, 2, 2
	repeatIteration.CompletedAt = now.Add(73 * time.Second)
	if _, err := store.CompleteKeywordAPISyncIteration(ctx, repeatIteration); err != nil {
		t.Fatalf("complete repeat iteration: %v", err)
	}
	repeatRun, repeatResult, err := store.FinalizeKeywordAPISyncRun(ctx, KeywordAPISyncFinalizeInput{
		RunID: repeatRun.ID, LeaseOwner: "worker-repeat", LeaseToken: "token-repeat",
		Values: []string{"Alpha", "Beta"}, ValueSequences: []int{1, 1}, SyncedAt: now.Add(74 * time.Second),
		Status: KeywordAPISyncRunStatusSuccess, RawItemCount: 2, RequestCount: 1, SuccessCount: 1,
	})
	if err != nil || repeatRun.NewKeywordCount != 0 || repeatRun.ExistingKeywordCount != 2 ||
		repeatResult.InsertedKeywords != 0 || repeatResult.ExistingKeywords != 2 {
		t.Fatalf("repeat finalize = run:%+v result:%+v err=%v", repeatRun, repeatResult, err)
	}
	repeatDetail, err := store.GetKeywordAPISyncRun(ctx, repeatRun.ID)
	if err != nil || len(repeatDetail.Iterations) != 3 || repeatDetail.Iterations[0].NewKeywordCount != 0 ||
		repeatDetail.Iterations[0].ExistingKeywordCount != 2 {
		t.Fatalf("repeat detail = %+v err=%v", repeatDetail, err)
	}
	refreshed, err := store.GetKeywordAPISource(ctx, source.ID)
	if err != nil || refreshed.LastAppliedConfigRevision != 1 || refreshed.ResultStale || refreshed.LatestRun == nil || refreshed.LatestRun.ID != repeatRun.ID {
		t.Fatalf("refreshed source = %+v err=%v", refreshed, err)
	}
	writeFailureRun, _, err := store.EnqueueKeywordAPISourceSync(ctx, source.ID, KeywordAPISyncTriggerManual, now.Add(80*time.Second))
	if err != nil {
		t.Fatalf("enqueue write-failure run: %v", err)
	}
	writeFailureClaim, err := store.ClaimNextKeywordAPISyncRun(ctx, "worker-write", "token-write", now.Add(81*time.Second), now.Add(126*time.Second))
	if err != nil || writeFailureClaim == nil || writeFailureClaim.Run.ID != writeFailureRun.ID {
		t.Fatalf("claim write-failure run = %+v err=%v", writeFailureClaim, err)
	}
	writeFailureIteration := KeywordAPISyncIterationInput{
		RunID: writeFailureRun.ID, LeaseOwner: "worker-write", LeaseToken: "token-write",
		Sequence: 1, IterationValue: 0, StartedAt: now.Add(82 * time.Second),
	}
	if _, err := store.BeginKeywordAPISyncIteration(ctx, writeFailureIteration); err != nil {
		t.Fatalf("begin write-failure iteration: %v", err)
	}
	writeFailureIteration.Status = KeywordAPISyncIterationStatusSuccess
	writeFailureIteration.RawItemCount, writeFailureIteration.UniqueItemCount, writeFailureIteration.CrossIterationNew = 1, 1, 1
	writeFailureIteration.CompletedAt = now.Add(83 * time.Second)
	if _, err := store.CompleteKeywordAPISyncIteration(ctx, writeFailureIteration); err != nil {
		t.Fatalf("complete write-failure iteration: %v", err)
	}
	writeFailureRun, err = store.FailKeywordAPISyncRun(ctx, writeFailureRun.ID, "worker-write", "token-write",
		"save synchronized keywords failed", now.Add(84*time.Second), 1, 0)
	if err != nil || writeFailureRun.Status != KeywordAPISyncRunStatusFailed || writeFailureRun.RequestCount != 1 ||
		writeFailureRun.SuccessCount != 1 || writeFailureRun.FailureCount != 0 {
		t.Fatalf("write-failure terminal run = %+v err=%v", writeFailureRun, err)
	}

	renamed := "Renamed only"
	refreshed, err = store.UpdateKeywordAPISource(ctx, source.ID, UpdateKeywordAPISourceInput{Name: &renamed})
	if err != nil || refreshed.SyncConfigRevision != 1 {
		t.Fatalf("rename revision = %+v err=%v", refreshed, err)
	}
	query := map[string]string{"api_key": "new-secret"}
	refreshed, err = store.UpdateKeywordAPISource(ctx, source.ID, UpdateKeywordAPISourceInput{QueryParams: &query})
	if err != nil || refreshed.SyncConfigRevision != 2 || !refreshed.ResultStale {
		t.Fatalf("config revision = %+v err=%v", refreshed, err)
	}
	secondRun, _, err := store.EnqueueKeywordAPISourceSync(ctx, source.ID, KeywordAPISyncTriggerSave, now.Add(2*time.Minute))
	if err != nil || secondRun.ConfigRevision != 2 {
		t.Fatalf("enqueue second run = %+v err=%v", secondRun, err)
	}
	if err := store.DeleteKeywordAPISource(ctx, source.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete active source error = %v", err)
	}
	secondClaim, err := store.ClaimNextKeywordAPISyncRun(ctx, "worker-b", "token-b", now.Add(2*time.Minute), now.Add(3*time.Minute))
	if err != nil || secondClaim == nil || secondClaim.Run.ID != secondRun.ID {
		t.Fatalf("claim second run = %+v err=%v", secondClaim, err)
	}
	failedRun, err := store.FailKeywordAPISyncRun(ctx, secondRun.ID, "worker-b", "token-b", "configuration failed before request", now.Add(2*time.Minute+time.Second), 0, 0)
	if err != nil || failedRun.Status != KeywordAPISyncRunStatusFailed || failedRun.RequestCount != 0 || failedRun.FailureCount != 0 {
		t.Fatalf("zero-request failure = %+v err=%v", failedRun, err)
	}
	if err := store.DeleteKeywordAPISource(ctx, source.ID); err != nil {
		t.Fatalf("delete completed source: %v", err)
	}
	deletedDetail, err := store.GetKeywordAPISyncRun(ctx, secondRun.ID)
	if err != nil || deletedDetail.SourceID == nil || *deletedDetail.SourceID != source.ID || deletedDetail.SourceExists || deletedDetail.LiveSourceID != nil {
		t.Fatalf("deleted-source run = %+v err=%v", deletedDetail, err)
	}
	page, err := store.ListKeywordAPISyncRuns(ctx, KeywordAPISyncRunFilter{SourceID: &source.ID, PageSize: 20})
	if err != nil || page.Total != 4 {
		t.Fatalf("deleted-source history = %+v err=%v", page, err)
	}
	from, to := run.CreatedAt.Add(-time.Nanosecond), repeatRun.CreatedAt
	page, err = store.ListKeywordAPISyncRuns(ctx, KeywordAPISyncRunFilter{
		SourceID: &source.ID, From: &from, To: &to, PageSize: 20,
	})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != run.ID {
		t.Fatalf("exclusive history boundary = %+v err=%v", page, err)
	}
}

func TestPostgresKeywordAPISyncUnlimitedAndExpiredLease(t *testing.T) {
	now := time.Date(2026, time.July, 12, 15, 0, 0, 0, time.UTC)
	store := newPostgresTestStore(t, now)
	ctx := context.Background()
	unlimited, err := store.CreateKeywordAPISource(ctx, CreateKeywordAPISourceInput{
		Name: "Unlimited", RequestURL: "https://example.test/items", ResponsePath: "items[]",
		IterationEnabled: true, IterationLocation: "query", IterationPath: "offset",
		IterationStart: 0, IterationStep: 20, IterationCount: 10,
		IterationUnlimited: true, IterationNoKeywordStopCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := store.EnqueueKeywordAPISourceSync(ctx, unlimited.ID, KeywordAPISyncTriggerManual, now)
	if err != nil || run.TotalIterations != nil || !run.Unlimited {
		t.Fatalf("unlimited enqueue = %+v err=%v", run, err)
	}
	detail, err := store.GetKeywordAPISyncRun(ctx, run.ID)
	if err != nil || len(detail.Iterations) != 0 {
		t.Fatalf("unlimited precreate = %+v err=%v", detail, err)
	}
	claim, err := store.ClaimNextKeywordAPISyncRun(ctx, "worker-u", "token-u", now, now.Add(45*time.Second))
	if err != nil || claim == nil {
		t.Fatalf("unlimited claim = %+v err=%v", claim, err)
	}
	input := KeywordAPISyncIterationInput{RunID: run.ID, LeaseOwner: "worker-u", LeaseToken: "token-u", Sequence: 1, IterationValue: 0, StartedAt: now}
	if _, err := store.BeginKeywordAPISyncIteration(ctx, input); err != nil {
		t.Fatal(err)
	}
	input.Status, input.CompletedAt = KeywordAPISyncIterationStatusSuccess, now.Add(time.Second)
	if _, err := store.CompleteKeywordAPISyncIteration(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.FinalizeKeywordAPISyncRun(ctx, KeywordAPISyncFinalizeInput{
		RunID: run.ID, LeaseOwner: "worker-u", LeaseToken: "token-u", SyncedAt: now.Add(2 * time.Second),
		Status: KeywordAPISyncRunStatusSuccess, RequestCount: 1, SuccessCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	detail, err = store.GetKeywordAPISyncRun(ctx, run.ID)
	if err != nil || detail.TotalIterations != nil || len(detail.Iterations) != 1 || detail.Iterations[0].Sequence != 1 {
		t.Fatalf("unlimited detail = %+v err=%v", detail, err)
	}

	expiring, err := store.CreateKeywordAPISource(ctx, CreateKeywordAPISourceInput{
		Name: "Expiring", RequestURL: "https://example.test/items", ResponsePath: "items[]",
	})
	if err != nil {
		t.Fatal(err)
	}
	expiredRun, _, err := store.EnqueueKeywordAPISourceSync(ctx, expiring.ID, KeywordAPISyncTriggerManual, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	claim, err = store.ClaimNextKeywordAPISyncRun(ctx, "worker-e", "token-e", now.Add(time.Minute), now.Add(time.Minute+time.Second))
	if err != nil || claim == nil || claim.Run.ID != expiredRun.ID {
		t.Fatalf("expiring claim = %+v err=%v", claim, err)
	}
	recovered, err := store.RecoverExpiredKeywordAPISyncRuns(ctx, now.Add(time.Minute+2*time.Second))
	if err != nil || recovered != 1 {
		t.Fatalf("recover expired = %d err=%v", recovered, err)
	}
	detail, err = store.GetKeywordAPISyncRun(ctx, expiredRun.ID)
	if err != nil || detail.Status != KeywordAPISyncRunStatusInterrupted || len(detail.Iterations) != 1 || detail.Iterations[0].Status != KeywordAPISyncIterationStatusSkipped {
		t.Fatalf("expired detail = %+v err=%v", detail, err)
	}
}
