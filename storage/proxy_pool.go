package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	ProxyStatusPending  = "pending"
	ProxyStatusHealthy  = "healthy"
	ProxyStatusCooling  = "cooling"
	ProxyStatusDisabled = "disabled"
	ProxyStatusExpired  = "expired"
	ProxyStatusInvalid  = "invalid"
)

type ProxyImportBatch struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	SourceFilename string    `json:"source_filename,omitempty"`
	TotalLines     int       `json:"total_lines"`
	AcceptedCount  int       `json:"accepted_count"`
	DuplicateCount int       `json:"duplicate_count"`
	InvalidCount   int       `json:"invalid_count"`
	ExpiresAt      time.Time `json:"expires_at"`
	Enabled        bool      `json:"enabled"`
	Status         string    `json:"status"`
	NodeCount      int64     `json:"node_count"`
	HealthyCount   int64     `json:"healthy_count"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type ProxyNode struct {
	ID                 int64      `json:"id"`
	BatchID            *int64     `json:"batch_id,omitempty"`
	Scheme             string     `json:"scheme"`
	Host               string     `json:"host"`
	Port               int        `json:"port"`
	DisplayURL         string     `json:"display_url"`
	HasAuth            bool       `json:"has_auth"`
	Fingerprint        []byte     `json:"-"`
	Ciphertext         []byte     `json:"-"`
	Nonce              []byte     `json:"-"`
	KeyVersion         int        `json:"-"`
	Enabled            bool       `json:"enabled"`
	Status             string     `json:"status"`
	LatencyMS          int64      `json:"latency_ms"`
	SuccessCount       int64      `json:"success_count"`
	FailureCount       int64      `json:"failure_count"`
	ConsecutiveFailure int        `json:"consecutive_failures"`
	LastCheckedAt      *time.Time `json:"last_checked_at,omitempty"`
	LastSuccessAt      *time.Time `json:"last_success_at,omitempty"`
	LastFailureAt      *time.Time `json:"last_failure_at,omitempty"`
	CooldownUntil      *time.Time `json:"cooldown_until,omitempty"`
	ExpiresAt          time.Time  `json:"expires_at"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	BatchName          string     `json:"batch_name,omitempty"`
}

type ProxyNodeInput struct {
	Scheme      string
	Host        string
	Port        int
	DisplayURL  string
	HasAuth     bool
	Ciphertext  []byte
	Nonce       []byte
	KeyVersion  int
	Fingerprint []byte
	ExpiresAt   time.Time
}

type ProxyImportInput struct {
	Name           string
	SourceFilename string
	ExpiresAt      time.Time
	TotalLines     int
	InvalidCount   int
}

type ProxyImportResult struct {
	Batch      ProxyImportBatch `json:"batch"`
	Accepted   int              `json:"accepted"`
	Duplicates int              `json:"duplicates"`
	Invalid    int              `json:"invalid"`
	Errors     []string         `json:"errors,omitempty"`
}

type ProxyNodeFilter struct {
	Status   string
	Query    string
	BatchID  *int64
	Page     int
	PageSize int
}

