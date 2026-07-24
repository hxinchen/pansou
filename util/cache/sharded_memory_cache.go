package cache

import (
	"hash/fnv"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// 全局清理任务相关变量（单例模式）
var (
	globalCleanupTicker *time.Ticker
	globalCleanupOnce   sync.Once
	registeredCaches    []cleanupTarget
	cacheRegistryMutex  sync.RWMutex
)

// 清理目标接口
type cleanupTarget interface {
	CleanExpired()
}

// 分片内存缓存项
type shardedMemoryCacheItem struct {
	data         []byte
	expiry       time.Time
	lastUsed     int64 // 使用原子操作的时间戳
	lastModified time.Time
	size         int
}

// 单个分片
type memoryCacheShard struct {
	items    map[string]*shardedMemoryCacheItem
	mutex    sync.RWMutex
	currSize int64
}

// 分片内存缓存
type ShardedMemoryCache struct {
	shards         []*memoryCacheShard
	shardMask      uint32 // 用于快速取模的掩码
	maxItems       int
	maxSize        int64
	itemsPerShard  int
	sizePerShard   int64
	diskCache      *ShardedDiskCache // 磁盘缓存引用
	diskCacheMutex sync.RWMutex      // 磁盘缓存引用的保护锁
}

// 创建新的分片内存缓存
func NewShardedMemoryCache(maxItems int, maxSizeMB int) *ShardedMemoryCache {
	// 动态确定分片数量：基于CPU核心数，但至少4个，最多64个
	shardCount := runtime.NumCPU() * 2
	if shardCount < 4 {
		shardCount = 4
	}
	if shardCount > 64 {
		shardCount = 64
	}

	// 确保分片数是2的幂，便于使用掩码进行快速取模
	shardCount = nextPowerOfTwo(shardCount)
	if maxItems > 0 && shardCount > maxItems {
		shardCount = maxPowerOfTwoAtMost(maxItems)
	}

	totalSize := int64(maxSizeMB) * 1024 * 1024
	itemsPerShard := maxItems / shardCount
	sizePerShard := totalSize / int64(shardCount)

	shards := make([]*memoryCacheShard, shardCount)
	for i := 0; i < shardCount; i++ {
		shards[i] = &memoryCacheShard{
			items: make(map[string]*shardedMemoryCacheItem),
		}
	}

	return &ShardedMemoryCache{
		shards:        shards,
		shardMask:     uint32(shardCount - 1), // 用于快速取模
		maxItems:      maxItems,
		maxSize:       totalSize,
		itemsPerShard: itemsPerShard,
		sizePerShard:  sizePerShard,
	}
}

// 获取下一个2的幂
func nextPowerOfTwo(n int) int {
	if n <= 1 {
		return 1
	}
	n--
	n |= n >> 1
	n |= n >> 2
	n |= n >> 4
	n |= n >> 8
	n |= n >> 16
	return n + 1
}

// 获取分片
func (c *ShardedMemoryCache) getShard(key string) *memoryCacheShard {
	h := fnv.New32a()
	h.Write([]byte(key))
	shardIndex := h.Sum32() & c.shardMask // 使用掩码进行快速取模
	return c.shards[shardIndex]
}

// 设置缓存
func (c *ShardedMemoryCache) Set(key string, data []byte, ttl time.Duration) {
	c.SetWithTimestamp(key, data, ttl, time.Now())
}

// SetWithTimestamp 设置缓存，并指定最后修改时间
func (c *ShardedMemoryCache) SetWithTimestamp(key string, data []byte, ttl time.Duration, lastModified time.Time) {
	shard := c.getShard(key)
	shard.mutex.Lock()
	defer shard.mutex.Unlock()

	if c.itemsPerShard <= 0 || int64(len(data)) > c.sizePerShard {
		return
	}

	// Remove an old version before capacity checks. This prevents an update from
	// double-subtracting its size when it is also selected for eviction.
	if item, exists := shard.items[key]; exists {
		atomic.AddInt64(&shard.currSize, -int64(item.size))
		delete(shard.items, key)
	}

	// 创建新的缓存项
	now := time.Now()
	item := &shardedMemoryCacheItem{
		data:         data,
		expiry:       now.Add(ttl),
		lastUsed:     now.UnixNano(),
		lastModified: lastModified,
		size:         len(data),
	}

	// Keep evicting until the incoming item fits. A single eviction was not
	// sufficient for a large item and allowed the configured memory budget to be
	// exceeded.
	for len(shard.items) >= c.itemsPerShard || shard.currSize+int64(len(data)) > c.sizePerShard {
		if !c.evictFromShard(shard) {
			return
		}
	}

	// 存储新项
	shard.items[key] = item
	atomic.AddInt64(&shard.currSize, int64(len(data)))
}

// 获取缓存
func (c *ShardedMemoryCache) Get(key string) ([]byte, bool) {
	shard := c.getShard(key)
	shard.mutex.RLock()
	item, exists := shard.items[key]
	shard.mutex.RUnlock()

	if !exists {
		return nil, false
	}

	// 检查是否过期
	if time.Now().After(item.expiry) {
		shard.mutex.Lock()
		delete(shard.items, key)
		atomic.AddInt64(&shard.currSize, -int64(item.size))
		shard.mutex.Unlock()
		return nil, false
	}

	// 原子操作更新最后使用时间，避免额外的锁
	atomic.StoreInt64(&item.lastUsed, time.Now().UnixNano())

	return item.data, true
}

// GetWithTimestamp 获取缓存及其最后修改时间
func (c *ShardedMemoryCache) GetWithTimestamp(key string) ([]byte, time.Time, bool) {
	shard := c.getShard(key)
	shard.mutex.RLock()
	item, exists := shard.items[key]
	shard.mutex.RUnlock()

	if !exists {
		return nil, time.Time{}, false
	}

	// 检查是否过期
	if time.Now().After(item.expiry) {
		shard.mutex.Lock()
		delete(shard.items, key)
		atomic.AddInt64(&shard.currSize, -int64(item.size))
		shard.mutex.Unlock()
		return nil, time.Time{}, false
	}

	// 原子操作更新最后使用时间
	atomic.StoreInt64(&item.lastUsed, time.Now().UnixNano())

	return item.data, item.lastModified, true
}

// GetLastModified 获取缓存项的最后修改时间
func (c *ShardedMemoryCache) GetLastModified(key string) (time.Time, bool) {
	shard := c.getShard(key)
	shard.mutex.RLock()
	defer shard.mutex.RUnlock()

	item, exists := shard.items[key]
	if !exists {
		return time.Time{}, false
	}

	// 检查是否过期
	if time.Now().After(item.expiry) {
		return time.Time{}, false
	}

	return item.lastModified, true
}

// Stats returns the number and total payload bytes of currently retained
// entries. Expired entries are removed by the shared cleanup task.
func (c *ShardedMemoryCache) Stats() (items int, bytes int64) {
	if c == nil {
		return 0, 0
	}
	for _, shard := range c.shards {
		shard.mutex.RLock()
		items += len(shard.items)
		bytes += atomic.LoadInt64(&shard.currSize)
		shard.mutex.RUnlock()
	}
	return items, bytes
}

// 从指定分片中驱逐最久未使用的项（带磁盘备份）
func (c *ShardedMemoryCache) evictFromShard(shard *memoryCacheShard) bool {
	var oldestKey string
	var oldestItem *shardedMemoryCacheItem
	var oldestTime int64 = 9223372036854775807 // int64最大值

	for k, v := range shard.items {
		lastUsed := atomic.LoadInt64(&v.lastUsed)
		if lastUsed < oldestTime {
			oldestKey = k
			oldestItem = v
			oldestTime = lastUsed
		}
	}

	// 如果找到了最久未使用的项，删除它
	if oldestKey != "" && oldestItem != nil {
		// 🔥 关键优化：淘汰前检查是否需要刷盘保护
		diskCache := c.getDiskCacheReference()
		if time.Now().Before(oldestItem.expiry) && diskCache != nil {
			// 数据还没过期，异步刷新到磁盘保存
			go func(key string, data []byte, expiry time.Time) {
				ttl := time.Until(expiry)
				if ttl > 0 {
					diskCache.Set(key, data, ttl) // 保持相同TTL
				}
			}(oldestKey, oldestItem.data, oldestItem.expiry)
		}

		// 从内存中删除
		atomic.AddInt64(&shard.currSize, -int64(oldestItem.size))
		delete(shard.items, oldestKey)
		return true
	}
	return false
}

func maxPowerOfTwoAtMost(n int) int {
	result := 1
	for result*2 <= n {
		result *= 2
	}
	return result
}

// 清理过期项
func (c *ShardedMemoryCache) CleanExpired() {
	now := time.Now()

	// 并行清理所有分片
	var wg sync.WaitGroup
	for _, shard := range c.shards {
		wg.Add(1)
		go func(s *memoryCacheShard) {
			defer wg.Done()
			s.mutex.Lock()
			defer s.mutex.Unlock()

			for k, v := range s.items {
				if now.After(v.expiry) {
					atomic.AddInt64(&s.currSize, -int64(v.size))
					delete(s.items, k)
				}
			}
		}(shard)
	}
	wg.Wait()
}

// Delete 删除指定键的缓存项
func (c *ShardedMemoryCache) Delete(key string) {
	shard := c.getShard(key)
	shard.mutex.Lock()
	defer shard.mutex.Unlock()

	if item, exists := shard.items[key]; exists {
		atomic.AddInt64(&shard.currSize, -int64(item.size))
		delete(shard.items, key)
	}
}

// Clear 清空所有缓存项
func (c *ShardedMemoryCache) Clear() {
	// 并行清理所有分片
	var wg sync.WaitGroup
	for _, shard := range c.shards {
		wg.Add(1)
		go func(s *memoryCacheShard) {
			defer wg.Done()
			s.mutex.Lock()
			defer s.mutex.Unlock()

			s.items = make(map[string]*shardedMemoryCacheItem)
			atomic.StoreInt64(&s.currSize, 0)
		}(shard)
	}
	wg.Wait()
}

// 启动全局清理任务（单例模式）
func startGlobalCleanupTask() {
	globalCleanupOnce.Do(func() {
		globalCleanupTicker = time.NewTicker(5 * time.Minute)
		go func() {
			for range globalCleanupTicker.C {
				cacheRegistryMutex.RLock()
				caches := make([]cleanupTarget, len(registeredCaches))
				copy(caches, registeredCaches)
				cacheRegistryMutex.RUnlock()

				// 并行清理所有注册的缓存
				for _, cache := range caches {
					go cache.CleanExpired()
				}
			}
		}()
	})
}

// 注册缓存到全局清理任务
func registerForCleanup(cache cleanupTarget) {
	cacheRegistryMutex.Lock()
	defer cacheRegistryMutex.Unlock()
	registeredCaches = append(registeredCaches, cache)
}

// 启动定期清理（修改为使用单例模式）
func (c *ShardedMemoryCache) StartCleanupTask() {
	registerForCleanup(c)
	startGlobalCleanupTask()
}

// SetDiskCacheReference 设置磁盘缓存引用
func (c *ShardedMemoryCache) SetDiskCacheReference(diskCache *ShardedDiskCache) {
	c.diskCacheMutex.Lock()
	defer c.diskCacheMutex.Unlock()
	c.diskCache = diskCache
}

// getDiskCacheReference 获取磁盘缓存引用
func (c *ShardedMemoryCache) getDiskCacheReference() *ShardedDiskCache {
	c.diskCacheMutex.RLock()
	defer c.diskCacheMutex.RUnlock()
	return c.diskCache
}

// MemoryCacheItem 内存缓存项结构（用于导出）
type MemoryCacheItem struct {
	Data []byte
	TTL  time.Duration
}

// GetAllItems 获取内存缓存中的所有项
func (c *ShardedMemoryCache) GetAllItems() map[string]*MemoryCacheItem {
	result := make(map[string]*MemoryCacheItem)
	now := time.Now()

	// 遍历所有分片
	for _, shard := range c.shards {
		shard.mutex.RLock()
		for key, item := range shard.items {
			// 检查是否过期
			if !item.expiry.IsZero() && now.After(item.expiry) {
				continue // 跳过过期项
			}

			// 计算剩余TTL
			var ttl time.Duration
			if !item.expiry.IsZero() {
				ttl = item.expiry.Sub(now)
				if ttl <= 0 {
					continue // 跳过即将过期的项
				}
			}

			result[key] = &MemoryCacheItem{
				Data: item.data,
				TTL:  ttl,
			}
		}
		shard.mutex.RUnlock()
	}

	return result
}
