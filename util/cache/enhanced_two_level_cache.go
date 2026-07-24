package cache

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"pansou/config"
)

// EnhancedTwoLevelCache 改进的两级缓存
type EnhancedTwoLevelCache struct {
	memory     *ShardedMemoryCache
	disk       *ShardedDiskCache
	mutex      sync.RWMutex
	serializer Serializer
	memoryHits atomic.Uint64
	diskHits   atomic.Uint64
	misses     atomic.Uint64
	diskErrors atomic.Uint64
}

// CacheStats is a process-lifetime snapshot of cache effectiveness and current
// payload occupancy. Metadata and Go map overhead are intentionally excluded
// from the byte counters.
type CacheStats struct {
	MemoryHits  uint64  `json:"memory_hits"`
	DiskHits    uint64  `json:"disk_hits"`
	Misses      uint64  `json:"misses"`
	DiskErrors  uint64  `json:"disk_errors"`
	Lookups     uint64  `json:"lookups"`
	HitRate     float64 `json:"hit_rate"`
	MemoryItems int     `json:"memory_items"`
	MemoryBytes int64   `json:"memory_bytes"`
	DiskItems   int     `json:"disk_items"`
	DiskBytes   int64   `json:"disk_bytes"`
}

// NewEnhancedTwoLevelCache 创建新的改进两级缓存
func NewEnhancedTwoLevelCache() (*EnhancedTwoLevelCache, error) {
	memCacheSizeMB, diskCacheSizeMB := splitCacheBudget(config.AppConfig.CacheMaxSizeMB)
	memCacheMaxItems := 5000

	memCache := NewShardedMemoryCache(memCacheMaxItems, memCacheSizeMB)
	memCache.StartCleanupTask()

	// 创建优化的分片磁盘缓存，使用动态分片数量
	diskCache, err := NewOptimizedShardedDiskCache(config.AppConfig.CachePath, diskCacheSizeMB)
	if err != nil {
		return nil, err
	}

	// 创建序列化器
	serializer := NewGobSerializer()

	// 设置内存缓存的磁盘缓存引用，用于LRU淘汰时的备份
	memCache.SetDiskCacheReference(diskCache)

	return &EnhancedTwoLevelCache{
		memory:     memCache,
		disk:       diskCache,
		serializer: serializer,
	}, nil
}

// splitCacheBudget keeps the configured cache size as a combined memory and
// disk payload budget. The old layout allocated 60% for memory plus 100% for
// disk, so CACHE_MAX_SIZE was not a real upper bound.
func splitCacheBudget(totalMB int) (memoryMB, diskMB int) {
	if totalMB <= 1 {
		return 0, 1
	}
	memoryMB = totalMB * 3 / 5
	diskMB = totalMB - memoryMB
	return memoryMB, diskMB
}

// Set 设置缓存
func (c *EnhancedTwoLevelCache) Set(key string, data []byte, ttl time.Duration) error {
	// 获取当前时间作为最后修改时间
	now := time.Now()

	// 先设置内存缓存（这是快速操作，直接在当前goroutine中执行）
	c.memory.SetWithTimestamp(key, data, ttl, now)

	// 异步设置磁盘缓存（这是IO操作，可能较慢）
	go func(k string, d []byte, t time.Duration) {
		// 使用独立的goroutine写入磁盘，避免阻塞调用者
		_ = c.disk.Set(k, d, t)
	}(key, data, ttl)

	return nil
}

// SetMemoryOnly 仅更新内存缓存
func (c *EnhancedTwoLevelCache) SetMemoryOnly(key string, data []byte, ttl time.Duration) error {
	now := time.Now()

	// 只更新内存缓存，不触发磁盘写入
	c.memory.SetWithTimestamp(key, data, ttl, now)

	return nil
}

// SetBothLevels 更新内存和磁盘缓存
func (c *EnhancedTwoLevelCache) SetBothLevels(key string, data []byte, ttl time.Duration) error {
	now := time.Now()

	// 同步更新内存缓存
	c.memory.SetWithTimestamp(key, data, ttl, now)

	// 同步更新磁盘缓存，确保数据立即写入
	return c.disk.Set(key, data, ttl)
}

// SetWithFinalFlag 根据结果状态选择更新策略
func (c *EnhancedTwoLevelCache) SetWithFinalFlag(key string, data []byte, ttl time.Duration, isFinal bool) error {
	if isFinal {
		return c.SetBothLevels(key, data, ttl)
	} else {
		return c.SetMemoryOnly(key, data, ttl)
	}
}

// Get 获取缓存
func (c *EnhancedTwoLevelCache) Get(key string) ([]byte, bool, error) {

	// 检查内存缓存
	data, _, memHit := c.memory.GetWithTimestamp(key)
	if memHit {
		c.memoryHits.Add(1)
		return data, true, nil
	}

	// 尝试从磁盘读取数据
	diskData, remainingTTL, diskHit, diskErr := c.disk.GetWithTTL(key)
	if diskErr == nil && diskHit {
		c.diskHits.Add(1)
		// 磁盘缓存命中，更新内存缓存
		diskLastModified, _ := c.disk.GetLastModified(key)
		c.memory.SetWithTimestamp(key, diskData, remainingTTL, diskLastModified)
		return diskData, true, nil
	}
	if diskErr != nil {
		c.diskErrors.Add(1)
	}
	c.misses.Add(1)

	return nil, false, nil
}

// Stats returns a concurrency-safe snapshot without scanning cache payloads.
func (c *EnhancedTwoLevelCache) Stats() CacheStats {
	if c == nil {
		return CacheStats{}
	}
	memoryHits := c.memoryHits.Load()
	diskHits := c.diskHits.Load()
	misses := c.misses.Load()
	lookups := memoryHits + diskHits + misses
	stats := CacheStats{
		MemoryHits: memoryHits,
		DiskHits:   diskHits,
		Misses:     misses,
		DiskErrors: c.diskErrors.Load(),
		Lookups:    lookups,
	}
	if lookups > 0 {
		stats.HitRate = float64(memoryHits+diskHits) / float64(lookups)
	}
	if c.memory != nil {
		stats.MemoryItems, stats.MemoryBytes = c.memory.Stats()
	}
	if c.disk != nil {
		stats.DiskItems, stats.DiskBytes = c.disk.Stats()
	}
	return stats
}

// Delete 删除缓存
func (c *EnhancedTwoLevelCache) Delete(key string) error {
	// 从内存缓存删除
	c.memory.Delete(key)

	// 从磁盘缓存删除
	return c.disk.Delete(key)
}

// Clear 清空所有缓存
func (c *EnhancedTwoLevelCache) Clear() error {
	// 清空内存缓存
	c.memory.Clear()

	// 清空磁盘缓存
	return c.disk.Clear()
}

// 设置序列化器
func (c *EnhancedTwoLevelCache) SetSerializer(serializer Serializer) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.serializer = serializer
}

// 获取序列化器
func (c *EnhancedTwoLevelCache) GetSerializer() Serializer {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.serializer
}

// FlushMemoryToDisk 将内存缓存中的所有数据刷新到磁盘
func (c *EnhancedTwoLevelCache) FlushMemoryToDisk() error {
	// 获取内存缓存中的所有键值对
	allItems := c.memory.GetAllItems()

	var lastErr error

	for key, item := range allItems {
		// 同步写入到磁盘缓存
		if err := c.disk.Set(key, item.Data, item.TTL); err != nil {
			fmt.Printf("[内存同步] 同步失败: %s -> %v\n", key, err)
			lastErr = err
			continue
		}
	}

	return lastErr
}
