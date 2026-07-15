package storage

import (
	"errors"
	"testing"
)

func TestNormalizeContributionSourceType(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		allowAll bool
		want     string
		wantErr  bool
	}{
		{name: "empty all", allowAll: true},
		{name: "explicit all", value: " ALL ", allowAll: true},
		{name: "plugin", value: " Plugin ", want: "plugin"},
		{name: "telegram", value: "TG", want: "tg"},
		{name: "all rejected by detail", value: "all", wantErr: true},
		{name: "injection rejected", value: "plugin' OR TRUE--", allowAll: true, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeContributionSourceType(test.value, test.allowAll)
			if test.wantErr {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("error = %v, want ErrInvalid", err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("normalizeContributionSourceType() = %q, %v, want %q", got, err, test.want)
			}
		})
	}
}

func TestContributionSortWhitelists(t *testing.T) {
	tests := []struct {
		name   string
		field  string
		dir    string
		fields map[string]sortField
		want   string
	}{
		{name: "source key", field: "source_key", dir: "asc", fields: sourceContributionSortFields, want: "lower(source_key) ASC, ROW(lower(source_type), source_type, source_key) ASC"},
		{name: "resource count", field: "resource_count", dir: "desc", fields: sourceContributionSortFields, want: "resource_count DESC, ROW(lower(source_type || ':' || source_key), source_type, source_key) DESC"},
		{name: "sub-source discovery", field: "discovery_count", dir: "desc", fields: subSourceContributionSortFields, want: "discovery_count DESC, ROW(lower(sub_source), sub_source) DESC"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clause, err := buildSortClause(test.field, test.dir, "default", test.fields)
			if err != nil || clause != test.want {
				t.Fatalf("buildSortClause() = %q, %v, want %q", clause, err, test.want)
			}
		})
	}
	for _, field := range []string{"source_key; DROP TABLE resources", "sub_source DESC--"} {
		if _, err := buildSortClause(field, "asc", "default", sourceContributionSortFields); !errors.Is(err, ErrInvalid) {
			t.Fatalf("field %q error = %v, want ErrInvalid", field, err)
		}
	}
}

func TestCanonicalCollectionSource(t *testing.T) {
	tests := []struct {
		sourceType string
		sourceKey  string
		wantType   string
		wantKey    string
	}{
		{sourceType: " Plugin ", sourceKey: "plugin:xdyh", wantType: "plugin", wantKey: "xdyh"},
		{sourceType: "TG", sourceKey: "TG:Channel-A", wantType: "tg", wantKey: "Channel-A"},
		{sourceType: "external", sourceKey: "plugin:xdyh"},
		{sourceType: "plugin", sourceKey: " "},
	}
	for _, test := range tests {
		got := canonicalCollectionSource(test.sourceType, test.sourceKey)
		if got.sourceType != test.wantType || got.sourceKey != test.wantKey {
			t.Errorf("canonicalCollectionSource(%q, %q) = %+v, want %q/%q", test.sourceType, test.sourceKey, got, test.wantType, test.wantKey)
		}
	}
}

func TestContributionRatio(t *testing.T) {
	if got := ratio(1, 4); got != 0.25 {
		t.Fatalf("ratio(1,4) = %v", got)
	}
	if got := ratio(1, 0); got != 0 {
		t.Fatalf("ratio(1,0) = %v", got)
	}
}
