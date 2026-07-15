package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type overviewSnapshotResult struct {
	stats OverviewStats
	err   error
}

type overviewActivityResult struct {
	active *CollectionRun
	recent []CollectionRun
	err    error
}

// Overview returns the resource-library counters used by the admin dashboard.
// Snapshot statistics and frequently changing collection activity are loaded in
// parallel so callers that still use this compatibility method do not pay the
// sum of both query latencies.
func (s *Store) Overview(ctx context.Context) (OverviewStats, error) {
	if s == nil || s.pool == nil {
		return OverviewStats{}, fmt.Errorf("storage is disabled")
	}

	snapshotCh := make(chan overviewSnapshotResult, 1)
	activityCh := make(chan overviewActivityResult, 1)
	go func() {
		stats, err := s.OverviewSnapshot(ctx)
		snapshotCh <- overviewSnapshotResult{stats: stats, err: err}
	}()
	go func() {
		active, recent, err := s.OverviewActivity(ctx)
		activityCh <- overviewActivityResult{active: active, recent: recent, err: err}
	}()

	snapshot := <-snapshotCh
	activity := <-activityCh
	if snapshot.err != nil {
		return OverviewStats{}, snapshot.err
	}
	if activity.err != nil {
		return OverviewStats{}, activity.err
	}
	snapshot.stats.ActiveRun = activity.active
	snapshot.stats.RecentRuns = activity.recent
	return snapshot.stats, nil
}

