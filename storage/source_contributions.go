package storage

import (
	"context"
	"fmt"
	"strings"
)

var sourceContributionSortFields = map[string]sortField{
	"source_key": {
		Expression: "lower(source_key)",
		TieBreaker: "ROW(lower(source_type), source_type, source_key)",
	},
	"resource_count": {
		Expression: "resource_count",
		TieBreaker: "ROW(lower(source_type || ':' || source_key), source_type, source_key)",
	},
	"discovery_count": {
		Expression: "discovery_count",
		TieBreaker: "ROW(lower(source_type || ':' || source_key), source_type, source_key)",
	},
}

var subSourceContributionSortFields = map[string]sortField{
	"sub_source": {
		Expression: "lower(sub_source)",
		TieBreaker: "sub_source",
	},
	"resource_count": {
		Expression: "resource_count",
		TieBreaker: "ROW(lower(sub_source), sub_source)",
	},
	"discovery_count": {
		Expression: "discovery_count",
		TieBreaker: "ROW(lower(sub_source), sub_source)",
	},
}

// ListSourceContributions returns source aggregates after applying the sort to
// the complete result set. An empty or "all" source type combines plugin and
// TG contributions; legacy unknown/external origins remain available in the
// overview's backward-compatible top_sources field only.
func (s *Store) ListSourceContributions(ctx context.Context, filter SourceContributionFilter) (SourceContributionPage, error) {
	if s == nil || s.pool == nil {
		return SourceContributionPage{}, fmt.Errorf("storage is disabled")
	}
	sourceType, err := normalizeContributionSourceType(filter.SourceType, true)
	if err != nil {
		return SourceContributionPage{}, err
	}
	page, pageSize := normalizePage(filter.Page, filter.PageSize, 50, 200)
	sortClause, err := buildSortClause(
		filter.SortBy,
		filter.SortDir,
		"resource_count DESC, discovery_count DESC, lower(source_type || ':' || source_key) ASC, source_type ASC, source_key ASC",
		sourceContributionSortFields,
	)
	if err != nil {
		return SourceContributionPage{}, err
	}

	where := "source_type IN ('plugin', 'tg')"
	args := make([]any, 0, 3)
	if sourceType != "" {
		where = "source_type = $1"
		args = append(args, sourceType)
	}
	var total int64
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM (
			SELECT source_type, source_key
			FROM resource_sources
			WHERE `+where+`
			GROUP BY source_type, source_key
		) contributions`, args...).Scan(&total); err != nil {
		return SourceContributionPage{}, fmt.Errorf("count source contributions: %w", err)
	}

	queryArgs := append(append([]any(nil), args...), pageSize, (page-1)*pageSize)
	limitParam := len(args) + 1
	offsetParam := len(args) + 2
	rows, err := s.pool.Query(ctx, `
		WITH contributions AS (
			SELECT source_type, source_key,
				count(DISTINCT resource_id)::bigint AS resource_count,
				COALESCE(sum(discovery_count), 0)::bigint AS discovery_count
			FROM resource_sources
			WHERE `+where+`
			GROUP BY source_type, source_key
		)
		SELECT source_type, source_key, resource_count, discovery_count
		FROM contributions
		ORDER BY `+sortClause+`
		LIMIT $`+fmt.Sprint(limitParam)+` OFFSET $`+fmt.Sprint(offsetParam), queryArgs...)
	if err != nil {
		return SourceContributionPage{}, fmt.Errorf("list source contributions: %w", err)
	}
	defer rows.Close()

	items := make([]SourceContribution, 0, pageSize)
	for rows.Next() {
		var item SourceContribution
		if err := rows.Scan(&item.SourceType, &item.SourceKey, &item.ResourceCount, &item.DiscoveryCount); err != nil {
			return SourceContributionPage{}, fmt.Errorf("scan source contribution: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return SourceContributionPage{}, fmt.Errorf("iterate source contributions: %w", err)
	}
	return SourceContributionPage{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

// GetSourceContribution returns one source summary and the paginated internal
// source breakdown stored in resource_sources.source_metadata.sub_source.
func (s *Store) GetSourceContribution(ctx context.Context, sourceType, sourceKey string, filter SourceContributionDetailFilter) (SourceContributionDetail, error) {
	if s == nil || s.pool == nil {
		return SourceContributionDetail{}, fmt.Errorf("storage is disabled")
	}
	sourceType, err := normalizeContributionSourceType(sourceType, false)
	if err != nil {
		return SourceContributionDetail{}, err
	}
	sourceKey = strings.TrimSpace(sourceKey)
	if sourceKey == "" {
		return SourceContributionDetail{}, fmt.Errorf("%w: source_key is required", ErrInvalid)
	}
	page, pageSize := normalizePage(filter.Page, filter.PageSize, 20, 100)
	sortClause, err := buildSortClause(
		filter.SortBy,
		filter.SortDir,
		"resource_count DESC, discovery_count DESC, lower(sub_source) ASC, sub_source ASC",
		subSourceContributionSortFields,
	)
	if err != nil {
		return SourceContributionDetail{}, err
	}

	detail := SourceContributionDetail{SourceType: sourceType, SourceKey: sourceKey}
	err = s.pool.QueryRow(ctx, `
		SELECT
			count(DISTINCT resource_id) FILTER (WHERE source_key = $2)::bigint,
			COALESCE(sum(discovery_count) FILTER (WHERE source_key = $2), 0)::bigint,
			count(DISTINCT resource_id)::bigint,
			COALESCE(sum(discovery_count), 0)::bigint
		FROM resource_sources
		WHERE source_type = $1`, sourceType, sourceKey).Scan(
		&detail.ResourceCount,
		&detail.DiscoveryCount,
		&detail.TypeResourceCount,
		&detail.TypeDiscoveryCount,
	)
	if err != nil {
		return SourceContributionDetail{}, fmt.Errorf("load source contribution: %w", err)
	}
	if detail.ResourceCount == 0 {
		return SourceContributionDetail{}, fmt.Errorf("%w: source contribution", ErrNotFound)
	}
	detail.ResourceShare = ratio(detail.ResourceCount, detail.TypeResourceCount)
	detail.DiscoveryShare = ratio(detail.DiscoveryCount, detail.TypeDiscoveryCount)

	var subSourceTotal int64
	err = s.pool.QueryRow(ctx, `
		WITH pairs AS (
			SELECT resource_id,
				NULLIF(btrim(source_metadata->>'sub_source'), '') AS sub_source
			FROM resource_sources
			WHERE source_type = $1 AND source_key = $2
				AND NULLIF(btrim(source_metadata->>'sub_source'), '') IS NOT NULL
			GROUP BY resource_id, NULLIF(btrim(source_metadata->>'sub_source'), '')
		)
		SELECT count(DISTINCT resource_id)::bigint, count(*)::bigint,
			count(DISTINCT sub_source)::bigint
		FROM pairs`, sourceType, sourceKey).Scan(
		&detail.IdentifiedResourceCount,
		&detail.SubSourcePairCount,
		&subSourceTotal,
	)
	if err != nil {
		return SourceContributionDetail{}, fmt.Errorf("load sub-source contribution totals: %w", err)
	}
	detail.SubSourceCoverage = ratio(detail.IdentifiedResourceCount, detail.ResourceCount)

	rows, err := s.pool.Query(ctx, `
		WITH pairs AS (
			SELECT resource_id,
				NULLIF(btrim(source_metadata->>'sub_source'), '') AS sub_source,
				COALESCE(sum(discovery_count), 0)::bigint AS discovery_count
			FROM resource_sources
			WHERE source_type = $1 AND source_key = $2
				AND NULLIF(btrim(source_metadata->>'sub_source'), '') IS NOT NULL
			GROUP BY resource_id, NULLIF(btrim(source_metadata->>'sub_source'), '')
		), contributions AS (
			SELECT sub_source, count(*)::bigint AS resource_count,
				COALESCE(sum(discovery_count), 0)::bigint AS discovery_count
			FROM pairs
			GROUP BY sub_source
		)
		SELECT sub_source, resource_count, discovery_count
		FROM contributions
		ORDER BY `+sortClause+`
		LIMIT $3 OFFSET $4`, sourceType, sourceKey, pageSize, (page-1)*pageSize)
	if err != nil {
		return SourceContributionDetail{}, fmt.Errorf("list sub-source contributions: %w", err)
	}
	defer rows.Close()

	items := make([]SubSourceContribution, 0, pageSize)
	for rows.Next() {
		var item SubSourceContribution
		if err := rows.Scan(&item.SubSource, &item.ResourceCount, &item.DiscoveryCount); err != nil {
			return SourceContributionDetail{}, fmt.Errorf("scan sub-source contribution: %w", err)
		}
		item.PairShare = ratio(item.ResourceCount, detail.SubSourcePairCount)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return SourceContributionDetail{}, fmt.Errorf("iterate sub-source contributions: %w", err)
	}
	detail.SubSources = SubSourceContributionPage{
		Items: items, Total: subSourceTotal, Page: page, PageSize: pageSize,
	}
	return detail, nil
}

func normalizeContributionSourceType(value string, allowAll bool) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if allowAll && (value == "" || value == "all") {
		return "", nil
	}
	if value == "plugin" || value == "tg" {
		return value, nil
	}
	return "", fmt.Errorf("%w: unsupported source_type %q", ErrInvalid, value)
}

func ratio(numerator, denominator int64) float64 {
	if numerator <= 0 || denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}
