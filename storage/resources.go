package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"pansou/model"
)

type rowScanner interface {
	Scan(dest ...any) error
}

const resourceColumns = `
	id, normalized_url, url, password, platform, title, content,
	link_datetime, check_status, last_checked_at, first_seen_at, last_seen_at,
	discovery_count, created_at, updated_at`

func scanResource(row rowScanner) (Resource, error) {
	var resource Resource
	err := row.Scan(
		&resource.ID, &resource.NormalizedURL, &resource.URL, &resource.Password,
		&resource.Platform, &resource.Title, &resource.Content, &resource.LinkDatetime,
		&resource.CheckStatus, &resource.LastCheckedAt, &resource.FirstSeenAt,
		&resource.LastSeenAt, &resource.DiscoveryCount, &resource.CreatedAt, &resource.UpdatedAt,
	)
	return resource, err
}

func (s *Store) UpsertResource(ctx context.Context, input ResourceInput) (UpsertResult, error) {
	if s == nil || s.pool == nil {
		return UpsertResult{}, fmt.Errorf("storage is disabled")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return UpsertResult{}, fmt.Errorf("begin resource upsert: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	result, err := s.upsertResourceTx(ctx, tx, input)
	if err != nil {
		return UpsertResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return UpsertResult{}, fmt.Errorf("commit resource upsert: %w", err)
	}
	return result, nil
}

func (s *Store) upsertResourceTx(ctx context.Context, tx pgx.Tx, input ResourceInput) (UpsertResult, error) {
	cleanedURL := cleanURLInput(input.URL)
	normalizedURL, err := NormalizeURL(cleanedURL)
	if err != nil {
		return UpsertResult{}, err
	}
	seenAt := input.DiscoveredAt
	if seenAt.IsZero() {
		seenAt = s.now()
	}
	if input.Password == "" {
		input.Password = ExtractionCode(cleanedURL)
	}
	if input.CheckStatus == "" {
		input.CheckStatus = CheckPending
	}
	if !validCheckStatus(input.CheckStatus) {
		return UpsertResult{}, fmt.Errorf("%w: check status %q", ErrInvalid, input.CheckStatus)
	}

	var result UpsertResult
	row := tx.QueryRow(ctx, `
		INSERT INTO resources (
			normalized_url, url, password, platform, title, content, link_datetime,
			check_status, last_checked_at, first_seen_at, last_seen_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10)
		ON CONFLICT (normalized_url) DO UPDATE SET
			url = CASE WHEN char_length(EXCLUDED.url) > char_length(resources.url) THEN EXCLUDED.url ELSE resources.url END,
			password = CASE WHEN char_length(EXCLUDED.password) > char_length(resources.password) THEN EXCLUDED.password ELSE resources.password END,
			platform = CASE WHEN resources.platform = '' THEN EXCLUDED.platform ELSE resources.platform END,
			title = CASE WHEN char_length(EXCLUDED.title) > char_length(resources.title) THEN EXCLUDED.title ELSE resources.title END,
			content = CASE WHEN char_length(EXCLUDED.content) > char_length(resources.content) THEN EXCLUDED.content ELSE resources.content END,
			link_datetime = CASE
				WHEN resources.link_datetime IS NULL THEN EXCLUDED.link_datetime
				WHEN EXCLUDED.link_datetime IS NULL THEN resources.link_datetime
				ELSE GREATEST(resources.link_datetime, EXCLUDED.link_datetime)
			END,
			check_status = CASE
				WHEN EXCLUDED.last_checked_at IS NOT NULL THEN EXCLUDED.check_status
				WHEN resources.check_status IN ('invalid','unknown')
					AND (resources.last_checked_at IS NULL OR resources.last_checked_at <= EXCLUDED.last_seen_at - interval '7 days')
				THEN 'pending'
				ELSE resources.check_status
			END,
			last_checked_at = COALESCE(EXCLUDED.last_checked_at, resources.last_checked_at),
			first_seen_at = LEAST(resources.first_seen_at, EXCLUDED.first_seen_at),
			last_seen_at = GREATEST(resources.last_seen_at, EXCLUDED.last_seen_at),
			discovery_count = resources.discovery_count + 1,
			updated_at = now()
		RETURNING `+resourceColumns+`, (xmax = 0)`,
		normalizedURL, cleanedURL, strings.TrimSpace(input.Password),
		strings.TrimSpace(input.Platform), strings.TrimSpace(input.Title), strings.TrimSpace(input.Content),
		input.LinkDatetime, input.CheckStatus, input.LastCheckedAt, seenAt,
	)
	resource, scanErr := scanResourceWithInserted(row, &result.Inserted)
	if scanErr != nil {
		return UpsertResult{}, fmt.Errorf("upsert resource: %w", scanErr)
	}
	result.Resource = resource

	if strings.TrimSpace(input.Source.SourceType) != "" {
		if err := upsertResourceSource(ctx, tx, resource.ID, normalizedURL, seenAt, input.Source); err != nil {
			return UpsertResult{}, err
		}
	}
	if NormalizeKeyword(input.Keyword) != "" {
		if err := upsertResourceKeyword(ctx, tx, resource.ID, seenAt, input.Keyword, input.KeywordType); err != nil {
			return UpsertResult{}, err
		}
	}
	return result, nil
}

func scanResourceWithInserted(row rowScanner, inserted *bool) (Resource, error) {
	var resource Resource
	err := row.Scan(
		&resource.ID, &resource.NormalizedURL, &resource.URL, &resource.Password,
		&resource.Platform, &resource.Title, &resource.Content, &resource.LinkDatetime,
		&resource.CheckStatus, &resource.LastCheckedAt, &resource.FirstSeenAt,
		&resource.LastSeenAt, &resource.DiscoveryCount, &resource.CreatedAt, &resource.UpdatedAt,
		inserted,
	)
	return resource, err
}

func upsertResourceSource(ctx context.Context, tx pgx.Tx, resourceID int64, normalizedURL string, seenAt time.Time, input ResourceSourceInput) error {
	discoveredAt := input.DiscoveredAt
	if discoveredAt.IsZero() {
		discoveredAt = seenAt
	}
	identity := strings.TrimSpace(firstNonEmpty(input.SourceIdentity, input.UniqueID, input.MessageID, normalizedURL))
	_, err := tx.Exec(ctx, `
		INSERT INTO resource_sources (
			resource_id, source_type, source_key, source_identity, message_id, unique_id,
			title, content, discovered_at, first_seen_at, last_seen_at, source_metadata
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10,$11::jsonb)
		ON CONFLICT (resource_id, source_type, source_key, source_identity) DO UPDATE SET
			message_id = CASE WHEN resource_sources.message_id='' THEN EXCLUDED.message_id ELSE resource_sources.message_id END,
			unique_id = CASE WHEN resource_sources.unique_id='' THEN EXCLUDED.unique_id ELSE resource_sources.unique_id END,
			title = CASE WHEN char_length(EXCLUDED.title)>char_length(resource_sources.title) THEN EXCLUDED.title ELSE resource_sources.title END,
			content = CASE WHEN char_length(EXCLUDED.content)>char_length(resource_sources.content) THEN EXCLUDED.content ELSE resource_sources.content END,
			discovered_at = GREATEST(resource_sources.discovered_at, EXCLUDED.discovered_at),
			first_seen_at = LEAST(resource_sources.first_seen_at, EXCLUDED.first_seen_at),
			last_seen_at = GREATEST(resource_sources.last_seen_at, EXCLUDED.last_seen_at),
			discovery_count = resource_sources.discovery_count + 1,
			source_metadata = resource_sources.source_metadata || EXCLUDED.source_metadata`,
		resourceID, strings.TrimSpace(input.SourceType), strings.TrimSpace(input.SourceKey), identity,
		strings.TrimSpace(input.MessageID), strings.TrimSpace(input.UniqueID), strings.TrimSpace(input.Title),
		strings.TrimSpace(input.Content), discoveredAt, seenAt, metadataJSON(input.Metadata),
	)
	if err != nil {
		return fmt.Errorf("upsert resource source: %w", err)
	}
	return nil
}

func upsertResourceKeyword(ctx context.Context, tx pgx.Tx, resourceID int64, seenAt time.Time, keyword, keywordType string) error {
	normalized := NormalizeKeyword(keyword)
	if keywordType = strings.TrimSpace(keywordType); keywordType == "" {
		keywordType = DefaultKeywordType
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO resource_keywords (
			resource_id, keyword_id, keyword, normalized_keyword, keyword_type, first_seen_at, last_seen_at
		) VALUES ($1, (SELECT id FROM keywords WHERE normalized_keyword=$2), $3, $2, $4, $5, $5)
		ON CONFLICT (resource_id, normalized_keyword) DO UPDATE SET
			keyword_id = COALESCE(resource_keywords.keyword_id, EXCLUDED.keyword_id),
			keyword = CASE WHEN char_length(EXCLUDED.keyword)>char_length(resource_keywords.keyword) THEN EXCLUDED.keyword ELSE resource_keywords.keyword END,
			keyword_type = CASE WHEN resource_keywords.keyword_type='general' THEN EXCLUDED.keyword_type ELSE resource_keywords.keyword_type END,
			first_seen_at = LEAST(resource_keywords.first_seen_at, EXCLUDED.first_seen_at),
			last_seen_at = GREATEST(resource_keywords.last_seen_at, EXCLUDED.last_seen_at),
			discovery_count = resource_keywords.discovery_count + 1`,
		resourceID, normalized, strings.TrimSpace(keyword), keywordType, seenAt,
	)
	if err != nil {
		return fmt.Errorf("upsert resource keyword: %w", err)
	}
	return nil
}

func (s *Store) UpsertSearchResponse(ctx context.Context, keyword, keywordType, trigger string, response model.SearchResponse) (UpsertSummary, error) {
	if s == nil || s.pool == nil {
		return UpsertSummary{}, fmt.Errorf("storage is disabled")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return UpsertSummary{}, fmt.Errorf("begin search response upsert: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	summary := UpsertSummary{}
	seenMerged := make(map[string]struct{})
	discoveredAt := s.now()
	explicitSources := make(map[string]resourceSourceRef)
	for _, links := range response.MergedByType {
		for _, link := range links {
			normalizedURL, normalizeErr := NormalizeURL(link.URL)
			if normalizeErr != nil {
				summary.Skipped++
				continue
			}
			sourceType, sourceKey := parseMergedSource(link.Source)
			explicitSources[normalizedURL] = resourceSourceRef{sourceType: sourceType, sourceKey: sourceKey}
		}
	}
	for _, result := range response.Results {
		for _, link := range result.Links {
			if strings.TrimSpace(link.URL) == "" {
				continue
			}
			linkDatetime := link.Datetime
			if linkDatetime.IsZero() {
				linkDatetime = result.Datetime
			}
			sourceType, sourceKey := inferResultSource(result)
			normalizedURL, normalizeErr := NormalizeURL(link.URL)
			if normalizeErr != nil {
				summary.Skipped++
				continue
			}
			if explicit, exists := explicitSources[normalizedURL]; exists {
				sourceType, sourceKey = explicit.sourceType, explicit.sourceKey
			}
			input := ResourceInput{
				URL: cleanURLInput(link.URL), Password: link.Password, Platform: link.Type,
				Title: firstNonEmpty(link.WorkTitle, result.Title), Content: result.Content,
				LinkDatetime: timePointer(linkDatetime), DiscoveredAt: discoveredAt,
				Source: ResourceSourceInput{
					SourceType: sourceType, SourceKey: sourceKey,
					SourceIdentity: firstNonEmpty(result.UniqueID, result.MessageID), MessageID: result.MessageID,
					UniqueID: result.UniqueID, Title: result.Title, Content: result.Content,
					DiscoveredAt: discoveredAt, Metadata: map[string]any{"tags": result.Tags, "images": result.Images, "trigger": trigger},
				},
				Keyword: keyword, KeywordType: keywordType,
			}
			upserted, err := s.upsertResourceTx(ctx, tx, input)
			if err != nil {
				return UpsertSummary{}, err
			}
			summary.Seen++
			if upserted.Inserted {
				summary.Inserted++
			} else {
				summary.Updated++
			}
			seenMerged[upserted.Resource.NormalizedURL] = struct{}{}
		}
	}
	for platform, links := range response.MergedByType {
		for _, link := range links {
			normalizedURL, normalizeErr := NormalizeURL(link.URL)
			if normalizeErr != nil {
				summary.Skipped++
				continue
			}
			if _, exists := seenMerged[normalizedURL]; exists {
				continue
			}
			sourceType, sourceKey := parseMergedSource(link.Source)
			input := ResourceInput{
				URL: cleanURLInput(link.URL), Password: link.Password, Platform: platform, Title: link.Note,
				Content: link.Note, LinkDatetime: timePointer(link.Datetime), DiscoveredAt: discoveredAt,
				Source: ResourceSourceInput{SourceType: sourceType, SourceKey: sourceKey, Title: link.Note,
					DiscoveredAt: discoveredAt, Metadata: map[string]any{"images": link.Images, "trigger": trigger}},
				Keyword: keyword, KeywordType: keywordType,
			}
			upserted, err := s.upsertResourceTx(ctx, tx, input)
			if err != nil {
				return UpsertSummary{}, err
			}
			summary.Seen++
			if upserted.Inserted {
				summary.Inserted++
			} else {
				summary.Updated++
			}
			seenMerged[normalizedURL] = struct{}{}
		}
	}
	summary.Duplicates = summary.Seen - summary.Inserted
	if err := tx.Commit(ctx); err != nil {
		return UpsertSummary{}, fmt.Errorf("commit search response upsert: %w", err)
	}
	return summary, nil
}

type resourceSourceRef struct {
	sourceType string
	sourceKey  string
}

func inferResultSource(result model.SearchResult) (string, string) {
	if channel := strings.TrimSpace(result.Channel); channel != "" {
		return "tg", channel
	}
	if prefix, _, found := strings.Cut(strings.TrimSpace(result.UniqueID), "-"); found && prefix != "" {
		return "plugin", prefix
	}
	return "unknown", ""
}

func parseMergedSource(source string) (string, string) {
	typeName, key, found := strings.Cut(strings.TrimSpace(source), ":")
	if found && typeName != "" {
		return typeName, key
	}
	if strings.TrimSpace(source) == "" || strings.TrimSpace(source) == "unknown" {
		return "unknown", ""
	}
	return "unknown", strings.TrimSpace(source)
}

func timePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func (s *Store) SearchResources(ctx context.Context, filter ResourceFilter) (ResourcePage, error) {
	return s.ListResources(ctx, filter)
}

func (s *Store) ListResources(ctx context.Context, filter ResourceFilter) (ResourcePage, error) {
	if s == nil || s.pool == nil {
		return ResourcePage{}, fmt.Errorf("storage is disabled")
	}
	page, pageSize := normalizePage(filter.Page, filter.PageSize, 50, 200)
	where, args := buildResourceWhere(filter)
	var total int64
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM resources r WHERE "+where, args...).Scan(&total); err != nil {
		return ResourcePage{}, fmt.Errorf("count resources: %w", err)
	}
	sortClause := "r.last_seen_at DESC, r.id DESC"
	switch filter.Sort {
	case "first_seen_asc":
		sortClause = "r.first_seen_at ASC, r.id ASC"
	case "first_seen_desc":
		sortClause = "r.first_seen_at DESC, r.id DESC"
	case "discoveries_desc":
		sortClause = "r.discovery_count DESC, r.id DESC"
	case "last_seen_asc":
		sortClause = "r.last_seen_at ASC, r.id ASC"
	}
	queryArgs := append(append([]any(nil), args...), pageSize, (page-1)*pageSize)
	rows, err := s.pool.Query(ctx, "SELECT "+resourceColumns+" FROM resources r WHERE "+where+" ORDER BY "+sortClause+fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2), queryArgs...)
	if err != nil {
		return ResourcePage{}, fmt.Errorf("list resources: %w", err)
	}
	defer rows.Close()
	resources := make([]Resource, 0, pageSize)
	for rows.Next() {
		resource, scanErr := scanResource(rows)
		if scanErr != nil {
			return ResourcePage{}, fmt.Errorf("scan resource: %w", scanErr)
		}
		resources = append(resources, resource)
	}
	if err := rows.Err(); err != nil {
		return ResourcePage{}, fmt.Errorf("iterate resources: %w", err)
	}
	if err := s.loadResourceAssociations(ctx, resources); err != nil {
		return ResourcePage{}, err
	}
	return ResourcePage{Items: resources, Total: total, Page: page, PageSize: pageSize}, nil
}

func buildResourceWhere(filter ResourceFilter) (string, []any) {
	conditions := []string{"TRUE"}
	args := make([]any, 0, 12)
	addArg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if normalized := NormalizeKeyword(filter.Keyword); normalized != "" {
		placeholder := addArg(normalized)
		conditions = append(conditions, "EXISTS (SELECT 1 FROM resource_keywords rk WHERE rk.resource_id=r.id AND rk.normalized_keyword="+placeholder+")")
	}
	if filter.KeywordType != "" {
		placeholder := addArg(filter.KeywordType)
		conditions = append(conditions, "EXISTS (SELECT 1 FROM resource_keywords rk WHERE rk.resource_id=r.id AND rk.keyword_type="+placeholder+")")
	}
	if query := strings.TrimSpace(filter.Query); query != "" {
		placeholder := addArg("%" + query + "%")
		conditions = append(conditions, "(r.title ILIKE "+placeholder+" OR r.content ILIKE "+placeholder+" OR r.url ILIKE "+placeholder+")")
	}
	if values := normalizeStringList(filter.Platforms); len(values) > 0 {
		conditions = append(conditions, "r.platform=ANY("+addArg(values)+"::text[])")
	}
	if values := normalizeStringList(filter.CheckStatuses); len(values) > 0 {
		conditions = append(conditions, "r.check_status=ANY("+addArg(values)+"::text[])")
	} else if !filter.IncludeInvalid {
		conditions = append(conditions, "r.check_status<>'invalid'")
	}
	if len(filter.SourceTypes) > 0 || len(filter.SourceKeys) > 0 {
		sourceConditions := []string{"rs.resource_id=r.id"}
		if values := normalizeStringList(filter.SourceTypes); len(values) > 0 {
			sourceConditions = append(sourceConditions, "rs.source_type=ANY("+addArg(values)+"::text[])")
		}
		if values := normalizeStringList(filter.SourceKeys); len(values) > 0 {
			sourceConditions = append(sourceConditions, "rs.source_key=ANY("+addArg(values)+"::text[])")
		}
		conditions = append(conditions, "EXISTS (SELECT 1 FROM resource_sources rs WHERE "+strings.Join(sourceConditions, " AND ")+")")
	}
	includeConditions := make([]string, 0, len(filter.Include))
	for _, value := range normalizeStringList(filter.Include) {
		placeholder := addArg("%" + value + "%")
		includeConditions = append(includeConditions, "r.title ILIKE "+placeholder+" OR r.content ILIKE "+placeholder)
	}
	if len(includeConditions) > 0 {
		conditions = append(conditions, "("+strings.Join(includeConditions, " OR ")+")")
	}
	for _, value := range normalizeStringList(filter.Exclude) {
		placeholder := addArg("%" + value + "%")
		conditions = append(conditions, "NOT (r.title ILIKE "+placeholder+" OR r.content ILIKE "+placeholder+")")
	}
	if filter.From != nil {
		conditions = append(conditions, "r.last_seen_at >= "+addArg(*filter.From))
	}
	if filter.To != nil {
		conditions = append(conditions, "r.last_seen_at < "+addArg(*filter.To))
	}
	return strings.Join(conditions, " AND "), args
}

func (s *Store) GetResource(ctx context.Context, id int64) (Resource, error) {
	if s == nil || s.pool == nil {
		return Resource{}, fmt.Errorf("storage is disabled")
	}
	resource, err := scanResource(s.pool.QueryRow(ctx, "SELECT "+resourceColumns+" FROM resources WHERE id=$1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Resource{}, ErrNotFound
	}
	if err != nil {
		return Resource{}, fmt.Errorf("get resource: %w", err)
	}
	resources := []Resource{resource}
	if err := s.loadResourceAssociations(ctx, resources); err != nil {
		return Resource{}, err
	}
	return resources[0], nil
}

func (s *Store) loadResourceAssociations(ctx context.Context, resources []Resource) error {
	if len(resources) == 0 {
		return nil
	}
	ids := make([]int64, len(resources))
	byID := make(map[int64]*Resource, len(resources))
	for index := range resources {
		ids[index] = resources[index].ID
		byID[resources[index].ID] = &resources[index]
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, resource_id, source_type, source_key, source_identity, message_id,
			unique_id, title, content, discovered_at, first_seen_at, last_seen_at,
			discovery_count, source_metadata
		FROM resource_sources WHERE resource_id=ANY($1::bigint[])
		ORDER BY resource_id, last_seen_at DESC, id DESC`, ids)
	if err != nil {
		return fmt.Errorf("load resource sources: %w", err)
	}
	for rows.Next() {
		var source ResourceSource
		var metadata []byte
		if err := rows.Scan(&source.ID, &source.ResourceID, &source.SourceType, &source.SourceKey,
			&source.SourceIdentity, &source.MessageID, &source.UniqueID, &source.Title, &source.Content,
			&source.DiscoveredAt, &source.FirstSeenAt, &source.LastSeenAt, &source.DiscoveryCount, &metadata); err != nil {
			rows.Close()
			return fmt.Errorf("scan resource source: %w", err)
		}
		source.SourceMetadata = decodeMetadata(metadata)
		if resource := byID[source.ResourceID]; resource != nil {
			resource.Sources = append(resource.Sources, source)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate resource sources: %w", err)
	}
	rows.Close()

	keywordRows, err := s.pool.Query(ctx, `
		SELECT resource_id, keyword_id, keyword, normalized_keyword, keyword_type,
			first_seen_at, last_seen_at, discovery_count
		FROM resource_keywords WHERE resource_id=ANY($1::bigint[])
		ORDER BY resource_id, last_seen_at DESC`, ids)
	if err != nil {
		return fmt.Errorf("load resource keywords: %w", err)
	}
	defer keywordRows.Close()
	for keywordRows.Next() {
		var keyword ResourceKeyword
		if err := keywordRows.Scan(&keyword.ResourceID, &keyword.KeywordID, &keyword.Keyword,
			&keyword.NormalizedKeyword, &keyword.KeywordType, &keyword.FirstSeenAt,
			&keyword.LastSeenAt, &keyword.DiscoveryCount); err != nil {
			return fmt.Errorf("scan resource keyword: %w", err)
		}
		if resource := byID[keyword.ResourceID]; resource != nil {
			resource.Keywords = append(resource.Keywords, keyword)
		}
	}
	if err := keywordRows.Err(); err != nil {
		return fmt.Errorf("iterate resource keywords: %w", err)
	}
	return nil
}

func (s *Store) UpdateResourceCheck(ctx context.Context, id int64, status string, checkedAt time.Time) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("storage is disabled")
	}
	if !validCheckStatus(status) || status == CheckPending {
		return fmt.Errorf("%w: completed check status %q", ErrInvalid, status)
	}
	if checkedAt.IsZero() {
		checkedAt = s.now()
	}
	command, err := s.pool.Exec(ctx, `UPDATE resources
		SET check_status=$2, last_checked_at=$3, updated_at=now() WHERE id=$1`, id, status, checkedAt)
	if err != nil {
		return fmt.Errorf("update resource check: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CompleteResourceCheck records the terminal result of an asynchronous link check.
func (s *Store) CompleteResourceCheck(ctx context.Context, id int64, status string, checkedAt time.Time) error {
	return s.UpdateResourceCheck(ctx, id, status, checkedAt)
}

func (s *Store) ListResourcesDueForCheck(ctx context.Context, limit int, staleBefore time.Time) ([]Resource, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("storage is disabled")
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	if staleBefore.IsZero() {
		staleBefore = s.now().Add(-7 * 24 * time.Hour)
	}
	rows, err := s.pool.Query(ctx, "SELECT "+resourceColumns+` FROM resources
		WHERE check_status='pending'
			OR (check_status IN ('invalid','unknown')
				AND (last_checked_at IS NULL OR last_checked_at <= $1)
				AND (last_checked_at IS NULL OR last_seen_at > last_checked_at))
		ORDER BY CASE WHEN check_status='pending' THEN 0 ELSE 1 END, last_seen_at DESC
		LIMIT $2`, staleBefore, limit)
	if err != nil {
		return nil, fmt.Errorf("list resources due for check: %w", err)
	}
	defer rows.Close()
	resources := make([]Resource, 0, limit)
	for rows.Next() {
		resource, scanErr := scanResource(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan due resource: %w", scanErr)
		}
		resources = append(resources, resource)
	}
	return resources, rows.Err()
}

func validCheckStatus(status string) bool {
	switch status {
	case CheckPending, CheckValid, CheckInvalid, CheckUnknown, CheckUnsupported:
		return true
	default:
		return false
	}
}

// IsValidCheckStatus reports whether status can be persisted for a resource.
func IsValidCheckStatus(status string) bool { return validCheckStatus(status) }

func normalizePage(page, pageSize, defaultSize, maxSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = defaultSize
	}
	if pageSize > maxSize {
		pageSize = maxSize
	}
	return page, pageSize
}
