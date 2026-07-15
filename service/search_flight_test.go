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
