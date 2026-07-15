package storage

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestBuildAPIRequestLogWhereMonitoringFilters(t *testing.T) {
	where, args, err := buildAPIRequestLogWhere(APIRequestLogFilter{
		StatusFamilies: []string{"2xx", "4xx", "5xx", "429"},
		CacheStatuses:  []string{"hit", "miss", "refresh", "bypass", "not_applicable", "not_recorded"},
	})
	if err != nil {
		t.Fatalf("buildAPIRequestLogWhere: %v", err)
	}
	for _, fragment := range []string{
		"l.status_code>=200 AND l.status_code<300",
		"l.status_code>=400 AND l.status_code<500",
		"l.status_code>=500 AND l.status_code<600",
		"l.status_code=429",
		"COALESCE(NULLIF(lower(btrim(l.cache_status)),''),'not_recorded')=ANY(",
	} {
		if !strings.Contains(where, fragment) {
			t.Fatalf("where clause %q does not contain %q", where, fragment)
		}
	}
	wantArgs := []any{[]string{"bypass", "hit", "miss", "not_applicable", "not_recorded", "refresh"}}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", args, wantArgs)
	}
}

func TestBuildAPIRequestLogWhereRejectsInvalidMonitoringFilters(t *testing.T) {
	for _, filter := range []APIRequestLogFilter{
		{StatusFamilies: []string{"2xx) OR TRUE --"}},
		{CacheStatuses: []string{"hit') OR TRUE --"}},
	} {
		if _, _, err := buildAPIRequestLogWhere(filter); !errors.Is(err, ErrInvalid) {
			t.Fatalf("error = %v, want ErrInvalid", err)
		}
	}
}

func TestAPIRequestLogPresentationNormalization(t *testing.T) {
	if got := normalizeAPIRequestLogCacheStatus(""); got != "not_recorded" {
		t.Fatalf("blank cache status = %q, want not_recorded", got)
	}
	if got := normalizeAPIRequestLogCacheStatus(" HIT "); got != "hit" {
		t.Fatalf("cache status = %q, want hit", got)
	}
	for input, want := range map[string]string{
		"127.0.0.1":    "internal",
		"::1":          "internal",
		"203.0.113.10": "203.0.113.10",
	} {
		if got := normalizeAPIRequestLogSourceIP(input); got != want {
			t.Errorf("normalizeAPIRequestLogSourceIP(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCacheHitRateUsesOnlyHitsAndMisses(t *testing.T) {
	if got := cacheHitRate(3, 1); got != 0.75 {
		t.Fatalf("cacheHitRate(3,1) = %v, want 0.75", got)
	}
	if got := cacheHitRate(0, 0); got != 0 {
		t.Fatalf("cacheHitRate(0,0) = %v, want 0", got)
	}
}
