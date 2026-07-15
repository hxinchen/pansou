package storage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"pansou/model"
)

func TestPostgresSourceContributionsAndAttribution(t *testing.T) {
	now := time.Date(2026, time.July, 15, 10, 0, 0, 0, time.UTC)
	store := newPostgresTestStore(t, now)
	ctx := context.Background()

	resourceIDs := make([]int64, 3)
	for index := range resourceIDs {
		key := fmt.Sprintf("source-contribution-%d", index+1)
		if err := store.pool.QueryRow(ctx, `
			INSERT INTO resources (normalized_url, url, first_seen_at, last_seen_at)
			VALUES ($1, $2, $3, $3)
			RETURNING id`, key, "https://example.com/"+key, now).Scan(&resourceIDs[index]); err != nil {
			t.Fatalf("insert resource %d: %v", index, err)
		}
	}
	type sourceFixture struct {
		resourceID int64
		sourceType string
		sourceKey  string
		identity   string
		count      int64
		subSource  string
	}
	fixtures := []sourceFixture{
		{resourceID: resourceIDs[0], sourceType: "plugin", sourceKey: "xdyh", identity: "xdyh-alpha-1", count: 2, subSource: "Alpha 站"},
		{resourceID: resourceIDs[0], sourceType: "plugin", sourceKey: "xdyh", identity: "xdyh-beta-1", count: 3, subSource: "Beta 站"},
		{resourceID: resourceIDs[1], sourceType: "plugin", sourceKey: "xdyh", identity: "xdyh-alpha-2", count: 4, subSource: "Alpha 站"},
		{resourceID: resourceIDs[1], sourceType: "plugin", sourceKey: "xdyh", identity: "xdyh-unknown", count: 5},
		{resourceID: resourceIDs[2], sourceType: "plugin", sourceKey: "beta", identity: "beta-1", count: 14},
		{resourceID: resourceIDs[1], sourceType: "plugin", sourceKey: "odd' plugin %", identity: "odd-1", count: 7, subSource: "Odd 站"},
		{resourceID: resourceIDs[0], sourceType: "tg", sourceKey: "channel-a", identity: "tg-1", count: 6},
	}
	for _, fixture := range fixtures {
		metadata := map[string]any{}
		if fixture.subSource != "" {
			metadata["sub_source"] = fixture.subSource
		}
		if _, err := store.pool.Exec(ctx, `
			INSERT INTO resource_sources (
				resource_id, source_type, source_key, source_identity,
				discovered_at, first_seen_at, last_seen_at, discovery_count, source_metadata
			) VALUES ($1, $2, $3, $4, $5, $5, $5, $6, $7::jsonb)`,
			fixture.resourceID, fixture.sourceType, fixture.sourceKey, fixture.identity,
			now, fixture.count, metadataJSON(metadata)); err != nil {
			t.Fatalf("insert source %s/%s/%s: %v", fixture.sourceType, fixture.sourceKey, fixture.identity, err)
		}
	}

	firstPage, err := store.ListSourceContributions(ctx, SourceContributionFilter{
		SourceType: "plugin", Page: 1, PageSize: 1, SortBy: "discovery_count", SortDir: "desc",
	})
	if err != nil {
		t.Fatalf("ListSourceContributions first page: %v", err)
	}
	if firstPage.Total != 3 || len(firstPage.Items) != 1 || firstPage.Items[0].SourceKey != "xdyh" ||
		firstPage.Items[0].ResourceCount != 2 || firstPage.Items[0].DiscoveryCount != 14 {
		t.Fatalf("first contribution page = %+v", firstPage)
	}
	secondPage, err := store.ListSourceContributions(ctx, SourceContributionFilter{
		SourceType: "plugin", Page: 2, PageSize: 1, SortBy: "discovery_count", SortDir: "desc",
	})
	if err != nil || len(secondPage.Items) != 1 || secondPage.Items[0].SourceKey != "beta" {
		t.Fatalf("second contribution page = %+v, err=%v", secondPage, err)
	}
	keyPage, err := store.ListSourceContributions(ctx, SourceContributionFilter{
		SourceType: "plugin", PageSize: 10, SortBy: "source_key", SortDir: "asc",
	})
	if err != nil || len(keyPage.Items) != 3 || keyPage.Items[0].SourceKey != "beta" || keyPage.Items[2].SourceKey != "xdyh" {
		t.Fatalf("source-key contribution order = %+v, err=%v", keyPage.Items, err)
	}

	detail, err := store.GetSourceContribution(ctx, "plugin", "xdyh", SourceContributionDetailFilter{
		Page: 1, PageSize: 1, SortBy: "resource_count", SortDir: "desc",
	})
	if err != nil {
		t.Fatalf("GetSourceContribution: %v", err)
	}
	if detail.ResourceCount != 2 || detail.DiscoveryCount != 14 ||
		detail.TypeResourceCount != 3 || detail.TypeDiscoveryCount != 35 ||
		detail.IdentifiedResourceCount != 2 || detail.SubSourcePairCount != 3 ||
		!almostEqual(detail.ResourceShare, 2.0/3.0) || !almostEqual(detail.DiscoveryShare, 0.4) ||
		!almostEqual(detail.SubSourceCoverage, 1) {
		t.Fatalf("source contribution detail = %+v", detail)
	}
	if detail.SubSources.Total != 2 || len(detail.SubSources.Items) != 1 ||
		detail.SubSources.Items[0].SubSource != "Alpha 站" ||
		detail.SubSources.Items[0].ResourceCount != 2 ||
		detail.SubSources.Items[0].DiscoveryCount != 6 ||
		!almostEqual(detail.SubSources.Items[0].PairShare, 2.0/3.0) {
		t.Fatalf("sub-source contribution page = %+v", detail.SubSources)
	}
	if _, err := store.GetSourceContribution(ctx, "plugin", "odd' plugin %", SourceContributionDetailFilter{}); err != nil {
		t.Fatalf("special-character source key: %v", err)
	}
	if _, err := store.GetSourceContribution(ctx, "plugin", "missing", SourceContributionDetailFilter{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing source error = %v, want ErrNotFound", err)
	}

	attributedURL := "https://example.com/authoritative-source"
	response := model.SearchResponse{
		Results: []model.SearchResult{{
			UniqueID: "wrong-plugin-1", SubSource: "内部站点", Title: "authoritative",
			Links: []model.Link{{Type: "quark", URL: attributedURL}},
		}},
		MergedByType: model.MergedLinks{"quark": {{
			URL: attributedURL, Source: "plugin:wrong-plugin", SubSource: "内部站点",
		}}},
	}
	if _, err := store.UpsertSearchResponseFromSource(
		ctx, "keyword", DefaultKeywordType, "manual", "plugin", "plugin:xdyh", response,
	); err != nil {
		t.Fatalf("UpsertSearchResponseFromSource: %v", err)
	}
	resources, err := store.ListResources(ctx, ResourceFilter{Query: "authoritative-source", IncludeInvalid: true})
	if err != nil || len(resources.Items) != 1 || len(resources.Items[0].Sources) != 1 {
		t.Fatalf("attributed resources = %+v, err=%v", resources, err)
	}
	source := resources.Items[0].Sources[0]
	if source.SourceType != "plugin" || source.SourceKey != "xdyh" ||
		metadataString(source.SourceMetadata, "sub_source") != "内部站点" {
		t.Fatalf("authoritative source = %+v", source)
	}
	if got := resources.Items[0].ToSearchResult().SubSource; got != "内部站点" {
		t.Fatalf("stored SearchResult sub_source = %q", got)
	}
	if got := resources.Items[0].ToMergedLink().SubSource; got != "内部站点" {
		t.Fatalf("stored MergedLink sub_source = %q", got)
	}
}

func almostEqual(left, right float64) bool {
	return math.Abs(left-right) < 0.0000001
}