type ProxyNodePage struct {
	Items    []ProxyNode `json:"items"`
	Total    int64       `json:"total"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
}

type ProxyTargetStat struct {
	ProxyID            int64
	TargetKey          string
	SuccessCount       int64
	FailureCount       int64
	LatencyMS          int64
	ConsecutiveFailure int
	CooldownUntil      *time.Time
}

type ProxyPolicy struct {
	ID         int64  `json:"id,omitempty"`
	TargetType string `json:"target_type"`
	TargetKey  string `json:"target_key"`
	Mode       string `json:"mode"`
}

type ProxyPoolSummary struct {
	Total          int64 `json:"total"`
	Healthy        int64 `json:"healthy"`
	Pending        int64 `json:"pending"`
	Cooling        int64 `json:"cooling"`
	Disabled       int64 `json:"disabled"`
	Expired        int64 `json:"expired"`
	Invalid        int64 `json:"invalid"`
	InUse          int   `json:"in_use"`
	RoutingEnabled bool  `json:"routing_enabled"`
	Probing        bool  `json:"probing"`
	ProbeJobs      int   `json:"probe_jobs"`
}

func scanProxyNode(row interface{ Scan(...any) error }) (ProxyNode, error) {
	var node ProxyNode
	err := row.Scan(
		&node.ID, &node.BatchID, &node.Scheme, &node.Host, &node.Port, &node.DisplayURL,
		&node.HasAuth, &node.Ciphertext, &node.Nonce, &node.KeyVersion, &node.Fingerprint,
		&node.Enabled, &node.Status, &node.LatencyMS, &node.SuccessCount, &node.FailureCount,
		&node.ConsecutiveFailure, &node.LastCheckedAt, &node.LastSuccessAt, &node.LastFailureAt,
		&node.CooldownUntil, &node.ExpiresAt, &node.CreatedAt, &node.UpdatedAt, &node.BatchName,
	)
	return node, err
}

const proxyNodeColumns = `
    n.id, n.batch_id, n.scheme, n.host, n.port, n.display_url,
    n.has_auth, n.ciphertext, n.nonce, n.key_version, n.fingerprint,
    n.enabled, n.status, n.latency_ms, n.success_count, n.failure_count,
    n.consecutive_failures, n.last_checked_at, n.last_success_at, n.last_failure_at,
    n.cooldown_until, n.expires_at, n.created_at, n.updated_at, COALESCE(b.name, '')`

func (s *Store) ImportProxyNodes(ctx context.Context, input ProxyImportInput, nodes []ProxyNodeInput) (ProxyImportResult, error) {
	if s == nil || s.pool == nil {
		return ProxyImportResult{}, errors.New("storage is disabled")
	}
	if input.ExpiresAt.IsZero() || !input.ExpiresAt.After(s.now()) || len(nodes) == 0 {
		return ProxyImportResult{}, ErrInvalid
	}
	if strings.TrimSpace(input.Name) == "" {
		input.Name = "代理批次 " + input.ExpiresAt.Format("2006-01-02")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ProxyImportResult{}, fmt.Errorf("begin proxy import: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var batch ProxyImportBatch
	err = tx.QueryRow(ctx, `INSERT INTO proxy_import_batches(name, source_filename, total_lines, invalid_count, expires_at)
        VALUES($1,$2,$3,$4,$5)
        RETURNING id,name,source_filename,total_lines,accepted_count,duplicate_count,invalid_count,expires_at,enabled,created_at,updated_at`,
		input.Name, input.SourceFilename, input.TotalLines, input.InvalidCount, input.ExpiresAt).Scan(
		&batch.ID, &batch.Name, &batch.SourceFilename, &batch.TotalLines, &batch.AcceptedCount,
		&batch.DuplicateCount, &batch.InvalidCount, &batch.ExpiresAt, &batch.Enabled, &batch.CreatedAt, &batch.UpdatedAt)
	if err != nil {
		return ProxyImportResult{}, fmt.Errorf("create proxy batch: %w", err)
	}
	accepted, duplicates := 0, 0
	for start := 0; start < len(nodes); start += 500 {
		end := start + 500
		if end > len(nodes) {
			end = len(nodes)
		}
		batchQueue := &pgx.Batch{}
		for _, node := range nodes[start:end] {
			batchQueue.Queue(`INSERT INTO proxy_nodes(
                batch_id,scheme,host,port,display_url,has_auth,ciphertext,nonce,key_version,fingerprint,expires_at)
                VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
                ON CONFLICT(fingerprint) DO UPDATE SET
                    batch_id=EXCLUDED.batch_id, scheme=EXCLUDED.scheme, host=EXCLUDED.host,
                    port=EXCLUDED.port, display_url=EXCLUDED.display_url, has_auth=EXCLUDED.has_auth,
                    ciphertext=EXCLUDED.ciphertext, nonce=EXCLUDED.nonce, key_version=EXCLUDED.key_version,
                    expires_at=EXCLUDED.expires_at, enabled=TRUE, status='pending',
                    consecutive_failures=0, cooldown_until=NULL, updated_at=now()
                WHERE proxy_nodes.expires_at <= now() OR proxy_nodes.status='expired'
                RETURNING id`, batch.ID, node.Scheme, node.Host, node.Port, node.DisplayURL,
				node.HasAuth, node.Ciphertext, node.Nonce, node.KeyVersion, node.Fingerprint, node.ExpiresAt)
		}
		results := tx.SendBatch(ctx, batchQueue)
		for range nodes[start:end] {
			var id int64
			scanErr := results.QueryRow().Scan(&id)
			if scanErr == nil {
				accepted++
			} else if errors.Is(scanErr, pgx.ErrNoRows) {
				duplicates++
			} else {
				_ = results.Close()
				return ProxyImportResult{}, fmt.Errorf("insert proxy node: %w", scanErr)
			}
		}
		if err := results.Close(); err != nil {
			return ProxyImportResult{}, fmt.Errorf("close proxy import batch: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE proxy_import_batches SET accepted_count=$2,duplicate_count=$3,updated_at=now() WHERE id=$1`, batch.ID, accepted, duplicates); err != nil {
		return ProxyImportResult{}, fmt.Errorf("update proxy batch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ProxyImportResult{}, fmt.Errorf("commit proxy import: %w", err)
	}
	batch.AcceptedCount, batch.DuplicateCount = accepted, duplicates
	batch.Status = proxyBatchStatus(batch, s.now())
	return ProxyImportResult{Batch: batch, Accepted: accepted, Duplicates: duplicates, Invalid: input.InvalidCount}, nil
}

