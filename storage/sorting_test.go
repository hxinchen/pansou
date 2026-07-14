package storage

import (
	"errors"
	"reflect"
	"sort"
	"testing"
)

func TestBuildSortClause(t *testing.T) {
	fields := map[string]sortField{
		"name":  {Expression: "lower(name)", TieBreaker: "id", NullsLast: true},
		"count": {Expression: "item_count", TieBreaker: "id"},
	}
	tests := []struct {
		name, by, dir, want string
		wantErr             bool
	}{
		{name: "default", want: "created_at DESC, id DESC"},
		{name: "ascending default direction", by: "name", want: "lower(name) ASC NULLS LAST, id ASC"},
		{name: "descending", by: "count", dir: "DESC", want: "item_count DESC, id DESC"},
		{name: "direction without field", dir: "asc", wantErr: true},
		{name: "unknown field", by: "name; DROP TABLE users", dir: "asc", wantErr: true},
		{name: "unknown direction", by: "name", dir: "sideways", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := buildSortClause(test.by, test.dir, "created_at DESC, id DESC", fields)
			if test.wantErr {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("error = %v, want ErrInvalid", err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("buildSortClause() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestSortFieldWhitelists(t *testing.T) {
	tests := []struct {
		name   string
		fields map[string]sortField
		want   []string
	}{
		{name: "resources", fields: resourceSortFields, want: []string{"check_status", "discovery_count", "first_seen_at", "last_seen_at", "platform", "resource", "source_count"}},
		{name: "keywords", fields: keywordSortFields, want: []string{"cooldown_seconds", "enabled", "keyword", "keyword_type", "next_eligible_at", "priority"}},
		{name: "keyword API sources", fields: keywordAPISourceSortFields, want: []string{"last_item_count", "last_status", "name", "request_url", "sync_interval_seconds"}},
		{name: "keyword API sync runs", fields: keywordAPISyncRunSortFields, want: []string{"progress", "source_name", "started_at", "status", "trigger", "unique_count"}},
		{name: "keyword API sync iterations", fields: keywordAPISyncIterationSortFields, want: []string{"detail", "duration_ms", "http_status", "new_keyword_count", "raw_item_count", "sequence", "status"}},
		{name: "collection runs", fields: collectionRunSortFields, want: []string{"duplicate_count", "duration", "id", "new_count", "progress", "started_at", "status", "trigger"}},
		{name: "users", fields: userSortFields, want: []string{"api_key", "expires_at", "last_login_at", "role", "rps_limit", "status", "username"}},
		{name: "API request logs", fields: apiRequestLogSortFields, want: []string{"cache_status", "created_at", "duration_ms", "keyword", "request", "result_count", "source_ip", "status_code", "username"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := make([]string, 0, len(test.fields))
			for key := range test.fields {
				got = append(got, key)
				for _, direction := range []string{"asc", "desc"} {
					if _, err := buildSortClause(key, direction, "id DESC", test.fields); err != nil {
						t.Fatalf("buildSortClause(%q, %q): %v", key, direction, err)
					}
				}
			}
			sort.Strings(got)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("whitelist = %v, want %v", got, test.want)
			}
		})
	}
}

func TestSortFieldSpecialValueRules(t *testing.T) {
	tests := []struct {
		name   string
		field  string
		fields map[string]sortField
		want   string
	}{
		{name: "eligible null is immediately available", field: "next_eligible_at", fields: keywordSortFields, want: "COALESCE(next_eligible_at,'-infinity'::timestamptz) ASC, id ASC"},
		{name: "expiry null is permanent", field: "expires_at", fields: userSortFields, want: "COALESCE(expires_at,'infinity'::timestamptz) ASC, id ASC"},
		{name: "iteration detail nulls last", field: "detail", fields: keywordAPISyncIterationSortFields, want: "COALESCE(NULLIF(error_message,''),NULLIF(samples->>0,'')) ASC NULLS LAST, id ASC"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := buildSortClause(test.field, "asc", "id DESC", test.fields)
			if err != nil || got != test.want {
				t.Fatalf("buildSortClause() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}
