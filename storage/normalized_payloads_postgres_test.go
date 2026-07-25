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
	var oldKeywordID, newKeywordID *int64
	if err := store.pool.QueryRow(ctx, `SELECT keyword_id FROM resource_keywords WHERE resource_id=$1`, result.Resource.ID).Scan(&oldKeywordID); err != nil {
		t.Fatalf("load legacy link after delete: %v", err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT keyword_id FROM resource_keyword_links WHERE resource_id=$1`, result.Resource.ID).Scan(&newKeywordID); err != nil {
		t.Fatalf("load normalized link after delete: %v", err)
	}
	if oldKeywordID != nil || newKeywordID != nil {
		t.Fatalf("keyword IDs after delete = legacy:%v normalized:%v, want nil", oldKeywordID, newKeywordID)
	}
}
