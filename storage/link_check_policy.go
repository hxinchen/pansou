package storage

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	MinLinkCheckIntervalSeconds     int64 = 3600
	MaxLinkCheckIntervalSeconds     int64 = 365 * 24 * 3600
	DefaultLinkCheckIntervalSeconds int64 = 7 * 24 * 3600
)

var canonicalLinkCheckPolicyStatuses = []string{
	CheckValid,
	CheckUnknown,
	CheckInvalid,
	CheckExpired,
	CheckCancelled,
	CheckViolation,
	CheckLocked,
}

// LinkCheckPolicy is the singleton policy used to select resources for
// periodic link checks. Pending resources are always checked independently of
// this policy.
type LinkCheckPolicy struct {
	Enabled         bool      `json:"enabled"`
	Statuses        []string  `json:"statuses"`
	IntervalSeconds int64     `json:"interval_seconds"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (p LinkCheckPolicy) Revision() string {
	return fmt.Sprintf("%t|%d|%s", p.Enabled, p.IntervalSeconds, strings.Join(p.Statuses, ","))
}

type UpdateLinkCheckPolicyInput struct {
	Enabled         bool     `json:"enabled"`
	Statuses        []string `json:"statuses"`
	IntervalSeconds int64    `json:"interval_seconds"`
}

func scanLinkCheckPolicy(row rowScanner) (LinkCheckPolicy, error) {
	var policy LinkCheckPolicy
	if err := row.Scan(&policy.Enabled, &policy.Statuses, &policy.IntervalSeconds, &policy.UpdatedAt); err != nil {
		return LinkCheckPolicy{}, err
	}
	normalized, err := normalizeLinkCheckPolicy(policy.Enabled, policy.Statuses, policy.IntervalSeconds)
	if err != nil {
		return LinkCheckPolicy{}, err
	}
	normalized.UpdatedAt = policy.UpdatedAt
	return normalized, nil
}

func normalizeLinkCheckPolicy(enabled bool, statuses []string, intervalSeconds int64) (LinkCheckPolicy, error) {
	if intervalSeconds < MinLinkCheckIntervalSeconds || intervalSeconds > MaxLinkCheckIntervalSeconds || intervalSeconds%3600 != 0 {
		return LinkCheckPolicy{}, fmt.Errorf("%w: link check interval must be a whole number of hours between 1 and 8760", ErrInvalid)
	}

	selected := make(map[string]struct{}, len(statuses))
	allowed := make(map[string]struct{}, len(canonicalLinkCheckPolicyStatuses))
	for _, status := range canonicalLinkCheckPolicyStatuses {
		allowed[status] = struct{}{}
	}
	for _, status := range statuses {
		status = strings.ToLower(strings.TrimSpace(status))
		if _, ok := allowed[status]; !ok {
			return LinkCheckPolicy{}, fmt.Errorf("%w: unsupported link check policy status %q", ErrInvalid, status)
		}
		selected[status] = struct{}{}
	}

	normalizedStatuses := make([]string, 0, len(selected))
	for _, status := range canonicalLinkCheckPolicyStatuses {
		if _, ok := selected[status]; ok {
			normalizedStatuses = append(normalizedStatuses, status)
		}
	}
	if enabled && len(normalizedStatuses) == 0 {
		return LinkCheckPolicy{}, fmt.Errorf("%w: enabled link check policy requires at least one status", ErrInvalid)
	}

	return LinkCheckPolicy{
		Enabled:         enabled,
		Statuses:        normalizedStatuses,
		IntervalSeconds: intervalSeconds,
	}, nil
}

func (s *Store) GetLinkCheckPolicy(ctx context.Context) (LinkCheckPolicy, error) {
	if s == nil || s.pool == nil {
		return LinkCheckPolicy{}, fmt.Errorf("storage is disabled")
	}
	policy, err := scanLinkCheckPolicy(s.pool.QueryRow(ctx, `
		SELECT enabled, statuses, interval_seconds, updated_at
		FROM link_check_policy WHERE singleton=TRUE`))
	if err != nil {
		return LinkCheckPolicy{}, fmt.Errorf("get link check policy: %w", err)
	}
	return policy, nil
}

func (s *Store) UpdateLinkCheckPolicy(ctx context.Context, input UpdateLinkCheckPolicyInput) (LinkCheckPolicy, error) {
	if s == nil || s.pool == nil {
		return LinkCheckPolicy{}, fmt.Errorf("storage is disabled")
	}
	policy, err := normalizeLinkCheckPolicy(input.Enabled, input.Statuses, input.IntervalSeconds)
	if err != nil {
		return LinkCheckPolicy{}, err
	}
	updatedAt := s.now()
	policy, err = scanLinkCheckPolicy(s.pool.QueryRow(ctx, `
		UPDATE link_check_policy
		SET enabled=$1, statuses=$2, interval_seconds=$3, updated_at=$4
		WHERE singleton=TRUE
		RETURNING enabled, statuses, interval_seconds, updated_at`,
		policy.Enabled, policy.Statuses, policy.IntervalSeconds, updatedAt))
	if err != nil {
		return LinkCheckPolicy{}, fmt.Errorf("update link check policy: %w", err)
	}
	return policy, nil
}
