package storage

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const apiRequestLogSelectColumns = `
	l.id, l.request_id, l.user_id, u.username, l.auth_type, l.method, l.endpoint,
	l.keyword, l.status_code, l.duration_ms, l.result_count, l.cache_status,
	l.error_code, l.source_ip, l.user_agent, l.created_at`

func scanAPIRequestLog(row rowScanner) (APIRequestLog, error) {
	var log APIRequestLog
	err := row.Scan(
		&log.ID, &log.RequestID, &log.UserID, &log.Username, &log.AuthType,
		&log.Method, &log.Endpoint, &log.Keyword, &log.StatusCode, &log.DurationMS,
		&log.ResultCount, &log.CacheStatus, &log.ErrorCode, &log.SourceIP,
		&log.UserAgent, &log.CreatedAt,
	)
	return log, err
}

func (s *Store) InsertAPIRequestLogs(ctx context.Context, logs []APIRequestLogInput) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("storage is disabled")
	}
	if len(logs) == 0 {
		return 0, nil
	}
	now := s.now()
	rows := make([][]any, 0, len(logs))
	for _, input := range logs {
		if input.UserID <= 0 || !validAuthType(input.AuthType) {
			return 0, fmt.Errorf("%w: invalid request log identity", ErrInvalid)
		}
		if strings.TrimSpace(input.Method) == "" || strings.TrimSpace(input.Endpoint) == "" || input.StatusCode <= 0 {
			return 0, fmt.Errorf("%w: incomplete request log", ErrInvalid)
		}
		if input.DurationMS < 0 || input.ResultCount < 0 {
			return 0, fmt.Errorf("%w: negative request metrics", ErrInvalid)
		}
		if input.CreatedAt.IsZero() {
			input.CreatedAt = now
		}
		rows = append(rows, []any{
			strings.TrimSpace(input.RequestID), input.UserID, input.AuthType,
			strings.ToUpper(strings.TrimSpace(input.Method)), strings.TrimSpace(input.Endpoint),
			strings.TrimSpace(input.Keyword), input.StatusCode, input.DurationMS,
			input.ResultCount, strings.TrimSpace(input.CacheStatus), strings.TrimSpace(input.ErrorCode),
			strings.TrimSpace(input.SourceIP), strings.TrimSpace(input.UserAgent), input.CreatedAt,
		})
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin request log batch: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	inserted, err := tx.CopyFrom(ctx, pgx.Identifier{"api_request_logs"}, []string{
		"request_id", "user_id", "auth_type", "method", "endpoint", "keyword",
		"status_code", "duration_ms", "result_count", "cache_status", "error_code",
		"source_ip", "user_agent", "created_at",
	}, pgx.CopyFromRows(rows))
	if err != nil {
		return 0, fmt.Errorf("insert request log batch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit request log batch: %w", err)
	}
	return inserted, nil
}

func (s *Store) ListAPIRequestLogs(ctx context.Context, filter APIRequestLogFilter) (APIRequestLogPage, error) {
	return s.listAPIRequestLogs(ctx, filter, nil)
}

func (s *Store) ListAdminAPIRequestLogs(ctx context.Context, filter APIRequestLogFilter) (APIRequestLogPage, error) {
	return s.listAPIRequestLogs(ctx, filter, nil)
}

func (s *Store) ListUserAPIRequestLogs(ctx context.Context, userID int64, filter APIRequestLogFilter) (APIRequestLogPage, error) {
	return s.listAPIRequestLogs(ctx, filter, &userID)
}

