package storage

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

type SearchSourceMetricDelta struct {
	Date                                           time.Time
	Source                                         string
	Runs, Failures, Timeouts, RateLimited, Skipped uint64
	ResultCount, UniqueCount, TotalDurationMS      uint64
	P50MS, P95MS, MaxMS                            int64
}

type SourceTierSuggestion struct {
	Source        string  `json:"source"`
	SourceType    string  `json:"source_type"`
	CurrentTier   string  `json:"current_tier,omitempty"`
	SuggestedTier string  `json:"suggested_tier"`
	Score         float64 `json:"score"`
	Runs          uint64  `json:"runs"`
	SuccessRate   float64 `json:"success_rate"`
	TimeoutRate   float64 `json:"timeout_rate"`
	RateLimitRate float64 `json:"rate_limit_rate"`
	UniquePerRun  float64 `json:"unique_per_run"`
	P95MS         int64   `json:"p95_ms"`
	ObservedDays  int     `json:"observed_days"`
	Eligible      bool    `json:"eligible"`
	Eligibility   string  `json:"eligibility"`
}

func (s *Store) RecordSearchSourceMetrics(ctx context.Context, metrics []SearchSourceMetricDelta) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("storage is disabled")
	}
	if len(metrics) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin scheduler metrics: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, metric := range metrics {
		date := metric.Date.UTC().Truncate(24 * time.Hour)
		_, err = tx.Exec(ctx, `INSERT INTO search_source_metrics_daily (
			metric_date, source, runs, failures, timeouts, rate_limited, skipped,
			result_count, unique_count, total_duration_ms, p50_ms, p95_ms, max_ms
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (metric_date, source) DO UPDATE SET
			runs=search_source_metrics_daily.runs+EXCLUDED.runs,
			failures=search_source_metrics_daily.failures+EXCLUDED.failures,
			timeouts=search_source_metrics_daily.timeouts+EXCLUDED.timeouts,
			rate_limited=search_source_metrics_daily.rate_limited+EXCLUDED.rate_limited,
			skipped=search_source_metrics_daily.skipped+EXCLUDED.skipped,
			result_count=search_source_metrics_daily.result_count+EXCLUDED.result_count,
			unique_count=search_source_metrics_daily.unique_count+EXCLUDED.unique_count,
			total_duration_ms=search_source_metrics_daily.total_duration_ms+EXCLUDED.total_duration_ms,
			p50_ms=EXCLUDED.p50_ms, p95_ms=GREATEST(search_source_metrics_daily.p95_ms,EXCLUDED.p95_ms),
			max_ms=GREATEST(search_source_metrics_daily.max_ms,EXCLUDED.max_ms), updated_at=now()`,
			date, metric.Source, metric.Runs, metric.Failures, metric.Timeouts, metric.RateLimited, metric.Skipped,
			metric.ResultCount, metric.UniqueCount, metric.TotalDurationMS, metric.P50MS, metric.P95MS, metric.MaxMS)
		if err != nil {
			return fmt.Errorf("upsert scheduler metric %s: %w", metric.Source, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit scheduler metrics: %w", err)
	}
	return nil
}

func (s *Store) SearchSourceTierSuggestions(ctx context.Context, days int) ([]SourceTierSuggestion, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("storage is disabled")
	}
	if days <= 0 {
		days = 14
	}
	rows, err := s.pool.Query(ctx, `SELECT source, sum(runs), sum(failures), sum(timeouts), sum(rate_limited),
		sum(unique_count), max(p95_ms), min(metric_date) FROM search_source_metrics_daily
		WHERE metric_date >= current_date - $1::int GROUP BY source`, days)
	if err != nil {
		return nil, fmt.Errorf("query source tier suggestions: %w", err)
	}
	defer rows.Close()
	items := make([]SourceTierSuggestion, 0)
	for rows.Next() {
		var item SourceTierSuggestion
		var failures, timeouts, rateLimited, unique uint64
		var since time.Time
		if err := rows.Scan(&item.Source, &item.Runs, &failures, &timeouts, &rateLimited, &unique, &item.P95MS, &since); err != nil {
			return nil, err
		}
		item.ObservedDays = int(time.Since(since).Hours()/24) + 1
		if item.ObservedDays < 1 {
			item.ObservedDays = 1
		}
		item.Eligible, item.Eligibility = sourceTierEligibility(item.Runs, item.ObservedDays)
		if strings.HasPrefix(item.Source, "tg:") {
			item.SourceType = "tg"
		} else {
			item.SourceType = "plugin"
		}
		denominator := float64(max(uint64(1), item.Runs))
		item.SuccessRate = float64(item.Runs-min(item.Runs, failures)) / denominator
		item.TimeoutRate = float64(timeouts) / denominator
		item.RateLimitRate = float64(rateLimited) / denominator
		item.UniquePerRun = float64(unique) / denominator
		item.Score = item.SuccessRate*100 + min(item.UniquePerRun, 20)*3 - item.TimeoutRate*80 - item.RateLimitRate*100 - min(float64(item.P95MS)/1000, 30)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	assignSuggestedTiers(items, "tg", 30, 0)
	assignSuggestedTiers(items, "plugin", 15, 30)
	sort.Slice(items, func(i, j int) bool {
		if items[i].SourceType == items[j].SourceType {
			return items[i].Score > items[j].Score
		}
		return items[i].SourceType < items[j].SourceType
	})
	return items, nil
}

func assignSuggestedTiers(items []SourceTierSuggestion, sourceType string, primary, secondary int) {
	indexes := make([]int, 0)
	for index := range items {
		if items[index].SourceType == sourceType && items[index].Eligible {
			indexes = append(indexes, index)
		}
	}
	sort.Slice(indexes, func(i, j int) bool { return items[indexes[i]].Score > items[indexes[j]].Score })
	for rank, index := range indexes {
		if sourceType == "tg" {
			if rank < primary {
				items[index].SuggestedTier = "realtime"
			} else {
				items[index].SuggestedTier = "collection"
			}
		} else if rank < primary {
			items[index].SuggestedTier = "primary"
		} else if rank < primary+secondary {
			items[index].SuggestedTier = "secondary"
		} else {
			items[index].SuggestedTier = "deep"
		}
	}
}

func sourceTierEligibility(runs uint64, observedDays int) (bool, string) {
	if runs >= 300 && observedDays >= 7 {
		return true, "样本不少于 300 次且观察不少于 7 天"
	}
	if runs >= 100 && observedDays >= 14 {
		return true, "样本不少于 100 次且观察不少于 14 天"
	}
	return false, "需达到 100 次且 14 天，或 300 次且 7 天"
}
