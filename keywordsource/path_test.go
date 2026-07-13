package keywordsource

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestParsePath(t *testing.T) {
	t.Parallel()
	segments, err := ParsePath("data.items[].meta[2].keyword")
	if err != nil {
		t.Fatal(err)
	}
	want := []PathSegment{
		{Kind: SegmentField, Field: "data"},
		{Kind: SegmentField, Field: "items"},
		{Kind: SegmentWildcard},
		{Kind: SegmentField, Field: "meta"},
		{Kind: SegmentIndex, Index: 2},
		{Kind: SegmentField, Field: "keyword"},
	}
	if !reflect.DeepEqual(segments, want) {
		t.Fatalf("ParsePath() = %#v, want %#v", segments, want)
	}

	root, err := ParsePath("[0].name")
	if err != nil || len(root) != 2 || root[0].Kind != SegmentIndex {
		t.Fatalf("root array ParsePath() = %#v, %v", root, err)
	}
}

func TestParsePathRejectsInvalidSyntax(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"", ".data", "data.", "data..name", "data.[0]", "data[-1]", "data[x]", "data[", "data item"} {
		path := path
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			_, err := ParsePath(path)
			if !errors.Is(err, ErrInvalidPath) {
				t.Fatalf("ParsePath(%q) error = %v", path, err)
			}
		})
	}
}

func TestExtractObjectArrayAndScalars(t *testing.T) {
	t.Parallel()
	document := map[string]any{
		"data": map[string]any{
			"items": []any{
				map[string]any{"name": "Alpha", "enabled": true},
				map[string]any{"name": json.Number("42"), "enabled": false},
				map[string]any{"enabled": true},
			},
			"keywords": []any{"One", json.Number("2"), true},
		},
	}
	values, err := Extract(document, "data.items[].name")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Alpha", "42"}; !reflect.DeepEqual(values, want) {
		t.Fatalf("Extract object array = %#v, want %#v", values, want)
	}
	values, err = Extract(document, "data.keywords")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"One", "2", "true"}; !reflect.DeepEqual(values, want) {
		t.Fatalf("Extract terminal array = %#v, want %#v", values, want)
	}
	values, err = Extract(document, "data.items[1].name")
	if err != nil || !reflect.DeepEqual(values, []string{"42"}) {
		t.Fatalf("Extract index = %#v, %v", values, err)
	}
}

func TestExtractRejectsObjectTerminal(t *testing.T) {
	t.Parallel()
	_, err := Extract(map[string]any{"data": map[string]any{"name": "x"}}, "data")
	if !errors.Is(err, ErrObjectResult) {
		t.Fatalf("Extract error = %v", err)
	}
}

func TestExtractKeywordsNormalizesAndDeduplicates(t *testing.T) {
	t.Parallel()
	document := map[string]any{"values": []any{"  Foo\t BAR ", "foo bar", "", "资源  搜索", "资源 搜索"}}
	result, err := ExtractKeywords(document, "values[]")
	if err != nil {
		t.Fatal(err)
	}
	if result.RawCount != 5 || result.UniqueCount != 2 {
		t.Fatalf("counts = raw %d unique %d", result.RawCount, result.UniqueCount)
	}
	want := []KeywordValue{
		{Value: "Foo\t BAR", Normalized: "foo bar"},
		{Value: "资源  搜索", Normalized: "资源 搜索"},
	}
	if !reflect.DeepEqual(result.Values, want) {
		t.Fatalf("values = %#v, want %#v", result.Values, want)
	}
	if got := NormalizeKeyword("  Go\n语言\t教程 "); got != "go 语言 教程" {
		t.Fatalf("NormalizeKeyword() = %q", got)
	}
}

func TestDiscoverFields(t *testing.T) {
	t.Parallel()
	document := map[string]any{
		"data": map[string]any{
			"items": []any{
				map[string]any{"name": "Alpha", "meta": map[string]any{"keyword": "A"}},
				map[string]any{"name": "Beta", "meta": map[string]any{"keyword": "B"}},
			},
			"keywords": []any{"one", "two", "two"},
			"page":     json.Number("3"),
		},
	}
	candidates := DiscoverFields(document)
	wantPaths := []string{"data.items[].meta.keyword", "data.items[].name", "data.keywords[]", "data.page"}
	paths := make([]string, len(candidates))
	for i, candidate := range candidates {
		paths[i] = candidate.Path
	}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("paths = %#v, want %#v", paths, wantPaths)
	}
	if candidates[2].Count != 3 || !reflect.DeepEqual(candidates[2].Samples, []string{"one", "two"}) {
		t.Fatalf("keyword candidate = %#v", candidates[2])
	}
}

func TestDiscoverFieldsCountsAllPathsAndLaterRepeatedValues(t *testing.T) {
	t.Parallel()
	first := make(map[string]any, 600)
	for index := 0; index < 600; index++ {
		first[fmt.Sprintf("field_%03d", index)] = fmt.Sprintf("value-%d", index)
	}
	document := []any{first, map[string]any{"field_000": "later-value"}}
	candidates := DiscoverFields(document)
	if len(candidates) != 600 {
		t.Fatalf("candidate count = %d, want 600", len(candidates))
	}
	var repeated *FieldCandidate
	for index := range candidates {
		if candidates[index].Path == "[].field_000" {
			repeated = &candidates[index]
			break
		}
	}
	if repeated == nil {
		t.Fatal("repeated candidate is missing")
	}
	if repeated.Count != 2 || !reflect.DeepEqual(repeated.Samples, []string{"value-0", "later-value"}) {
		t.Fatalf("repeated candidate = %#v", repeated)
	}
}

func TestDiscoverFieldsTraversesDeepBoundedJSON(t *testing.T) {
	t.Parallel()
	var document any = "value"
	for depth := 0; depth < 30; depth++ {
		document = map[string]any{"nested": document}
	}
	candidates := DiscoverFields(document)
	if len(candidates) != 1 || candidates[0].Count != 1 {
		t.Fatalf("deep candidates = %#v", candidates)
	}
}
