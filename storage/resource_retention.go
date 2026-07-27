package storage

import (
	"context"
	"fmt"
)

func (s *Store) DeleteTerminalResources(ctx context.Context, batchSize int) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("storage is disabled")
	}
	if batchSize <= 0 || batchSize > 10000 {
		batchSize = 1000
	}
	command, err := s.pool.Exec(ctx, `WITH doomed AS (
		SELECT id FROM resources
		WHERE check_status IN ('invalid', 'expired', 'cancelled', 'violation')
		ORDER BY check_status, last_checked_at, id
		FOR UPDATE SKIP LOCKED
		LIMIT $1
	) DELETE FROM resources r USING doomed WHERE r.id=doomed.id`, batchSize)
	if err != nil {
		return 0, fmt.Errorf("delete terminal resources: %w", err)
	}
	return command.RowsAffected(), nil
}

func (s *Store) CleanupTerminalResources(ctx context.Context, batchSize, maxBatches int) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("storage is disabled")
	}
	if batchSize <= 0 || batchSize > 10000 {
		batchSize = 1000
	}
	if maxBatches <= 0 || maxBatches > 1000 {
		maxBatches = 100
	}
	var total int64
	for batch := 0; batch < maxBatches; batch++ {
		deleted, err := s.DeleteTerminalResources(ctx, batchSize)
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
