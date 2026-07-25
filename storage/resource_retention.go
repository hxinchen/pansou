package storage

import (
	"context"
	"fmt"
	"time"
)

const defaultTerminalResourceRetention = 60 * 24 * time.Hour

func TerminalResourceRetentionCutoff(now time.Time, retention time.Duration) time.Time {
	if retention <= 0 {
		retention = defaultTerminalResourceRetention
	}
	return now.Add(-retention)
}

func (s *Store) DeleteTerminalResourcesBefore(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("storage is disabled")
	}
	if cutoff.IsZero() {
		cutoff = TerminalResourceRetentionCutoff(s.now(), 0)
	}
	if batchSize <= 0 || batchSize > 10000 {
		batchSize = 1000
	}
	command, err := s.pool.Exec(ctx, `WITH doomed AS (
		SELECT id FROM resources
		WHERE check_status IN ('invalid', 'expired', 'cancelled', 'violation')
			AND last_checked_at < $1
			AND last_seen_at < $1
		ORDER BY last_checked_at, id
		FOR UPDATE SKIP LOCKED
		LIMIT $2
	) DELETE FROM resources r USING doomed WHERE r.id=doomed.id`, cutoff, batchSize)
	if err != nil {
		return 0, fmt.Errorf("delete terminal resources: %w", err)
	}
	return command.RowsAffected(), nil
}

func (s *Store) CleanupTerminalResources(ctx context.Context, now time.Time, retention time.Duration, batchSize, maxBatches int) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("storage is disabled")
	}
	if now.IsZero() {
		now = s.now()
	}
	if batchSize <= 0 || batchSize > 10000 {
		batchSize = 1000
	}
	if maxBatches <= 0 || maxBatches > 1000 {
		maxBatches = 100
	}
	cutoff := TerminalResourceRetentionCutoff(now, retention)
	var total int64
	for batch := 0; batch < maxBatches; batch++ {
		deleted, err := s.DeleteTerminalResourcesBefore(ctx, cutoff, batchSize)
		if err != nil {
			return total, err
		}
		total += deleted
		if deleted < int64(batchSize) {
			break
		}
	}
	return total, nil
}