// OverviewSnapshot loads the comparatively expensive statistics that are safe
// to cache for a short period. ActiveRun and RecentRuns are intentionally left
// empty; use OverviewActivity for those frequently changing fields.
func (s *Store) OverviewSnapshot(ctx context.Context) (OverviewStats, error) {
	if s == nil || s.pool == nil {
		return OverviewStats{}, fmt.Errorf("storage is disabled")
	}
	now := s.now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	tomorrow := today.AddDate(0, 0, 1)
	sevenDaysAgo := today.AddDate(0, 0, -6)

	type resourceResult struct {
		resourceCount    int64
		todayNew         int64
		lastSevenDaysNew int64
		statusCounts     StatusCounts
		err              error
	}
	type keywordResult struct {
		keywordCount        int64
		enabledKeywordCount int64
		err                 error
	}
	type sourcesResult struct {
		topSources       []SourceContribution
		sourceTypeTotals map[string]SourceContributionTotal
		topSourcesByType map[string][]SourceContribution
		err              error
	}

	resourcesCh := make(chan resourceResult, 1)
	keywordsCh := make(chan keywordResult, 1)
	sourcesCh := make(chan sourcesResult, 1)

	go func() {
		var result resourceResult
		var pending, valid, invalid, unknown, unsupported int64
		result.err = s.pool.QueryRow(ctx, `
			SELECT
				count(*),
				count(*) FILTER (WHERE first_seen_at >= $1 AND first_seen_at < $2),
				count(*) FILTER (WHERE first_seen_at >= $3 AND first_seen_at < $2),
				count(*) FILTER (WHERE check_status = 'pending'),
				count(*) FILTER (WHERE check_status = 'valid'),
				count(*) FILTER (WHERE check_status = 'invalid'),
				count(*) FILTER (WHERE check_status = 'unknown'),
				count(*) FILTER (WHERE check_status = 'unsupported')
			FROM resources`, today, tomorrow, sevenDaysAgo).Scan(
			&result.resourceCount,
			&result.todayNew,
			&result.lastSevenDaysNew,
			&pending,
			&valid,
			&invalid,
			&unknown,
			&unsupported,
		)
		if result.err != nil {
			result.err = fmt.Errorf("load resource overview statistics: %w", result.err)
		} else {
			result.statusCounts = StatusCounts{
				CheckPending: pending, CheckValid: valid, CheckInvalid: invalid,
				CheckUnknown: unknown, CheckUnsupported: unsupported,
			}
		}
		resourcesCh <- result
	}()

	go func() {
		var result keywordResult
		result.err = s.pool.QueryRow(ctx, `
			SELECT count(*), count(*) FILTER (WHERE enabled)
			FROM keywords`).Scan(&result.keywordCount, &result.enabledKeywordCount)
		if result.err != nil {
			result.err = fmt.Errorf("load keyword overview statistics: %w", result.err)
		}
		keywordsCh <- result
	}()

	go func() {
		result := sourcesResult{
			topSources: make([]SourceContribution, 0, 10),
			sourceTypeTotals: map[string]SourceContributionTotal{
				"plugin": {SourceType: "plugin"},
				"tg":     {SourceType: "tg"},
			},
			topSourcesByType: map[string][]SourceContribution{
				"plugin": make([]SourceContribution, 0, 10),
				"tg":     make([]SourceContribution, 0, 10),
			},
		}
		rows, err := s.pool.Query(ctx, `
			SELECT source_type, source_key, count(DISTINCT resource_id), sum(discovery_count)
			FROM resource_sources
			GROUP BY source_type, source_key
			ORDER BY count(DISTINCT resource_id) DESC, sum(discovery_count) DESC,
				source_type, source_key
			LIMIT 10`)
		if err != nil {
			result.err = fmt.Errorf("load source contributions: %w", err)
			sourcesCh <- result
			return
		}
		defer rows.Close()
		for rows.Next() {
			var contribution SourceContribution
			if err := rows.Scan(&contribution.SourceType, &contribution.SourceKey,
				&contribution.ResourceCount, &contribution.DiscoveryCount); err != nil {
				result.err = fmt.Errorf("scan source contribution: %w", err)
				sourcesCh <- result
				return
			}
			result.topSources = append(result.topSources, contribution)
		}
		if err := rows.Err(); err != nil {
			result.err = fmt.Errorf("iterate source contributions: %w", err)
			sourcesCh <- result
			return
		}

		totalRows, err := s.pool.Query(ctx, `
			SELECT source_type, count(DISTINCT resource_id)::bigint,
				COALESCE(sum(discovery_count), 0)::bigint
			FROM resource_sources
			WHERE source_type IN ('plugin', 'tg')
			GROUP BY source_type`)
		if err != nil {
			result.err = fmt.Errorf("load source type totals: %w", err)
			sourcesCh <- result
			return
		}
		for totalRows.Next() {
			var total SourceContributionTotal
			if err := totalRows.Scan(&total.SourceType, &total.ResourceCount, &total.DiscoveryCount); err != nil {
				totalRows.Close()
				result.err = fmt.Errorf("scan source type total: %w", err)
				sourcesCh <- result
				return
			}
			result.sourceTypeTotals[total.SourceType] = total
		}
		if err := totalRows.Err(); err != nil {
			totalRows.Close()
			result.err = fmt.Errorf("iterate source type totals: %w", err)
			sourcesCh <- result
			return
		}
		totalRows.Close()

		typeRows, err := s.pool.Query(ctx, `
			WITH contributions AS (
				SELECT source_type, source_key,
					count(DISTINCT resource_id)::bigint AS resource_count,
					COALESCE(sum(discovery_count), 0)::bigint AS discovery_count
				FROM resource_sources
				WHERE source_type IN ('plugin', 'tg')
				GROUP BY source_type, source_key
			), ranked AS (
				SELECT *, row_number() OVER (
					PARTITION BY source_type
					ORDER BY resource_count DESC, discovery_count DESC, lower(source_key), source_key
				) AS source_rank
				FROM contributions
			)
			SELECT source_type, source_key, resource_count, discovery_count
			FROM ranked
			WHERE source_rank <= 10
			ORDER BY source_type, source_rank`)
		if err != nil {
			result.err = fmt.Errorf("load top sources by type: %w", err)
			sourcesCh <- result
			return
		}
		for typeRows.Next() {
			var contribution SourceContribution
			if err := typeRows.Scan(&contribution.SourceType, &contribution.SourceKey,
				&contribution.ResourceCount, &contribution.DiscoveryCount); err != nil {
				typeRows.Close()
				result.err = fmt.Errorf("scan top source by type: %w", err)
				sourcesCh <- result
				return
			}
			result.topSourcesByType[contribution.SourceType] = append(
				result.topSourcesByType[contribution.SourceType], contribution,
			)
		}
		if err := typeRows.Err(); err != nil {
			typeRows.Close()
			result.err = fmt.Errorf("iterate top sources by type: %w", err)
			sourcesCh <- result
			return
		}
		typeRows.Close()
		sourcesCh <- result
	}()

	resources := <-resourcesCh
	keywords := <-keywordsCh
	sources := <-sourcesCh
	if resources.err != nil {
		return OverviewStats{}, resources.err
	}
	if keywords.err != nil {
		return OverviewStats{}, keywords.err
	}
	if sources.err != nil {
		return OverviewStats{}, sources.err
	}
	return OverviewStats{
		ResourceCount:       resources.resourceCount,
		TodayNew:            resources.todayNew,
		LastSevenDaysNew:    resources.lastSevenDaysNew,
		KeywordCount:        keywords.keywordCount,
		EnabledKeywordCount: keywords.enabledKeywordCount,
		StatusCounts:        resources.statusCounts,
		TopSources:          sources.topSources,
		SourceTypeTotals:    sources.sourceTypeTotals,
		TopSourcesByType:    sources.topSourcesByType,
	}, nil
}

