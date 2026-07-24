package api

import (
	"context"
	"net/http"
	"runtime"
	runtimemetrics "runtime/metrics"
	"time"

	"github.com/gin-gonic/gin"

	"pansou/model"
	"pansou/storage"
	"pansou/util/cache"
)

type runtimeMemoryDiagnostics struct {
	HeapAllocBytes    uint64 `json:"heap_alloc_bytes"`
	HeapInUseBytes    uint64 `json:"heap_in_use_bytes"`
	HeapIdleBytes     uint64 `json:"heap_idle_bytes"`
	HeapReleasedBytes uint64 `json:"heap_released_bytes"`
	HeapObjects       uint64 `json:"heap_objects"`
	StackInUseBytes   uint64 `json:"stack_in_use_bytes"`
	TotalSystemBytes  uint64 `json:"total_system_bytes"`
	NextGCBytes       uint64 `json:"next_gc_bytes"`
	GCCycles          uint32 `json:"gc_cycles"`
	GCPauseTotalNS    uint64 `json:"gc_pause_total_ns"`
	LastGC            string `json:"last_gc,omitempty"`
}

type goRuntimeDiagnostics struct {
	GoVersion      string                   `json:"go_version"`
	Goroutines     int                      `json:"goroutines"`
	CPUs           int                      `json:"cpus"`
	GOMAXPROCS     int                      `json:"gomaxprocs"`
	Memory         runtimeMemoryDiagnostics `json:"memory"`
	RuntimeMetrics map[string]uint64        `json:"runtime_metrics"`
}

type cacheDiagnostics struct {
	Enabled bool              `json:"enabled"`
	Stats   *cache.CacheStats `json:"stats,omitempty"`
}

type slowQueryDiagnostics struct {
	Available bool                `json:"available"`
	Message   string              `json:"message,omitempty"`
	Queries   []storage.SlowQuery `json:"queries,omitempty"`
}

type databaseDiagnostics struct {
	Configured  bool                 `json:"configured"`
	Pool        *storage.PoolStats   `json:"pool,omitempty"`
	SlowQueries slowQueryDiagnostics `json:"slow_queries"`
}

type runtimeDiagnosticsResponse struct {
	GeneratedAt time.Time            `json:"generated_at"`
	Go          goRuntimeDiagnostics `json:"go"`
	Cache       cacheDiagnostics     `json:"cache"`
	Database    databaseDiagnostics  `json:"database"`
}

func (h *AdminHandler) runtimeDiagnostics(c *gin.Context) {
	response := runtimeDiagnosticsResponse{
		GeneratedAt: time.Now().UTC(),
		Go:          collectGoRuntimeDiagnostics(),
		Cache:       cacheDiagnostics{Enabled: h != nil && h.searchCache != nil},
		Database: databaseDiagnostics{
			Configured:  h != nil && h.store != nil,
			SlowQueries: slowQueryDiagnostics{Available: false},
		},
	}
	if h != nil && h.searchCache != nil {
		stats := h.searchCache.Stats()
		response.Cache.Stats = &stats
	}
	if h != nil && h.store != nil {
		if stats, ok := h.store.PoolStats(); ok {
			response.Database.Pool = &stats
		}
		limit := queryInt(c, "slow_query_limit", 10)
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		queries, err := h.store.SlowQueries(ctx, limit)
		cancel()
		if err != nil {
			response.Database.SlowQueries.Message = "pg_stat_statements unavailable"
		} else {
			response.Database.SlowQueries.Available = true
			response.Database.SlowQueries.Queries = queries
		}
	} else {
		response.Database.SlowQueries.Message = "database is disabled"
	}
	c.JSON(http.StatusOK, model.NewSuccessResponse(response))
}

func collectGoRuntimeDiagnostics() goRuntimeDiagnostics {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	diagnostics := goRuntimeDiagnostics{
		GoVersion:  runtime.Version(),
		Goroutines: runtime.NumGoroutine(),
		CPUs:       runtime.NumCPU(),
		GOMAXPROCS: runtime.GOMAXPROCS(0),
		Memory: runtimeMemoryDiagnostics{
			HeapAllocBytes:    memory.HeapAlloc,
			HeapInUseBytes:    memory.HeapInuse,
			HeapIdleBytes:     memory.HeapIdle,
			HeapReleasedBytes: memory.HeapReleased,
			HeapObjects:       memory.HeapObjects,
			StackInUseBytes:   memory.StackInuse,
			TotalSystemBytes:  memory.Sys,
			NextGCBytes:       memory.NextGC,
			GCCycles:          memory.NumGC,
			GCPauseTotalNS:    memory.PauseTotalNs,
		},
		RuntimeMetrics: collectRuntimeMetrics(),
	}
	if memory.LastGC > 0 {
		diagnostics.Memory.LastGC = time.Unix(0, int64(memory.LastGC)).UTC().Format(time.RFC3339Nano)
	}
	return diagnostics
}

func collectRuntimeMetrics() map[string]uint64 {
	names := []string{
		"/gc/cycles/total:gc-cycles",
		"/gc/heap/goal:bytes",
		"/gc/heap/live:bytes",
		"/memory/classes/heap/objects:bytes",
		"/memory/classes/heap/released:bytes",
		"/sched/goroutines:goroutines",
	}
	samples := make([]runtimemetrics.Sample, len(names))
	for index, name := range names {
		samples[index].Name = name
	}
	runtimemetrics.Read(samples)
	values := make(map[string]uint64, len(samples))
	for _, sample := range samples {
		if sample.Value.Kind() == runtimemetrics.KindUint64 {
			values[sample.Name] = sample.Value.Uint64()
		}
	}
	return values
}
