package storage

import (
	"context"
	"testing"
)

func TestDisabledStoreDiagnostics(t *testing.T) {
	var store *Store
	if stats, ok := store.PoolStats(); ok || stats != (PoolStats{}) {
		t.Fatalf("PoolStats = %+v/%v, want empty/false", stats, ok)
	}
	if _, err := store.SlowQueries(context.Background(), 10); err == nil {
		t.Fatal("SlowQueries on disabled store returned no error")
	}
}