func (s *Store) listAPIRequestLogs(ctx context.Context, filter APIRequestLogFilter, forcedUserID *int64) (APIRequestLogPage, error) {
	if s == nil || s.pool == nil {
		return APIRequestLogPage{}, fmt.Errorf("storage is disabled")
	}
	if forcedUserID != nil {
		if *forcedUserID <= 0 {
			return APIRequestLogPage{}, fmt.Errorf("%w: invalid user id", ErrInvalid)
		}
		filter.UserID = forcedUserID
	}
	page, pageSize := normalizePage(filter.Page, filter.PageSize, 50, 200)
	where, args, err := buildAPIRequestLogWhere(filter)
	if err != nil {
		return APIRequestLogPage{}, err
	}
	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM api_request_logs l WHERE `+where, args...).Scan(&total); err != nil {
		return APIRequestLogPage{}, fmt.Errorf("count request logs: %w", err)
	}
	sortClause, err := buildSortClause(filter.SortBy, filter.SortDir, "l.created_at DESC, l.id DESC", apiRequestLogSortFields)
	if err != nil {
		return APIRequestLogPage{}, err
	}
	queryArgs := append(append([]any(nil), args...), pageSize, (page-1)*pageSize)
	rows, err := s.pool.Query(ctx, `SELECT `+apiRequestLogSelectColumns+`
		FROM api_request_logs l JOIN users u ON u.id=l.user_id
		WHERE `+where+` ORDER BY `+sortClause+
		fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2), queryArgs...)
	if err != nil {
		return APIRequestLogPage{}, fmt.Errorf("list request logs: %w", err)
	}
	defer rows.Close()
	items := make([]APIRequestLog, 0, pageSize)
	for rows.Next() {
		item, scanErr := scanAPIRequestLog(rows)
		if scanErr != nil {
			return APIRequestLogPage{}, fmt.Errorf("scan request log: %w", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return APIRequestLogPage{}, fmt.Errorf("iterate request logs: %w", err)
	}
	return APIRequestLogPage{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func buildAPIRequestLogWhere(filter APIRequestLogFilter) (string, []any, error) {
	conditions := []string{"TRUE"}
	args := make([]any, 0, 10)
	addArg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if filter.UserID != nil {
		if *filter.UserID <= 0 {
			return "", nil, fmt.Errorf("%w: invalid user id", ErrInvalid)
		}
		conditions = append(conditions, "l.user_id="+addArg(*filter.UserID))
	}
	if authTypes := normalizeStringList(filter.AuthTypes); len(authTypes) > 0 {
		for _, authType := range authTypes {
			if !validAuthType(authType) {
				return "", nil, fmt.Errorf("%w: auth type %q", ErrInvalid, authType)
			}
		}
		conditions = append(conditions, "l.auth_type=ANY("+addArg(authTypes)+"::text[])")
	}
	if methods := normalizeStringList(filter.Methods); len(methods) > 0 {
		for index := range methods {
			methods[index] = strings.ToUpper(methods[index])
		}
		conditions = append(conditions, "l.method=ANY("+addArg(methods)+"::text[])")
	}
	if endpoints := normalizeStringList(filter.Endpoints); len(endpoints) > 0 {
		conditions = append(conditions, "l.endpoint=ANY("+addArg(endpoints)+"::text[])")
	}
	if len(filter.StatusCodes) > 0 {
		statuses := make([]int32, 0, len(filter.StatusCodes))
		for _, status := range filter.StatusCodes {
			statuses = append(statuses, int32(status))
		}
		conditions = append(conditions, "l.status_code=ANY("+addArg(statuses)+"::int[])")
	}
	if query := strings.TrimSpace(filter.Query); query != "" {
		placeholder := addArg("%" + query + "%")
		conditions = append(conditions, "(l.keyword ILIKE "+placeholder+
			" OR l.request_id ILIKE "+placeholder+" OR l.error_code ILIKE "+placeholder+")")
	}
	if filter.From != nil {
		conditions = append(conditions, "l.created_at>="+addArg(*filter.From))
	}
	if filter.To != nil {
		conditions = append(conditions, "l.created_at<"+addArg(*filter.To))
	}
	return strings.Join(conditions, " AND "), args, nil
}

func (s *Store) UserUsageOverview(ctx context.Context, userID int64, filter UsageStatsFilter) (UsageOverviewStats, error) {
	filter.UserID = &userID
	return s.UsageOverview(ctx, filter)
}

func (s *Store) AdminUsageOverview(ctx context.Context, filter UsageStatsFilter) (UsageOverviewStats, error) {
	return s.UsageOverview(ctx, filter)
}

func (s *Store) UsageOverview(ctx context.Context, filter UsageStatsFilter) (UsageOverviewStats, error) {
	if s == nil || s.pool == nil {
		return UsageOverviewStats{}, fmt.Errorf("storage is disabled")
	}
	from, to, _ := normalizeUsageRange(filter, s.now())
	filter.From, filter.To = from, to
	if filter.SlowThresholdMS <= 0 {
		filter.SlowThresholdMS = 1000
	}
	where, args, err := buildUsageWhere(filter)
	if err != nil {
		return UsageOverviewStats{}, err
	}
	args = append(args, filter.SlowThresholdMS)
	slowPlaceholder := fmt.Sprintf("$%d", len(args))
	result := UsageOverviewStats{
		From: from, To: to, StatusCounts: make(map[string]int64), ErrorCounts: make(map[string]int64),
		TopUsers: make([]UserUsageSummary, 0), RecentRequests: make([]APIRequestLog, 0),
	}
	if err := s.pool.QueryRow(ctx, `SELECT
		count(*),
		count(*) FILTER (WHERE l.status_code>=200 AND l.status_code<400),
		count(*) FILTER (WHERE l.status_code=429),
		count(DISTINCT l.user_id),
		COALESCE(avg(l.duration_ms),0)::float8,
		COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY l.duration_ms),0)::float8,
		count(*) FILTER (WHERE l.cache_status='hit'),
		COALESCE(sum(l.result_count),0),
		count(*) FILTER (WHERE l.duration_ms>=`+slowPlaceholder+`)
		FROM api_request_logs l WHERE `+where, args...).Scan(
		&result.TotalRequests, &result.SuccessfulRequests, &result.RateLimitedRequests,
		&result.ActiveUsers, &result.AvgDurationMS, &result.P95DurationMS,
		&result.CacheHits, &result.TotalResults, &result.SlowRequests,
	); err != nil {
		return UsageOverviewStats{}, fmt.Errorf("load usage overview: %w", err)
	}
	result.FailedRequests = result.TotalRequests - result.SuccessfulRequests
	if result.TotalRequests > 0 {
		result.SuccessRate = float64(result.SuccessfulRequests) / float64(result.TotalRequests)
		result.CacheHitRate = float64(result.CacheHits) / float64(result.TotalRequests)
	}

	rows, err := s.pool.Query(ctx, `SELECT l.status_code, count(*)
		FROM api_request_logs l WHERE `+where+` GROUP BY l.status_code ORDER BY l.status_code`, args[:len(args)-1]...)
	if err != nil {
		return UsageOverviewStats{}, fmt.Errorf("load usage status counts: %w", err)
	}
	for rows.Next() {
		var status int
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			return UsageOverviewStats{}, fmt.Errorf("scan usage status count: %w", err)
		}
		result.StatusCounts[strconv.Itoa(status)] = count
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return UsageOverviewStats{}, fmt.Errorf("iterate usage status counts: %w", err)
	}
	rows.Close()

	rows, err = s.pool.Query(ctx, `SELECT l.error_code, count(*)
		FROM api_request_logs l WHERE `+where+` AND l.error_code<>''
		GROUP BY l.error_code ORDER BY count(*) DESC, l.error_code`, args[:len(args)-1]...)
	if err != nil {
		return UsageOverviewStats{}, fmt.Errorf("load usage error counts: %w", err)
	}
	for rows.Next() {
		var code string
		var count int64
		if err := rows.Scan(&code, &count); err != nil {
			rows.Close()
			return UsageOverviewStats{}, fmt.Errorf("scan usage error count: %w", err)
		}
		result.ErrorCounts[code] = count
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return UsageOverviewStats{}, fmt.Errorf("iterate usage error counts: %w", err)
	}
	rows.Close()

	topLimit := filter.TopUserLimit
	if topLimit <= 0 || topLimit > 100 {
		topLimit = 10
	}
	topArgs := append(append([]any(nil), args[:len(args)-1]...), topLimit)
	rows, err = s.pool.Query(ctx, `SELECT l.user_id, u.username, count(*),
		count(*) FILTER (WHERE l.status_code>=200 AND l.status_code<400),
		COALESCE(avg(l.duration_ms),0)::float8
		FROM api_request_logs l JOIN users u ON u.id=l.user_id
		WHERE `+where+` GROUP BY l.user_id, u.username
		ORDER BY count(*) DESC, l.user_id LIMIT $`+strconv.Itoa(len(topArgs)), topArgs...)
	if err != nil {
		return UsageOverviewStats{}, fmt.Errorf("load top usage users: %w", err)
	}
	for rows.Next() {
		var item UserUsageSummary
		var successes int64
		if err := rows.Scan(&item.UserID, &item.Username, &item.RequestCount, &successes, &item.AvgDurationMS); err != nil {
			rows.Close()
			return UsageOverviewStats{}, fmt.Errorf("scan top usage user: %w", err)
		}
		if item.RequestCount > 0 {
			item.SuccessRate = float64(successes) / float64(item.RequestCount)
		}
		result.TopUsers = append(result.TopUsers, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return UsageOverviewStats{}, fmt.Errorf("iterate top usage users: %w", err)
	}
	rows.Close()

	recentLimit := filter.RecentLimit
	if recentLimit <= 0 || recentLimit > 100 {
		recentLimit = 10
	}
	logFilter := APIRequestLogFilter{UserID: filter.UserID, From: &from, To: &to, Page: 1, PageSize: recentLimit}
	recent, err := s.ListAdminAPIRequestLogs(ctx, logFilter)
	if err != nil {
		return UsageOverviewStats{}, fmt.Errorf("load recent usage requests: %w", err)
	}
	result.RecentRequests = recent.Items
	return result, nil
}

func (s *Store) UserUsageTrends(ctx context.Context, userID int64, filter UsageStatsFilter) ([]UsageTrendPoint, error) {
	filter.UserID = &userID
	return s.UsageTrends(ctx, filter)
}

func (s *Store) AdminUsageTrends(ctx context.Context, filter UsageStatsFilter) ([]UsageTrendPoint, error) {
	return s.UsageTrends(ctx, filter)
}

func (s *Store) UsageTrends(ctx context.Context, filter UsageStatsFilter) ([]UsageTrendPoint, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("storage is disabled")
	}
	from, to, bucket := normalizeUsageRange(filter, s.now())
	truncateUnit, step := "hour", "1 hour"
	if bucket == UsageBucketDay {
		truncateUnit, step = "day", "1 day"
	}
	conditions := []string{"l.created_at >= $1", "l.created_at < $2"}
	args := []any{from, to}
	if filter.UserID != nil {
		if *filter.UserID <= 0 {
			return nil, fmt.Errorf("%w: invalid user id", ErrInvalid)
		}
		args = append(args, *filter.UserID)
		conditions = append(conditions, fmt.Sprintf("l.user_id=$%d", len(args)))
	}
	where := strings.Join(conditions, " AND ")
	query := `WITH buckets AS (
		SELECT generate_series(
			date_trunc('` + truncateUnit + `',$1::timestamptz),
			date_trunc('` + truncateUnit + `',$2::timestamptz - interval '1 microsecond'),
			interval '` + step + `'
		) AS bucket
	), aggregates AS (
		SELECT date_trunc('` + truncateUnit + `',l.created_at) AS bucket,
			count(*) AS requests,
			count(*) FILTER (WHERE l.status_code>=200 AND l.status_code<400) AS successes,
			count(*) FILTER (WHERE l.status_code<200 OR l.status_code>=400) AS failures,
			count(*) FILTER (WHERE l.status_code=429) AS rate_limited,
			count(DISTINCT l.user_id) AS active_users,
			COALESCE(avg(l.duration_ms),0)::float8 AS average_ms,
			COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY l.duration_ms),0)::float8 AS p95_ms,
			count(*) FILTER (WHERE l.cache_status='hit') AS cache_hits,
			COALESCE(sum(l.result_count),0) AS results
		FROM api_request_logs l WHERE ` + where + ` GROUP BY 1
	)
	SELECT b.bucket, COALESCE(a.requests,0), COALESCE(a.successes,0),
		COALESCE(a.failures,0), COALESCE(a.rate_limited,0), COALESCE(a.active_users,0),
		COALESCE(a.average_ms,0), COALESCE(a.p95_ms,0), COALESCE(a.cache_hits,0),
		COALESCE(a.results,0)
	FROM buckets b LEFT JOIN aggregates a ON a.bucket=b.bucket ORDER BY b.bucket`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load usage trends: %w", err)
	}
	defer rows.Close()
	result := make([]UsageTrendPoint, 0)
	for rows.Next() {
		var point UsageTrendPoint
		if err := rows.Scan(&point.Bucket, &point.RequestCount, &point.SuccessfulRequests,
			&point.FailedRequests, &point.RateLimitedRequests, &point.ActiveUsers,
			&point.AvgDurationMS, &point.P95DurationMS, &point.CacheHits, &point.ResultCount); err != nil {
			return nil, fmt.Errorf("scan usage trend: %w", err)
		}
		result = append(result, point)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage trends: %w", err)
	}
	return result, nil
}

func buildUsageWhere(filter UsageStatsFilter) (string, []any, error) {
	conditions := []string{"l.created_at >= $1", "l.created_at < $2"}
	args := []any{filter.From, filter.To}
	if filter.UserID != nil {
		if *filter.UserID <= 0 {
			return "", nil, fmt.Errorf("%w: invalid user id", ErrInvalid)
		}
		args = append(args, *filter.UserID)
		conditions = append(conditions, fmt.Sprintf("l.user_id=$%d", len(args)))
	}
	return strings.Join(conditions, " AND "), args, nil
}

func normalizeUsageRange(filter UsageStatsFilter, now time.Time) (time.Time, time.Time, string) {
	to := filter.To
	if to.IsZero() {
		to = now
	}
	from := filter.From
	if from.IsZero() || !from.Before(to) {
		from = to.Add(-24 * time.Hour)
	}
	bucket := filter.Bucket
	if bucket != UsageBucketHour && bucket != UsageBucketDay {
		bucket = UsageBucketHour
		if to.Sub(from) > 48*time.Hour {
			bucket = UsageBucketDay
		}
	}
	return from, to, bucket
}

func APIRequestLogRetentionCutoff(now time.Time, retention time.Duration) time.Time {
	if retention <= 0 {
		retention = 30 * 24 * time.Hour
	}
	return now.Add(-retention)
}

func (s *Store) DeleteAPIRequestLogsBefore(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("storage is disabled")
	}
	if cutoff.IsZero() {
		cutoff = APIRequestLogRetentionCutoff(s.now(), 0)
	}
	if batchSize <= 0 || batchSize > 10000 {
		batchSize = 1000
	}
	command, err := s.pool.Exec(ctx, `WITH doomed AS (
		SELECT id FROM api_request_logs WHERE created_at < $1
		ORDER BY created_at, id FOR UPDATE SKIP LOCKED LIMIT $2
	) DELETE FROM api_request_logs l USING doomed WHERE l.id=doomed.id`, cutoff, batchSize)
	if err != nil {
		return 0, fmt.Errorf("delete expired request logs: %w", err)
	}
	return command.RowsAffected(), nil
}

func (s *Store) CleanupAPIRequestLogs(ctx context.Context, now time.Time, retention time.Duration, batchSize int) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("storage is disabled")
	}
	if now.IsZero() {
		now = s.now()
	}
	if batchSize <= 0 || batchSize > 10000 {
		batchSize = 1000
	}
	cutoff := APIRequestLogRetentionCutoff(now, retention)
	var total int64
	for {
		deleted, err := s.DeleteAPIRequestLogsBefore(ctx, cutoff, batchSize)
		if err != nil {
			return total, err
		}
		total += deleted
		if deleted < int64(batchSize) {
			return total, nil
		}
	}
}

func validAuthType(authType string) bool {
	return authType == AuthTypeWeb || authType == AuthTypeAPIKey
}
