package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/singleflight"

	"pansou/model"
)

func TestExecuteSearchFlightContinuesAfterCallerDeadline(t *testing.T) {
	var group singleflight.Group
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	request := ContextSearchRequest{Keyword: "test", ResultType: "merged_by_type", SourceType: "all"}
	search := func(ctx context.Context) (model.SearchResponse, error) {
		calls.Add(1)
		close(started)
		<-release
		if err := ctx.Err(); err != nil {
			return model.SearchResponse{}, err
		}
		return model.SearchResponse{Total: 1, Completion: model.SearchCompletionComplete}, nil
	}

	callerCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	firstDone := make(chan error, 1)
	go func() {
		_, err := executeSearchFlight(callerCtx, &group, "test", request, search)
		firstDone <- err
	}()
	<-started
	firstResult := make(chan error, 1)
	go func() {
		firstResult <- <-firstDone
		close(release)
	}()

	response, err := executeSearchFlight(context.Background(), &group, "test", request, search)
	if firstErr := <-firstResult; firstErr != context.DeadlineExceeded {
		t.Fatalf("first caller error = %v, want deadline exceeded", firstErr)
	}
	if err != nil {
		t.Fatalf("second caller error = %v", err)
	}
	if response.Total != 1 {
		t.Fatalf("second caller response = %+v", response)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("shared search calls = %d, want 1", got)
	}
}

func TestExecuteSearchFlightExecutionBudgetReturnsPartial(t *testing.T) {
	var group singleflight.Group
	request := ContextSearchRequest{
		Keyword: "deadline", ResultType: "merged_by_type", SourceType: "all",
		ExecutionBudget: 20 * time.Millisecond,
	}
	response, err := executeSearchFlight(context.Background(), &group, "deadline", request, func(ctx context.Context) (model.SearchResponse, error) {
		<-ctx.Done()
		return model.SearchResponse{
			Total: 1, PartialSources: []string{"plugin:slow", "plugin:slow"},
			Execution: &model.SearchExecution{Requested: 2, Executed: 1, Completed: 1, Cancelled: 1},
		}, ctx.Err()
	})
	if err != nil {
		t.Fatalf("deadline search error = %v", err)
	}
	if response.Completion != model.SearchCompletionPartial || response.StopReason != model.SearchStopReasonDeadline {
		t.Fatalf("deadline response = %+v", response)
	}
	if response.Total != 1 || response.ElapsedMS < 15 {
		t.Fatalf("partial result or elapsed time missing: %+v", response)
	}
	if len(response.PartialSources) != 1 || response.PartialSources[0] != "plugin:slow" {
		t.Fatalf("partial sources = %#v", response.PartialSources)
	}
	if status := response.SourceStatuses["plugin:slow"]; status.Message != model.SearchStopReasonDeadline {
		t.Fatalf("deadline source status = %+v", status)
	}
}

func TestSearchFlightKeySeparatesExecutionBudgets(t *testing.T) {
	request := ContextSearchRequest{Keyword: "same", ResultType: "all", SourceType: "all"}
	withoutBudget, err := buildSearchFlightKey("test", request)
	if err != nil {
		t.Fatal(err)
	}
	request.ExecutionBudget = 12 * time.Second
	withBudget, err := buildSearchFlightKey("test", request)
	if err != nil {
		t.Fatal(err)
	}
	if withoutBudget == withBudget {
		t.Fatal("execution policies must not share a singleflight key")
	}
}

func TestNestedSearchFlightsShareOneExecutionBudget(t *testing.T) {
	var outerGroup, innerGroup singleflight.Group
	request := ContextSearchRequest{
		Keyword: "nested", ResultType: "all", SourceType: "all",
		ExecutionBudget: 25 * time.Millisecond,
	}
	started := time.Now()
	response, err := executeSearchFlight(context.Background(), &outerGroup, "outer", request, func(outerCtx context.Context) (model.SearchResponse, error) {
		return executeSearchFlight(outerCtx, &innerGroup, "inner", request, func(innerCtx context.Context) (model.SearchResponse, error) {
			<-innerCtx.Done()
			return model.SearchResponse{
				Total:   1,
				Results: []model.SearchResult{{UniqueID: "completed-before-deadline"}},
			}, innerCtx.Err()
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed >= 45*time.Millisecond {
		t.Fatalf("nested search restarted the execution budget: %s", elapsed)
	}
	if response.StopReason != model.SearchStopReasonDeadline {
		t.Fatalf("nested deadline response = %+v", response)
	}
	if response.Total != 1 || len(response.Results) != 1 {
		t.Fatalf("nested deadline discarded completed results: %+v", response)
	}
}

func TestExecuteSearchFlightReplaysRecentlyCompletedResult(t *testing.T) {
	var group singleflight.Group
	var calls atomic.Int32
	request := ContextSearchRequest{Keyword: "replay", ResultType: "merged_by_type", SourceType: "all"}
	search := func(context.Context) (model.SearchResponse, error) {
		calls.Add(1)
		return model.SearchResponse{Total: 2, Completion: model.SearchCompletionPartial}, nil
	}

	first, err := executeSearchFlight(context.Background(), &group, "replay", request, search)
	if err != nil || first.Total != 2 {
		t.Fatalf("first result = %+v, err = %v", first, err)
	}
	second, err := executeSearchFlight(context.Background(), &group, "replay", request, search)
	if err != nil || second.Total != 2 || second.Completion != model.SearchCompletionPartial {
		t.Fatalf("replayed result = %+v, err = %v", second, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("shared search calls = %d, want 1", got)
	}
}
