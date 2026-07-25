package storage

import (
	"context"
	"testing"
	"time"
)

func TestNormalizedKeywordLinksFollowKeywordLifecycle(t *testing.T) {
	now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	store := newPostgresTestStore(t, now)
	ctx := context.Background()
	keyword, err := store.CreateKeyword(ctx, CreateKeywordInput{Keyword: "Original", KeywordType: "general"})
	if err != nil {
		t.Fatalf("create keyword: %v", err)
	}
	result, err := store.UpsertResource(ctx, ResourceInput{
		URL: "https://normalized.example/item", Title: "normalized", Content: "shared detail",
		DiscoveredAt: now, Keyword: "Original", KeywordType: "general",
	})
	if err != nil {
		t.Fatalf("upsert resource: %v", err)
	}
	second, err := store.UpsertResource(ctx, ResourceInput{
		URL: "https://normalized.example/second", Title: "normalized second", Content: "shared detail",
		DiscoveredAt: now, Keyword: "Original", KeywordType: "general",
	})
	if err != nil {
		t.Fatalf("upsert second resource: %v", err)
	}
	var contentRows, termRows, linkRows int
	if err := store.pool.QueryRow(ctx, "SELECT count(*) FROM resource_contents").Scan(&contentRows); err != nil {
		t.Fatalf("count contents: %v", err)
	}
	if err := store.pool.QueryRow(ctx, "SELECT count(*) FROM resource_keyword_terms").Scan(&termRows); err != nil {
		t.Fatalf("count terms: %v", err)
	}
	if err := store.pool.QueryRow(ctx, "SELECT count(*) FROM resource_keyword_links").Scan(&linkRows); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if contentRows != 1 || termRows != 1 || linkRows != 2 || second.Resource.ID == result.Resource.ID {
		t.Fatalf("deduplicated rows content/terms/links=%d/%d/%d second=%d first=%d",
			contentRows, termRows, linkRows, second.Resource.ID, result.Resource.ID)
	}

	renamed, keywordType := "Renamed", "movie"
	updated, err := store.UpdateKeyword(ctx, keyword.ID, UpdateKeywordInput{Keyword: &renamed, KeywordType: &keywordType})
	if err != nil {
		t.Fatalf("update keyword: %v", err)
	}
	var normalized, linkedType string
	var discoveryCount int64
	if err := store.pool.QueryRow(ctx, `SELECT term.normalized_keyword, term.keyword_type, link.discovery_count
		FROM resource_keyword_links link
		JOIN resource_keyword_terms term ON term.id=link.term_id
		WHERE link.resource_id=$1 AND link.keyword_id=$2`, result.Resource.ID, updated.ID).
		Scan(&normalized, &linkedType, &discoveryCount); err != nil {
		t.Fatalf("load renamed normalized link: %v", err)
	}
	if normalized != NormalizeKeyword(renamed) || linkedType != keywordType || discoveryCount != 1 {
		t.Fatalf("renamed normalized link = %q/%q/%d", normalized, linkedType, discoveryCount)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM resource_keyword_links
		WHERE keyword_id=$1`, updated.ID).Scan(&linkRows); err != nil || linkRows != 2 {
		t.Fatalf("renamed link count = %d, err=%v, want 2", linkRows, err)
	}

	if err := store.DeleteKeyword(ctx, keyword.ID); err != nil {
		t.Fatalf("delete keyword: %v", err)
	}
	var newKeywordID *int64
	if err := store.pool.QueryRow(ctx, `SELECT keyword_id FROM resource_keyword_links WHERE resource_id=$1`, result.Resource.ID).Scan(&newKeywordID); err != nil {
		t.Fatalf("load normalized link after delete: %v", err)
	}
	if newKeywordID != nil {
		t.Fatalf("normalized keyword ID after delete = %v, want nil", newKeywordID)
	}
}

func TestNormalizedPayloadReadPathsIgnoreLegacyStorage(t *testing.T) {
	now := time.Date(2026, time.July, 25, 13, 0, 0, 0, time.UTC)
	store := newPostgresTestStore(t, now)
	ctx := context.Background()
	if _, err := store.CreateKeyword(ctx, CreateKeywordInput{Keyword: "Original", KeywordType: "movie"}); err != nil {
		t.Fatalf("create keyword: %v", err)
	}
	result, err := store.UpsertResource(ctx, ResourceInput{
		URL: "https://normalized.example/read-path", Title: "normalized title", Content: "normalized body",
		DiscoveredAt: now, Keyword: "Original", KeywordType: "movie",
	})
	if err != nil {
		t.Fatalf("upsert resource: %v", err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE resources SET content='legacy stale' WHERE id=$1`, result.Resource.ID); err != nil {
		t.Fatalf("stale legacy content: %v", err)
	}
	if _, err := store.pool.Exec(ctx, `DELETE FROM resource_keywords WHERE resource_id=$1`, result.Resource.ID); err != nil {
		t.Fatalf("delete legacy keyword link: %v", err)
	}

	page, err := store.ListResources(ctx, ResourceFilter{
		Keyword: "Original", KeywordType: "movie", Query: "normalized body", IncludeInvalid: true,
	})
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("normalized ListResources page=%+v err=%v", page, err)
	}
	if page.Items[0].Content != "normalized body" || len(page.Items[0].Keywords) != 1 || page.Items[0].Keywords[0].Keyword != "Original" {
		t.Fatalf("normalized ListResources item=%+v", page.Items[0])
	}

	searchPage, err := store.SearchResources(ctx, ResourceFilter{Keyword: "Original", TitleQuery: "normalized", IncludeInvalid: true})
	if err != nil || len(searchPage.Items) != 1 {
		t.Fatalf("normalized SearchResources page=%+v err=%v", searchPage, err)
	}
	if searchPage.Items[0].Content != "" || len(searchPage.Items[0].Keywords) != 1 {
		t.Fatalf("search path loaded content or missed normalized keywords: %+v", searchPage.Items[0])
	}

	detail, err := store.GetResource(ctx, result.Resource.ID)
	if err != nil || detail.Content != "normalized body" || detail.KeywordCount != 1 {
		t.Fatalf("normalized GetResource detail=%+v err=%v", detail, err)
	}
	keywords, err := store.ListResourceKeywords(ctx, result.Resource.ID, ResourceAssociationFilter{})
	if err != nil || keywords.Total != 1 || len(keywords.Items) != 1 || keywords.Items[0].NormalizedKeyword != "original" {
		t.Fatalf("normalized ListResourceKeywords page=%+v err=%v", keywords, err)
	}
}

