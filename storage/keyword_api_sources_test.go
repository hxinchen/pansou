package storage

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestNormalizeKeywordAPISourceValues(t *testing.T) {
	got := normalizeKeywordAPISourceValues([]string{" Alpha ", "alpha", "ALPHA", "", " Beta  Value ", "beta value"})
	want := []normalizedKeywordAPIValue{{External: "Alpha", Normalized: "alpha"}, {External: "Beta  Value", Normalized: "beta value"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeKeywordAPISourceValues() = %#v, want %#v", got, want)
	}
}

func TestNormalizeKeywordAPISourceCreateDefaultsAndValidation(t *testing.T) {
	now := time.Date(2026, time.July, 12, 9, 0, 0, 0, time.UTC)
	source, err := normalizeKeywordAPISourceCreate(CreateKeywordAPISourceInput{Name: " Draft "}, now)
	if err != nil {
		t.Fatal(err)
	}
	if source.Name != "Draft" || source.RequestMethod != "GET" || source.BodyType != "none" ||
		source.TimeoutSeconds != 15 || source.SyncIntervalSeconds != 3600 ||
		source.DefaultKeywordType != DefaultKeywordType || !source.DefaultKeywordEnabled || source.NextSyncAt != nil ||
		source.IterationEnabled || source.IterationLocation != "query" || source.IterationPath != "" ||
		source.IterationStart != 0 || source.IterationStep != 20 || source.IterationCount != 1 || source.IterationDelaySeconds != 0 ||
		source.IterationUnlimited || source.IterationNoKeywordStopCount != 0 ||
		source.IterationRandomDelayMinSeconds != 0 || source.IterationRandomDelayMaxSeconds != 0 {
		t.Fatalf("defaults = %+v", source)
	}
	_, err = normalizeKeywordAPISourceCreate(CreateKeywordAPISourceInput{Name: "Enabled", Enabled: true}, now)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("incomplete enabled source error = %v, want ErrInvalid", err)
	}
	enabled, err := normalizeKeywordAPISourceCreate(CreateKeywordAPISourceInput{
		Name: "Enabled", Enabled: true, RequestURL: "https://example.test/api", ResponsePath: "data.items[]",
	}, now)
	if err != nil || enabled.NextSyncAt == nil || !enabled.NextSyncAt.Equal(now) {
		t.Fatalf("enabled source = %+v, %v", enabled, err)
	}
}

func TestNormalizeKeywordAPISourceIterationValidation(t *testing.T) {
	now := time.Date(2026, time.July, 12, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		input CreateKeywordAPISourceInput
	}{
		{name: "missing path", input: CreateKeywordAPISourceInput{Name: "Source", IterationEnabled: true}},
		{name: "bad location", input: CreateKeywordAPISourceInput{Name: "Source", IterationEnabled: true, IterationLocation: "cookie", IterationPath: "page"}},
		{name: "too many requests", input: CreateKeywordAPISourceInput{Name: "Source", IterationCount: 101}},
		{name: "delay too long", input: CreateKeywordAPISourceInput{Name: "Source", IterationDelaySeconds: 3601}},
		{name: "negative no-keyword stop", input: CreateKeywordAPISourceInput{Name: "Source", IterationNoKeywordStopCount: -1}},
		{name: "too many no-keyword rounds", input: CreateKeywordAPISourceInput{Name: "Source", IterationNoKeywordStopCount: 101}},
		{name: "unlimited without stop", input: CreateKeywordAPISourceInput{Name: "Source", IterationEnabled: true, IterationLocation: "query", IterationPath: "page", IterationUnlimited: true}},
		{name: "random minimum too low", input: CreateKeywordAPISourceInput{Name: "Source", IterationRandomDelayMinSeconds: -3601}},
		{name: "random maximum too high", input: CreateKeywordAPISourceInput{Name: "Source", IterationRandomDelayMaxSeconds: 3601}},
		{name: "random range reversed", input: CreateKeywordAPISourceInput{Name: "Source", IterationRandomDelayMinSeconds: 2, IterationRandomDelayMaxSeconds: -1}},
		{name: "raw body", input: CreateKeywordAPISourceInput{Name: "Source", BodyType: "raw", IterationEnabled: true, IterationLocation: "body", IterationPath: "page"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := normalizeKeywordAPISourceCreate(test.input, now); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}
	source, err := normalizeKeywordAPISourceCreate(CreateKeywordAPISourceInput{
		Name: "Source", BodyType: "json", RequestBody: `{}`, IterationEnabled: true,
		IterationLocation: "body", IterationPath: "pagination.offset", IterationStep: -20,
		IterationCount: 10, IterationDelaySeconds: 2, IterationUnlimited: true,
		IterationNoKeywordStopCount: 3, IterationRandomDelayMinSeconds: -2,
		IterationRandomDelayMaxSeconds: 5,
	}, now)
	if err != nil || source.IterationStep != -20 || source.IterationCount != 10 ||
		!source.IterationUnlimited || source.IterationNoKeywordStopCount != 3 ||
		source.IterationRandomDelayMinSeconds != -2 || source.IterationRandomDelayMaxSeconds != 5 {
		t.Fatalf("valid iteration = %+v, %v", source, err)
	}
}

func TestNormalizeKeywordAPISourceSyncCompletion(t *testing.T) {
	status, message, requests, successes, failures, err := normalizeKeywordAPISourceSyncCompletion(KeywordAPISourceSyncInput{})
	if err != nil || status != KeywordAPISourceStatusSuccess || message != "" || requests != 1 || successes != 1 || failures != 0 {
		t.Fatalf("legacy completion = %q %q %d/%d/%d, %v", status, message, requests, successes, failures, err)
	}
	status, message, requests, successes, failures, err = normalizeKeywordAPISourceSyncCompletion(KeywordAPISourceSyncInput{
		Status: KeywordAPISourceStatusPartial, ErrorMessage: " round 2 failed ", RequestCount: 3, SuccessCount: 2, FailureCount: 1,
	})
	if err != nil || status != KeywordAPISourceStatusPartial || message != "round 2 failed" || requests != 3 || successes != 2 || failures != 1 {
		t.Fatalf("partial completion = %q %q %d/%d/%d, %v", status, message, requests, successes, failures, err)
	}
	if _, _, _, _, _, err := normalizeKeywordAPISourceSyncCompletion(KeywordAPISourceSyncInput{
		Status: KeywordAPISourceStatusPartial, RequestCount: 2, SuccessCount: 2,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid partial error = %v", err)
	}
}
