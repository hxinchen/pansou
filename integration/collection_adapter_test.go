package integration

import (
	"context"
	"testing"
	"time"

	"pansou/collection"
	"pansou/storage"
)

func TestBatchSnapshotPreservesRuntimeKeywordFields(t *testing.T) {
	cooldown := time.Hour
	next := time.Now().Add(time.Minute)
	input := collection.Keyword{
		ID: 7, Value: "Example", Normalized: "example", KeywordType: "general",
		SourceType: "manual", SourceKey: "operator", Enabled: true, Priority: 8,
		Cooldown: cooldown, NextEligibleAt: &next,
	}
	run := storage.CollectionRun{
		ID: 9,
		Items: []storage.CollectionRunItem{{
			ID: 11, RunID: 9, KeywordID: pointer(int64(7)), Keyword: "Example",
			NormalizedKeyword: "example", KeywordType: "general", Priority: 8,
		}},
	}
	batch := toCollectionBatch(run)
	snapshots := map[string]collection.Keyword{input.Normalized: input}
	for index := range batch.Items {
		if snapshot, exists := snapshots[batch.Items[index].Keyword.Normalized]; exists {
			batch.Items[index].Keyword = snapshot
		}
	}
	got := batch.Items[0].Keyword
	if got.Cooldown != cooldown || got.SourceKey != "operator" || !got.Enabled || got.NextEligibleAt == nil || !got.NextEligibleAt.Equal(next) {
		t.Fatalf("keyword snapshot lost runtime fields: %+v", got)
	}
}

func TestStoredBatchSnapshotRestoresCooldown(t *testing.T) {
	seconds := int64(3600)
	run := storage.CollectionRun{ID: 9, Items: []storage.CollectionRunItem{{
		ID: 11, RunID: 9, Keyword: "Example", NormalizedKeyword: "example",
		KeywordType: "general", CooldownSeconds: &seconds,
	}}}
	batch := toCollectionBatch(run)
	if got := batch.Items[0].Keyword.Cooldown; got != time.Hour {
		t.Fatalf("Cooldown = %v, want 1h", got)
	}
}

func TestClaimPendingRejectsDisabledRepository(t *testing.T) {
	var repository *CollectionRepository
	claimed, err := repository.ClaimPending(context.Background())
	if claimed != nil || err == nil {
		t.Fatalf("ClaimPending() = %+v, %v", claimed, err)
	}
}

func TestCollectionSourceIdentityUsesExecutedSource(t *testing.T) {
	tests := []struct {
		name       string
		source     collection.Source
		wantType   string
		wantSource string
	}{
		{name: "configured plugin key", source: collection.Source{Type: "plugin", Key: "plugin:xdyh"}, wantType: "plugin", wantSource: "plugin:xdyh"},
		{name: "single channel fallback", source: collection.Source{Type: " TG ", Channels: []string{" channel-a "}}, wantType: "tg", wantSource: "channel-a"},
		{name: "single plugin fallback", source: collection.Source{Type: "plugin", Plugins: []string{" pansearch "}}, wantType: "plugin", wantSource: "pansearch"},
		{name: "ambiguous plugins stay empty", source: collection.Source{Type: "plugin", Plugins: []string{"one", "two"}}, wantType: "plugin"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotType, gotSource := collectionSourceIdentity(test.source)
			if gotType != test.wantType || gotSource != test.wantSource {
				t.Fatalf("collectionSourceIdentity() = %q, %q, want %q, %q", gotType, gotSource, test.wantType, test.wantSource)
			}
		})
	}
}

func pointer[T any](value T) *T { return &value }