// OverviewActivity loads the dashboard fields that change while a collection
// is running. It deliberately bypasses the snapshot cache used by the service.
func (s *Store) OverviewActivity(ctx context.Context) (*CollectionRun, []CollectionRun, error) {
	if s == nil || s.pool == nil {
		return nil, nil, fmt.Errorf("storage is disabled")
	}

	type activeResult struct {
		active *CollectionRun
		err    error
	}
	type recentResult struct {
		recent []CollectionRun
		err    error
	}
	activeCh := make(chan activeResult, 1)
	recentCh := make(chan recentResult, 1)

	go func() {
		active, err := scanRun(s.pool.QueryRow(ctx, runSelect+`
			WHERE cr.status IN ('pending','running')`+runGroup+`
			ORDER BY cr.created_at ASC, cr.id ASC LIMIT 1`))
		if errors.Is(err, pgx.ErrNoRows) {
			activeCh <- activeResult{}
			return
		}
		if err != nil {
			activeCh <- activeResult{err: fmt.Errorf("load active collection run: %w", err)}
			return
		}
		activeCh <- activeResult{active: &active}
	}()

	go func() {
		rows, err := s.pool.Query(ctx, runSelect+runGroup+`
			ORDER BY cr.created_at DESC, cr.id DESC LIMIT 5`)
		if err != nil {
			recentCh <- recentResult{err: fmt.Errorf("load recent collection runs: %w", err)}
			return
		}
		defer rows.Close()
		recent := make([]CollectionRun, 0, 5)
		for rows.Next() {
			run, scanErr := scanRun(rows)
			if scanErr != nil {
				recentCh <- recentResult{err: fmt.Errorf("scan recent collection run: %w", scanErr)}
				return
			}
			recent = append(recent, run)
		}
		if err := rows.Err(); err != nil {
			recentCh <- recentResult{err: fmt.Errorf("iterate recent collection runs: %w", err)}
			return
		}
		recentCh <- recentResult{recent: recent}
	}()

	active := <-activeCh
	recent := <-recentCh
	if active.err != nil {
		return nil, nil, active.err
	}
	if recent.err != nil {
		return nil, nil, recent.err
	}
	return active.active, recent.recent, nil
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
	starts := make([]time.Time, days)
	ends := make([]time.Time, days)
	for i := 0; i < days; i++ {
		starts[i] = start.AddDate(0, 0, i)
		ends[i] = start.AddDate(0, 0, i+1)
	}

	rows, err := s.pool.Query(ctx, `
		WITH days(day, next_day) AS MATERIALIZED (
			SELECT * FROM unnest($1::timestamptz[], $2::timestamptz[])
		),
		resource_daily AS (
			SELECT d.day,
				count(r.id) AS new_count,
				count(r.id) FILTER (WHERE r.check_status = 'valid') AS valid_new_count
			FROM days d
			LEFT JOIN resources r
				ON r.first_seen_at >= d.day AND r.first_seen_at < d.next_day
			GROUP BY d.day
		),
		discovery_daily AS (
			SELECT d.day, COALESCE(sum(i.found_count), 0)::bigint AS discoveries
			FROM days d
			LEFT JOIN collection_run_items i
				ON i.completed_at >= d.day AND i.completed_at < d.next_day
			GROUP BY d.day
		),
		valid_before AS (
			SELECT count(*)::bigint AS valid_count
			FROM resources
			WHERE check_status = 'valid' AND first_seen_at < $3
		)
		SELECT r.day,
			r.new_count,
			d.discoveries,
			(v.valid_count + sum(r.valid_new_count) OVER (
				ORDER BY r.day ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW
			))::bigint AS valid_count
		FROM resource_daily r
		JOIN discovery_daily d USING (day)
		CROSS JOIN valid_before v
		ORDER BY r.day`, starts, ends, start)
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
