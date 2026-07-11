package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Overview returns the resource-library counters used by the admin dashboard.
func (s *Store) Overview(ctx context.Context) (OverviewStats, error) {
	if s == nil || s.pool == nil {
		return OverviewStats{}, fmt.Errorf("storage is disabled")
	}
	now := s.now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	tomorrow := today.AddDate(0, 0, 1)
	sevenDaysAgo := today.AddDate(0, 0, -6)

	var result OverviewStats
	if err := s.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM resources),
			(SELECT count(*) FROM resources WHERE first_seen_at >= $1 AND first_seen_at < $2),
			(SELECT count(*) FROM resources WHERE first_seen_at >= $3 AND first_seen_at < $2),
			(SELECT count(*) FROM keywords),
			(SELECT count(*) FROM keywords WHERE enabled)`,
		today, tomorrow, sevenDaysAgo,
	).Scan(&result.ResourceCount, &result.TodayNew, &result.LastSevenDaysNew,
		&result.KeywordCount, &result.EnabledKeywordCount); err != nil {
		return OverviewStats{}, fmt.Errorf("load overview counters: %w", err)
	}

	counts := StatusCounts{
		CheckPending: 0, CheckValid: 0, CheckInvalid: 0, CheckUnknown: 0, CheckUnsupported: 0,
	}
	rows, err := s.pool.Query(ctx, `SELECT check_status, count(*) FROM resources
		GROUP BY check_status`)
	if err != nil {
		return OverviewStats{}, fmt.Errorf("load resource status counts: %w", err)
	}
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			return OverviewStats{}, fmt.Errorf("scan resource status count: %w", err)
		}
		counts[status] = count
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return OverviewStats{}, fmt.Errorf("iterate resource status counts: %w", err)
	}
	rows.Close()
	result.StatusCounts = counts

	active, err := scanRun(s.pool.QueryRow(ctx, runSelect+`
		WHERE cr.status IN ('pending','running')`+runGroup+`
		ORDER BY cr.created_at ASC, cr.id ASC LIMIT 1`))
	if err == nil {
		result.ActiveRun = &active
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return OverviewStats{}, fmt.Errorf("load active collection run: %w", err)
	}

	rows, err = s.pool.Query(ctx, `
		SELECT source_type, source_key, count(DISTINCT resource_id), sum(discovery_count)
		FROM resource_sources
		GROUP BY source_type, source_key
		ORDER BY count(DISTINCT resource_id) DESC, sum(discovery_count) DESC,
			source_type, source_key
		LIMIT 10`)
	if err != nil {
		return OverviewStats{}, fmt.Errorf("load source contributions: %w", err)
	}
	result.TopSources = make([]SourceContribution, 0, 10)
	for rows.Next() {
		var contribution SourceContribution
		if err := rows.Scan(&contribution.SourceType, &contribution.SourceKey,
			&contribution.ResourceCount, &contribution.DiscoveryCount); err != nil {
			rows.Close()
			return OverviewStats{}, fmt.Errorf("scan source contribution: %w", err)
		}
		result.TopSources = append(result.TopSources, contribution)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return OverviewStats{}, fmt.Errorf("iterate source contributions: %w", err)
	}
	rows.Close()

	recent, err := s.ListRuns(ctx, RunFilter{Page: 1, PageSize: 5})
	if err != nil {
		return OverviewStats{}, fmt.Errorf("load recent collection runs: %w", err)
	}
	result.RecentRuns = recent.Items
	return result, nil
}

// Trends returns one point per local calendar day, including days with no data.
func (s *Store) Trends(ctx context.Context, days int) ([]TrendPoint, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("storage is disabled")
	}
	if days < 1 {
		days = 7
	}
	if days > 366 {
		days = 366
	}
	now := s.now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	start := today.AddDate(0, 0, -(days - 1))
	end := today.AddDate(0, 0, 1)
	rows, err := s.pool.Query(ctx, `
		SELECT days.day,
			(SELECT count(*) FROM resources r
				WHERE r.first_seen_at >= days.day AND r.first_seen_at < days.day + interval '1 day'),
			COALESCE((SELECT sum(i.found_count) FROM collection_run_items i
				WHERE i.completed_at >= days.day AND i.completed_at < days.day + interval '1 day'), 0),
			(SELECT count(*) FROM resources r
				WHERE r.check_status='valid' AND r.first_seen_at < days.day + interval '1 day')
		FROM generate_series($1::timestamptz, $2::timestamptz - interval '1 day', interval '1 day') AS days(day)
		ORDER BY days.day`, start, end)
	if err != nil {
		return nil, fmt.Errorf("load resource trends: %w", err)
	}
	defer rows.Close()
	points := make([]TrendPoint, 0, days)
	for rows.Next() {
		var point TrendPoint
		if err := rows.Scan(&point.Date, &point.NewCount, &point.Discoveries, &point.ValidCount); err != nil {
			return nil, fmt.Errorf("scan resource trend: %w", err)
		}
		point.NewResources = point.NewCount
		point.ValidResources = point.ValidCount
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate resource trends: %w", err)
	}
	return points, nil
}
