package storage

import (
	"context"
	"fmt"
	"time"
)

// PoolStats is a point-in-time pgxpool snapshot. Durations are exposed in
// milliseconds to keep the JSON representation explicit.
type PoolStats struct {
	MaxConnections              int32   `json:"max_connections"`
	TotalConnections            int32   `json:"total_connections"`
	AcquiredConnections         int32   `json:"acquired_connections"`
	IdleConnections             int32   `json:"idle_connections"`
	ConstructingConnections     int32   `json:"constructing_connections"`
	AcquireCount                int64   `json:"acquire_count"`
	AcquireDurationMilliseconds float64 `json:"acquire_duration_ms"`
	CanceledAcquireCount        int64   `json:"canceled_acquire_count"`
	EmptyAcquireCount           int64   `json:"empty_acquire_count"`
	NewConnectionsCount         int64   `json:"new_connections_count"`
	MaxLifetimeDestroyCount     int64   `json:"max_lifetime_destroy_count"`
	MaxIdleDestroyCount         int64   `json:"max_idle_destroy_count"`
}

// SlowQuery describes aggregated execution statistics collected by the
// pg_stat_statements extension. Query text is normalized by PostgreSQL.
type SlowQuery struct {
	QueryID           int64   `json:"query_id"`
	Query             string  `json:"query"`
	Calls             int64   `json:"calls"`
	TotalExecTimeMS   float64 `json:"total_exec_time_ms"`
	MeanExecTimeMS    float64 `json:"mean_exec_time_ms"`
	Rows              int64   `json:"rows"`
	SharedBlocksHit   int64   `json:"shared_blocks_hit"`
	SharedBlocksRead  int64   `json:"shared_blocks_read"`
	TempBlocksWritten int64   `json:"temp_blocks_written"`
}

// PoolStats returns local pool counters and does not query PostgreSQL.
func (s *Store) PoolStats() (PoolStats, bool) {
	if s == nil || s.pool == nil {
		return PoolStats{}, false
	}
	stats := s.pool.Stat()
	return PoolStats{
		MaxConnections:              stats.MaxConns(),
		TotalConnections:            stats.TotalConns(),
		AcquiredConnections:         stats.AcquiredConns(),
		IdleConnections:             stats.IdleConns(),
		ConstructingConnections:     stats.ConstructingConns(),
		AcquireCount:                stats.AcquireCount(),
		AcquireDurationMilliseconds: float64(stats.AcquireDuration()) / float64(time.Millisecond),
		CanceledAcquireCount:        stats.CanceledAcquireCount(),
		EmptyAcquireCount:           stats.EmptyAcquireCount(),
		NewConnectionsCount:         stats.NewConnsCount(),
		MaxLifetimeDestroyCount:     stats.MaxLifetimeDestroyCount(),
		MaxIdleDestroyCount:         stats.MaxIdleDestroyCount(),
	}, true
}

// SlowQueries returns the statements with the highest cumulative execution
// time. The caller should treat any error as an unavailable optional metric;
// pg_stat_statements is not required for normal application operation.
func (s *Store) SlowQueries(ctx context.Context, limit int) ([]SlowQuery, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("storage is disabled")
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 50 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT queryid, query, calls::bigint, total_exec_time, mean_exec_time,
		       rows, shared_blks_hit, shared_blks_read, temp_blks_written
		FROM pg_stat_statements
		WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
		  AND query NOT ILIKE '%pg_stat_statements%'
		ORDER BY total_exec_time DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("query pg_stat_statements: %w", err)
	}
	defer rows.Close()

	queries := make([]SlowQuery, 0, limit)
	for rows.Next() {
		var query SlowQuery
		if err := rows.Scan(
			&query.QueryID,
			&query.Query,
			&query.Calls,
			&query.TotalExecTimeMS,
			&query.MeanExecTimeMS,
			&query.Rows,
			&query.SharedBlocksHit,
			&query.SharedBlocksRead,
			&query.TempBlocksWritten,
		); err != nil {
			return nil, fmt.Errorf("scan pg_stat_statements: %w", err)
		}
		queries = append(queries, query)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read pg_stat_statements: %w", err)
	}
	return queries, nil
}