func TestCreateKeywordAttachesExistingNormalizedLinks(t *testing.T) {
	now := time.Date(2026, time.July, 25, 14, 0, 0, 0, time.UTC)
	store := newPostgresTestStore(t, now)
	ctx := context.Background()
	result, err := store.UpsertResource(ctx, ResourceInput{
		URL: "https://normalized.example/orphan", Title: "orphan title", Keyword: "Orphan", DiscoveredAt: now,
	})
	if err != nil {
		t.Fatalf("upsert orphan keyword resource: %v", err)
	}
	keyword, err := store.CreateKeyword(ctx, CreateKeywordInput{Keyword: "Orphan"})
	if err != nil {
		t.Fatalf("create managed keyword: %v", err)
	}
	var normalizedKeywordID *int64
	if err := store.pool.QueryRow(ctx, `SELECT keyword_id FROM resource_keyword_links WHERE resource_id=$1`, result.Resource.ID).Scan(&normalizedKeywordID); err != nil {
		t.Fatalf("load attached normalized keyword: %v", err)
	}
	if normalizedKeywordID == nil || *normalizedKeywordID != keyword.ID {
		t.Fatalf("attached normalized keyword ID = %v, want %d", normalizedKeywordID, keyword.ID)
	}
}

func TestNormalizedWritesLeaveLegacyPayloadsEmpty(t *testing.T) {
	now := time.Date(2026, time.July, 25, 15, 0, 0, 0, time.UTC)
	store := newPostgresTestStore(t, now)
	ctx := context.Background()
	result, err := store.UpsertResource(ctx, ResourceInput{
		URL: "https://normalized.example/new-writes", Title: "normalized write", Content: "new payload",
		Keyword: "New Keyword", DiscoveredAt: now,
	})
	if err != nil {
		t.Fatalf("upsert normalized resource: %v", err)
	}
	var legacyContent string
	var legacyKeywordRows int
	if err := store.pool.QueryRow(ctx, `SELECT content FROM resources WHERE id=$1`, result.Resource.ID).Scan(&legacyContent); err != nil {
		t.Fatalf("load legacy content: %v", err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM resource_keywords WHERE resource_id=$1`, result.Resource.ID).Scan(&legacyKeywordRows); err != nil {
		t.Fatalf("count legacy keyword rows: %v", err)
	}
	if legacyContent != "" || legacyKeywordRows != 0 {
		t.Fatalf("legacy writes content=%q keyword_rows=%d", legacyContent, legacyKeywordRows)
	}
	detail, err := store.GetResource(ctx, result.Resource.ID)
	if err != nil || detail.Content != "new payload" || detail.KeywordCount != 1 {
		t.Fatalf("normalized detail=%+v err=%v", detail, err)
	}
}
