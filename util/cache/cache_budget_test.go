package cache

import (
	"bytes"
	"testing"
	"time"
)

func TestSplitCacheBudgetUsesConfiguredTotal(t *testing.T) {
	memoryMB, diskMB := splitCacheBudget(100)
	if memoryMB != 60 || diskMB != 40 || memoryMB+diskMB != 100 {
		t.Fatalf("splitCacheBudget(100) = %d/%d", memoryMB, diskMB)
	}

	memoryMB, diskMB = splitCacheBudget(1)
	if memoryMB != 0 || diskMB != 1 {
		t.Fatalf("splitCacheBudget(1) = %d/%d", memoryMB, diskMB)
	}
}

func TestDiskPromotionPreservesRemainingTTL(t *testing.T) {
	disk, err := NewShardedDiskCache(t.TempDir(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := disk.Set("key", []byte("value"), time.Minute); err != nil {
		t.Fatal(err)
	}
	shard := disk.shards[0]
	shard.mutex.Lock()
	shard.metadata["key"].Expiry = time.Now().Add(30 * time.Millisecond)
	shard.mutex.Unlock()

	cache := &EnhancedTwoLevelCache{
		memory:     NewShardedMemoryCache(16, 1),
		disk:       disk,
		serializer: NewGobSerializer(),
	}
	data, hit, err := cache.Get("key")
	if err != nil || !hit || !bytes.Equal(data, []byte("value")) {
		t.Fatalf("initial Get = %q/%v/%v", data, hit, err)
	}

	time.Sleep(60 * time.Millisecond)
	if _, hit, err := cache.Get("key"); err != nil || hit {
		t.Fatalf("expired Get hit=%v err=%v", hit, err)
	}
}

func TestShardedMemoryCacheRejectsOversizedItem(t *testing.T) {
	cache := NewShardedMemoryCache(16, 1)
	data := make([]byte, cache.sizePerShard+1)
	cache.Set("oversized", data, time.Minute)
	if _, hit := cache.Get("oversized"); hit {
		t.Fatal("oversized item was cached")
	}
	for _, shard := range cache.shards {
		if shard.currSize > cache.sizePerShard {
			t.Fatalf("shard size %d exceeds limit %d", shard.currSize, cache.sizePerShard)
		}
	}
}

func TestDiskCacheRejectsOversizedItem(t *testing.T) {
	cache, err := NewDiskCache(t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Set("oversized", make([]byte, 1024*1024+1), time.Minute); err == nil {
		t.Fatal("expected oversized item error")
	}
	if cache.currSize != 0 {
		t.Fatalf("cache size = %d, want 0", cache.currSize)
	}
}

func TestEnhancedCacheStatsTracksHitsMissesAndOccupancy(t *testing.T) {
	disk, err := NewShardedDiskCache(t.TempDir(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	cache := &EnhancedTwoLevelCache{
		memory:     NewShardedMemoryCache(16, 1),
		disk:       disk,
		serializer: NewGobSerializer(),
	}
	if err := cache.SetBothLevels("memory-hit", []byte("memory"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := cache.Get("memory-hit"); err != nil || !hit {
		t.Fatalf("memory Get hit=%v err=%v", hit, err)
	}
	if err := disk.Set("disk-hit", []byte("disk"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := cache.Get("disk-hit"); err != nil || !hit {
		t.Fatalf("disk Get hit=%v err=%v", hit, err)
	}
	if _, hit, err := cache.Get("miss"); err != nil || hit {
		t.Fatalf("missing Get hit=%v err=%v", hit, err)
	}

	stats := cache.Stats()
	if stats.MemoryHits != 1 || stats.DiskHits != 1 || stats.Misses != 1 || stats.Lookups != 3 {
		t.Fatalf("unexpected counters: %+v", stats)
	}
	if stats.HitRate < 0.66 || stats.HitRate > 0.67 {
		t.Fatalf("hit rate = %f, want about 2/3", stats.HitRate)
	}
	if stats.MemoryItems != 2 || stats.MemoryBytes != int64(len("memory")+len("disk")) {
		t.Fatalf("unexpected memory occupancy: %+v", stats)
	}
	if stats.DiskItems != 2 || stats.DiskBytes != int64(len("memory")+len("disk")) {
		t.Fatalf("unexpected disk occupancy: %+v", stats)
	}
}
