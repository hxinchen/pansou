package collection

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"pansou/model"
)

func TestNormalizeKeywordAndCooldown(t *testing.T) {
	if DefaultConfig().ScheduleInterval != time.Minute {
		t.Fatalf("default schedule interval = %v", DefaultConfig().ScheduleInterval)
	}
	if got := NormalizeKeyword("  Foo\t BAR\n资源  "); got != "foo bar 资源" {
		t.Fatalf("NormalizeKeyword() = %q", got)
	}

	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	keyword := Keyword{Cooldown: 12 * time.Hour}
	if got := CalculateNextEligibleAt(keyword, now, DefaultCooldown); !got.Equal(now.Add(12 * time.Hour)) {
		t.Fatalf("custom cooldown result = %v", got)
	}
	keyword.Cooldown = 0
	if got := CalculateNextEligibleAt(keyword, now, 48*time.Hour); !got.Equal(now.Add(48 * time.Hour)) {
		t.Fatalf("fallback cooldown result = %v", got)
	}

	future := now.Add(time.Minute)
	keyword.NextEligibleAt = &future
	if IsEligible(keyword, now) {
		t.Fatal("future keyword should not be eligible")
	}
	if !IsEligible(keyword, future) {
		t.Fatal("keyword should become eligible at next_eligible_at")
	}
}

func TestDetermineRunStatus(t *testing.T) {
	tests := []struct {
		name         string
		anyData      bool
		anyCompleted bool
		want         RunStatus
	}{
		{"data wins", true, false, StatusSuccess},
		{"normal empty", false, true, StatusSuccessEmpty},
		{"technical failure", false, false, StatusFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := DetermineRunStatus(test.anyData, test.anyCompleted); got != test.want {
				t.Fatalf("DetermineRunStatus() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSourceSummaryJSONMap(t *testing.T) {
	summary := SourceSummary{"tg": {Key: "tg", Status: string(StatusSuccess), Attempts: 1}}
	if got := summary.JSONMap(); len(got) != 1 {
		t.Fatalf("JSONMap() = %+v", got)
	}
	if _, err := json.Marshal(summary.JSONMap()); err != nil {
		t.Fatalf("JSONMap is not marshalable: %v", err)
	}
}

func TestShouldQueueLinkCheck(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	base := LinkCheckCandidate{ResourceID: 1, URL: "https://example.test/share"}
	if !ShouldQueueLinkCheck(LinkCheckCandidate{ResourceID: 1, URL: base.URL, IsNew: true, Status: DetectionPending}, now, DefaultLinkCheckStale) {
		t.Fatal("new resource should be queued")
	}
	recent := now.Add(-6 * 24 * time.Hour)
	base.Status = DetectionInvalid
	base.LastCheckedAt = &recent
	if ShouldQueueLinkCheck(base, now, DefaultLinkCheckStale) {
		t.Fatal("recent invalid resource should not be rechecked")
	}
	stale := now.Add(-7 * 24 * time.Hour)
	base.LastCheckedAt = &stale
	if !ShouldQueueLinkCheck(base, now, DefaultLinkCheckStale) {
		t.Fatal("seven-day-old invalid resource should be rechecked")
	}
	base.Status = DetectionValid
	if ShouldQueueLinkCheck(base, now, DefaultLinkCheckStale) {
		t.Fatal("valid resource should not be periodically queued")
	}
}

func TestAdaptCurrentSearch(t *testing.T) {
	searcher := AdaptCurrentSearch(func(keyword string, channels []string, concurrency int, forceRefresh bool, resultType string, sourceType string, plugins []string, cloudTypes []string, extra map[string]interface{}) (model.SearchResponse, error) {
		if keyword != "Alpha" || len(channels) != 1 || channels[0] != "channel-a" || concurrency != 3 || !forceRefresh || resultType != "all" || sourceType != "tg" || len(plugins) != 0 || len(cloudTypes) != 1 || extra["key"] != "value" {
			t.Fatalf("adapted search arguments were not preserved")
		}
		return model.SearchResponse{Total: 1}, nil
	})
	response, err := searcher.Search(context.Background(), SearchRequest{
		Keyword: Keyword{Value: "Alpha"},
		Source: Source{
			Type: "tg", Channels: []string{"channel-a"}, Concurrency: 3,
			ResultType: "all", CloudTypes: []string{"quark"}, Extra: map[string]interface{}{"key": "value"},
		},
		ForceRefresh: true,
	})
	if err != nil || response.Total != 1 {
		t.Fatalf("adapted search result = %+v, %v", response, err)
	}
}