func proxyBatchStatus(batch ProxyImportBatch, now time.Time) string {
	if !batch.Enabled {
		return ProxyStatusDisabled
	}
	if !batch.ExpiresAt.After(now) {
		return ProxyStatusExpired
	}
	return "active"
}

func (s *Store) ListProxyNodes(ctx context.Context, filter ProxyNodeFilter) (ProxyNodePage, error) {
	if s == nil || s.pool == nil {
		return ProxyNodePage{}, errors.New("storage is disabled")
	}
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.PageSize < 1 || filter.PageSize > 200 {
		filter.PageSize = 50
	}
	where := []string{"1=1"}
	args := []any{}
	if status := strings.TrimSpace(filter.Status); status != "" {
		args = append(args, status)
		where = append(where, fmt.Sprintf("(CASE WHEN n.expires_at<=now() THEN 'expired' WHEN n.enabled=FALSE THEN 'disabled' ELSE n.status END)=$%d", len(args)))
	}
	if filter.BatchID != nil {
		args = append(args, *filter.BatchID)
		where = append(where, fmt.Sprintf("n.batch_id=$%d", len(args)))
	}
	if query := strings.TrimSpace(filter.Query); query != "" {
		args = append(args, "%"+query+"%")
		where = append(where, fmt.Sprintf("(n.host ILIKE $%d OR n.display_url ILIKE $%d OR b.name ILIKE $%d)", len(args), len(args), len(args)))
	}
	whereSQL := strings.Join(where, " AND ")
	var total int64
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM proxy_nodes n LEFT JOIN proxy_import_batches b ON b.id=n.batch_id WHERE "+whereSQL, args...).Scan(&total); err != nil {
		return ProxyNodePage{}, fmt.Errorf("count proxy nodes: %w", err)
	}
	args = append(args, filter.PageSize, (filter.Page-1)*filter.PageSize)
	rows, err := s.pool.Query(ctx, "SELECT "+proxyNodeColumns+" FROM proxy_nodes n LEFT JOIN proxy_import_batches b ON b.id=n.batch_id WHERE "+whereSQL+" ORDER BY n.id DESC LIMIT $"+fmt.Sprint(len(args)-1)+" OFFSET $"+fmt.Sprint(len(args)), args...)
	if err != nil {
		return ProxyNodePage{}, fmt.Errorf("list proxy nodes: %w", err)
	}
	defer rows.Close()
	items := make([]ProxyNode, 0)
	for rows.Next() {
		node, scanErr := scanProxyNode(rows)
		if scanErr != nil {
			return ProxyNodePage{}, fmt.Errorf("scan proxy node: %w", scanErr)
		}
		items = append(items, node)
	}
	if err := rows.Err(); err != nil {
		return ProxyNodePage{}, err
	}
	return ProxyNodePage{Items: items, Total: total, Page: filter.Page, PageSize: filter.PageSize}, nil
}

func (s *Store) GetProxyNode(ctx context.Context, id int64) (ProxyNode, error) {
	if s == nil || s.pool == nil {
		return ProxyNode{}, errors.New("storage is disabled")
	}
	if id <= 0 {
		return ProxyNode{}, ErrInvalid
	}
	node, err := scanProxyNode(s.pool.QueryRow(ctx, "SELECT "+proxyNodeColumns+" FROM proxy_nodes n LEFT JOIN proxy_import_batches b ON b.id=n.batch_id WHERE n.id=$1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ProxyNode{}, ErrNotFound
	}
	if err != nil {
		return ProxyNode{}, fmt.Errorf("get proxy node: %w", err)
	}
	return node, nil
}

func (s *Store) ListRuntimeProxyNodes(ctx context.Context, now time.Time, limit int) ([]ProxyNode, error) {
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	rows, err := s.pool.Query(ctx, "SELECT "+proxyNodeColumns+" FROM proxy_nodes n LEFT JOIN proxy_import_batches b ON b.id=n.batch_id WHERE n.enabled=TRUE AND n.expires_at>$1 AND n.status IN ('healthy','cooling') ORDER BY CASE WHEN n.status='healthy' THEN 0 ELSE 1 END, n.success_count DESC, n.latency_ms, n.id LIMIT $2", now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ProxyNode, 0)
	for rows.Next() {
		node, e := scanProxyNode(rows)
		if e != nil {
			return nil, e
		}
		items = append(items, node)
	}
	return items, rows.Err()
}

func (s *Store) ListRuntimeProxyTargetStats(ctx context.Context, proxyIDs []int64) ([]ProxyTargetStat, error) {
	if len(proxyIDs) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT proxy_id,target_key,success_count,failure_count,latency_ms,consecutive_failures,cooldown_until
        FROM proxy_target_stats WHERE proxy_id=ANY($1::bigint[])`, proxyIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ProxyTargetStat, 0)
	for rows.Next() {
		var item ProxyTargetStat
		if err := rows.Scan(&item.ProxyID, &item.TargetKey, &item.SuccessCount, &item.FailureCount, &item.LatencyMS, &item.ConsecutiveFailure, &item.CooldownUntil); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListProxyProbeCandidates(ctx context.Context, now, before time.Time, limit int) ([]ProxyNode, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, "SELECT "+proxyNodeColumns+" FROM proxy_nodes n LEFT JOIN proxy_import_batches b ON b.id=n.batch_id WHERE n.enabled=TRUE AND n.expires_at>$1 AND n.status IN ('pending','healthy','cooling') AND (n.status<>'cooling' OR n.cooldown_until IS NULL OR n.cooldown_until<=$1) AND (n.last_checked_at IS NULL OR n.last_checked_at<$2) ORDER BY n.last_checked_at NULLS FIRST, n.id LIMIT $3", now, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ProxyNode, 0)
	for rows.Next() {
		node, e := scanProxyNode(rows)
		if e != nil {
			return nil, e
		}
		items = append(items, node)
	}
	return items, rows.Err()
}

func (s *Store) RecordProxyProbe(ctx context.Context, id int64, success bool, latency time.Duration, failures int, cooldown time.Duration) error {
	statusOnFailure := ProxyStatusPending
	if failures > 0 {
		statusOnFailure = ProxyStatusCooling
	}
	_, err := s.pool.Exec(ctx, `UPDATE proxy_nodes SET
        status=CASE WHEN $2 THEN 'healthy' ELSE CASE WHEN consecutive_failures+1 >= $3 THEN $4 ELSE 'pending' END END,
        latency_ms=CASE WHEN $5>0 THEN $5 ELSE latency_ms END,
        success_count=success_count+CASE WHEN $2 THEN 1 ELSE 0 END,
        failure_count=failure_count+CASE WHEN $2 THEN 0 ELSE 1 END,
        consecutive_failures=CASE WHEN $2 THEN 0 ELSE consecutive_failures+1 END,
        last_checked_at=now(), last_success_at=CASE WHEN $2 THEN now() ELSE last_success_at END,
        last_failure_at=CASE WHEN $2 THEN last_failure_at ELSE now() END,
        cooldown_until=CASE WHEN $2 OR consecutive_failures+1 < $3 THEN NULL ELSE now()+$6 END,
        updated_at=now() WHERE id=$1`, id, success, failures, statusOnFailure, latency.Milliseconds(), cooldown)
	return err
}

type ProxyOutcomeRecord struct {
	ProxyID                  int64
	TargetKey                string
	Success                  bool
	FailureScope             string
	Latency                  time.Duration
	ConsecutiveFailures      int
	CooldownUntil            *time.Time
	TargetConsecutiveFailure int
	TargetCooldownUntil      *time.Time
}

func (s *Store) RecordProxyOutcome(ctx context.Context, outcome ProxyOutcomeRecord) error {
	if outcome.ProxyID <= 0 {
		return nil
	}
	scope := strings.ToLower(strings.TrimSpace(outcome.FailureScope))
	if !outcome.Success && scope == "" {
		scope = "node"
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if outcome.Success {
		if _, err := tx.Exec(ctx, `UPDATE proxy_nodes SET status='healthy',
            latency_ms=CASE WHEN $2>0 THEN $2 ELSE latency_ms END,
            success_count=success_count+1,consecutive_failures=0,cooldown_until=NULL,
            last_success_at=now(),updated_at=now() WHERE id=$1`, outcome.ProxyID, outcome.Latency.Milliseconds()); err != nil {
			return err
		}
	} else if scope == "node" {
		if _, err := tx.Exec(ctx, `UPDATE proxy_nodes SET
            status=CASE WHEN $4::timestamptz IS NULL THEN status ELSE 'cooling' END,
            latency_ms=CASE WHEN $2>0 THEN $2 ELSE latency_ms END,
            failure_count=failure_count+1,consecutive_failures=$3,cooldown_until=$4,
            last_failure_at=now(),updated_at=now() WHERE id=$1`, outcome.ProxyID, outcome.Latency.Milliseconds(), outcome.ConsecutiveFailures, outcome.CooldownUntil); err != nil {
			return err
		}
	}
	if strings.TrimSpace(outcome.TargetKey) != "" && (outcome.Success || scope == "node" || scope == "target") {
		if _, err := tx.Exec(ctx, `INSERT INTO proxy_target_stats(proxy_id,target_key,success_count,failure_count,latency_ms,last_success_at,last_failure_at,consecutive_failures,cooldown_until)
            VALUES($1,$2,CASE WHEN $3 THEN 1 ELSE 0 END,CASE WHEN $3 THEN 0 ELSE 1 END,$4,CASE WHEN $3 THEN now() ELSE NULL END,CASE WHEN $3 THEN NULL ELSE now() END,$5,$6)
            ON CONFLICT(proxy_id,target_key) DO UPDATE SET success_count=proxy_target_stats.success_count+CASE WHEN $3 THEN 1 ELSE 0 END,
            failure_count=proxy_target_stats.failure_count+CASE WHEN $3 THEN 0 ELSE 1 END,
            latency_ms=CASE WHEN $4>0 THEN $4 ELSE proxy_target_stats.latency_ms END,
            last_success_at=CASE WHEN $3 THEN now() ELSE proxy_target_stats.last_success_at END,
            last_failure_at=CASE WHEN $3 THEN proxy_target_stats.last_failure_at ELSE now() END,
            consecutive_failures=$5,cooldown_until=$6,updated_at=now()`, outcome.ProxyID, outcome.TargetKey, outcome.Success, outcome.Latency.Milliseconds(), outcome.TargetConsecutiveFailure, outcome.TargetCooldownUntil); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) SetProxyNodeEnabled(ctx context.Context, id int64, enabled bool) error {
	status := ProxyStatusDisabled
	if enabled {
		status = ProxyStatusPending
	}
	result, err := s.pool.Exec(ctx, "UPDATE proxy_nodes SET enabled=$2,status=CASE WHEN $2=FALSE THEN $4 WHEN expires_at<=now() THEN 'expired' ELSE $3 END,updated_at=now() WHERE id=$1", id, enabled, status, ProxyStatusDisabled)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteProxyNode(ctx context.Context, id int64) error {
	result, err := s.pool.Exec(ctx, "DELETE FROM proxy_nodes WHERE id=$1", id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetProxyBatchEnabled(ctx context.Context, id int64, enabled bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	result, err := tx.Exec(ctx, "UPDATE proxy_import_batches SET enabled=$2,updated_at=now() WHERE id=$1", id, enabled)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	status := ProxyStatusDisabled
	if enabled {
		status = ProxyStatusPending
	}
	if _, err := tx.Exec(ctx, "UPDATE proxy_nodes SET enabled=$2,status=CASE WHEN $2=FALSE THEN $4 WHEN expires_at<=now() THEN 'expired' ELSE $3 END,updated_at=now() WHERE batch_id=$1", id, enabled, status, ProxyStatusDisabled); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ListProxyBatches(ctx context.Context, page, pageSize int) ([]ProxyImportBatch, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	var total int64
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM proxy_import_batches").Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `SELECT b.id,b.name,b.source_filename,b.total_lines,b.accepted_count,b.duplicate_count,b.invalid_count,b.expires_at,b.enabled,b.created_at,b.updated_at,
        count(n.id),count(n.id) FILTER (WHERE n.status='healthy' AND n.enabled=TRUE AND n.expires_at>now())
        FROM proxy_import_batches b LEFT JOIN proxy_nodes n ON n.batch_id=b.id GROUP BY b.id ORDER BY b.id DESC LIMIT $1 OFFSET $2`, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]ProxyImportBatch, 0)
	for rows.Next() {
		var item ProxyImportBatch
		if err := rows.Scan(&item.ID, &item.Name, &item.SourceFilename, &item.TotalLines, &item.AcceptedCount, &item.DuplicateCount, &item.InvalidCount, &item.ExpiresAt, &item.Enabled, &item.CreatedAt, &item.UpdatedAt, &item.NodeCount, &item.HealthyCount); err != nil {
			return nil, 0, err
		}
		item.Status = proxyBatchStatus(item, s.now())
		items = append(items, item)
	}
	return items, total, rows.Err()
}

func (s *Store) ProxyPoolSummary(ctx context.Context, now time.Time) (ProxyPoolSummary, error) {
	var summary ProxyPoolSummary
	rows, err := s.pool.Query(ctx, `SELECT CASE WHEN expires_at<=$1 THEN 'expired' WHEN enabled=FALSE THEN 'disabled' ELSE status END AS effective_status,count(*) FROM proxy_nodes GROUP BY effective_status`, now)
	if err != nil {
		return summary, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return summary, err
		}
		summary.Total += count
		switch status {
		case ProxyStatusHealthy:
			summary.Healthy = count
		case ProxyStatusPending:
			summary.Pending = count
		case ProxyStatusCooling:
			summary.Cooling = count
		case ProxyStatusDisabled:
			summary.Disabled = count
		case ProxyStatusExpired:
			summary.Expired = count
		case ProxyStatusInvalid:
			summary.Invalid = count
		}
	}
	return summary, rows.Err()
}

func (s *Store) ListProxyPolicies(ctx context.Context) ([]ProxyPolicy, error) {
	rows, err := s.pool.Query(ctx, "SELECT id,target_type,target_key,mode FROM proxy_route_policies ORDER BY target_type,target_key")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ProxyPolicy, 0)
	for rows.Next() {
		var p ProxyPolicy
		if err := rows.Scan(&p.ID, &p.TargetType, &p.TargetKey, &p.Mode); err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	return items, rows.Err()
}

func (s *Store) ReplaceProxyPolicies(ctx context.Context, policies []ProxyPolicy) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, "DELETE FROM proxy_route_policies"); err != nil {
		return err
	}
	seen := make(map[string]struct{})
	for _, p := range policies {
		p.TargetType = strings.TrimSpace(strings.ToLower(p.TargetType))
		p.TargetKey = strings.TrimSpace(p.TargetKey)
		p.Mode = strings.TrimSpace(strings.ToLower(p.Mode))
		if p.TargetType == "" || p.TargetKey == "" || p.Mode == "" {
			continue
		}
		key := p.TargetType + "\x00" + p.TargetKey
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if _, err := tx.Exec(ctx, "INSERT INTO proxy_route_policies(target_type,target_key,mode) VALUES($1,$2,$3)", p.TargetType, p.TargetKey, p.Mode); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, "INSERT INTO proxy_route_policies(target_type,target_key,mode) VALUES('global','*','baseline_first') ON CONFLICT DO NOTHING"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
