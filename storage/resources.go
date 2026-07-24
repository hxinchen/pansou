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

func sanitizeResourceInput(input ResourceInput) ResourceInput {
	input.URL = strings.ToValidUTF8(input.URL, "")
	input.Password = strings.ToValidUTF8(input.Password, "")
	input.Platform = strings.ToValidUTF8(input.Platform, "")
	input.Title = strings.ToValidUTF8(input.Title, "")
	input.Content = strings.ToValidUTF8(input.Content, "")
	input.CheckStatus = strings.ToValidUTF8(input.CheckStatus, "")
	input.Keyword = strings.ToValidUTF8(input.Keyword, "")
	input.KeywordType = strings.ToValidUTF8(input.KeywordType, "")
	input.Source.SourceType = strings.ToValidUTF8(input.Source.SourceType, "")
	input.Source.SourceKey = strings.ToValidUTF8(input.Source.SourceKey, "")
	input.Source.SourceIdentity = strings.ToValidUTF8(input.Source.SourceIdentity, "")
	input.Source.MessageID = strings.ToValidUTF8(input.Source.MessageID, "")
	input.Source.UniqueID = strings.ToValidUTF8(input.Source.UniqueID, "")
	input.Source.Title = strings.ToValidUTF8(input.Source.Title, "")
	input.Source.Content = strings.ToValidUTF8(input.Source.Content, "")
	input.Source.Metadata = sanitizeUTF8Map(input.Source.Metadata)
	return input
}

func sanitizeUTF8Map(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	result := make(map[string]any, len(values))
	for key, value := range values {
		result[strings.ToValidUTF8(key, "")] = sanitizeUTF8Value(value)
	}
	return result
}

func sanitizeUTF8Value(value any) any {
	switch typed := value.(type) {
	case string:
		return strings.ToValidUTF8(typed, "")
	case []string:
		result := make([]string, len(typed))
		for index, item := range typed {
			result[index] = strings.ToValidUTF8(item, "")
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = sanitizeUTF8Value(item)
		}
		return result
	case map[string]any:
		return sanitizeUTF8Map(typed)
	default:
		return value
	}
}

const resourceColumns = `
	id, normalized_url, url, password, platform, title, content,
	link_datetime, check_status, last_checked_at, candidate_check_status, candidate_checked_at,
	first_seen_at, last_seen_at,
	discovery_count, created_at, updated_at`

const resourceListColumns = `
	r.id, r.normalized_url, r.url, r.platform, left(r.title, 500),
	r.link_datetime, r.check_status, r.last_checked_at, r.candidate_check_status, r.candidate_checked_at,
	r.first_seen_at, r.last_seen_at,
	r.discovery_count, r.created_at, r.updated_at`

const maxImmediateLinkCheckCandidates = 500

func addImmediateLinkCheckCandidate(summary *UpsertSummary, resource Resource) {
	if resource.CheckStatus == CheckPending && len(summary.CheckCandidates) < maxImmediateLinkCheckCandidates {
		summary.CheckCandidates = append(summary.CheckCandidates, resource)
	}
}

func scanResource(row rowScanner) (Resource, error) {
	var resource Resource
	err := row.Scan(
		&resource.ID, &resource.NormalizedURL, &resource.URL, &resource.Password,
		&resource.Platform, &resource.Title, &resource.Content, &resource.LinkDatetime,
		&resource.CheckStatus, &resource.LastCheckedAt, &resource.CandidateCheckStatus,
		&resource.CandidateCheckedAt, &resource.FirstSeenAt,
		&resource.LastSeenAt, &resource.DiscoveryCount, &resource.CreatedAt, &resource.UpdatedAt,
	)
	return resource, err
}

func scanResourceListItem(row rowScanner) (Resource, error) {
	var resource Resource
	err := row.Scan(
		&resource.ID, &resource.NormalizedURL, &resource.URL, &resource.Platform,
		&resource.Title, &resource.LinkDatetime, &resource.CheckStatus,
		&resource.LastCheckedAt, &resource.CandidateCheckStatus, &resource.CandidateCheckedAt,
		&resource.FirstSeenAt, &resource.LastSeenAt,
		&resource.DiscoveryCount, &resource.CreatedAt, &resource.UpdatedAt,
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
	input = sanitizeResourceInput(input)
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
			check_status = CASE WHEN EXCLUDED.last_checked_at IS NOT NULL THEN EXCLUDED.check_status ELSE resources.check_status END,
			last_checked_at = COALESCE(EXCLUDED.last_checked_at, resources.last_checked_at),
			candidate_check_status = CASE WHEN EXCLUDED.last_checked_at IS NOT NULL THEN NULL ELSE resources.candidate_check_status END,
			candidate_checked_at = CASE WHEN EXCLUDED.last_checked_at IS NOT NULL THEN NULL ELSE resources.candidate_checked_at END,
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
		&resource.CheckStatus, &resource.LastCheckedAt, &resource.CandidateCheckStatus,
		&resource.CandidateCheckedAt, &resource.FirstSeenAt,
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
	return s.upsertSearchResponse(ctx, keyword, keywordType, trigger, resourceSourceRef{}, response)
}

// UpsertSearchResponseFromSource persists a collector response using the
// source that was actually executed. TG and plugin collectors are authoritative
// because result-level Channel/UniqueID values are optional and not uniformly
// implemented by plugins. Other callers retain the legacy result-level source
// inference used by UpsertSearchResponse.
func (s *Store) UpsertSearchResponseFromSource(ctx context.Context, keyword, keywordType, trigger, sourceType, sourceKey string, response model.SearchResponse) (UpsertSummary, error) {
	return s.upsertSearchResponse(ctx, keyword, keywordType, trigger, canonicalCollectionSource(sourceType, sourceKey), response)
}

func (s *Store) upsertSearchResponse(ctx context.Context, keyword, keywordType, trigger string, authoritative resourceSourceRef, response model.SearchResponse) (UpsertSummary, error) {
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
			explicitSources[normalizedURL] = resourceSourceRef{
				sourceType: sourceType,
				sourceKey:  sourceKey,
				subSource:  strings.TrimSpace(link.SubSource),
			}
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
			explicit := explicitSources[normalizedURL]
			if authoritative.valid() {
				sourceType, sourceKey = authoritative.sourceType, authoritative.sourceKey
			} else if explicit.valid() {
				sourceType, sourceKey = explicit.sourceType, explicit.sourceKey
			}
			subSource := strings.TrimSpace(firstNonEmpty(result.SubSource, explicit.subSource))
			metadata := map[string]any{"tags": result.Tags, "images": result.Images, "trigger": trigger}
			if subSource != "" {
				metadata["sub_source"] = subSource
			}
			input := ResourceInput{
				URL: cleanURLInput(link.URL), Password: link.Password, Platform: link.Type,
				Title: firstNonEmpty(link.WorkTitle, result.Title), Content: result.Content,
				LinkDatetime: timePointer(linkDatetime), DiscoveredAt: discoveredAt,
				Source: ResourceSourceInput{
					SourceType: sourceType, SourceKey: sourceKey,
					SourceIdentity: firstNonEmpty(result.UniqueID, result.MessageID), MessageID: result.MessageID,
					UniqueID: result.UniqueID, Title: result.Title, Content: result.Content,
					DiscoveredAt: discoveredAt, Metadata: metadata,
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
				addImmediateLinkCheckCandidate(&summary, upserted.Resource)
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
			if authoritative.valid() {
				sourceType, sourceKey = authoritative.sourceType, authoritative.sourceKey
			}
			metadata := map[string]any{"images": link.Images, "trigger": trigger}
			if subSource := strings.TrimSpace(link.SubSource); subSource != "" {
				metadata["sub_source"] = subSource
			}
			input := ResourceInput{
				URL: cleanURLInput(link.URL), Password: link.Password, Platform: platform, Title: link.Note,
				Content: link.Note, LinkDatetime: timePointer(link.Datetime), DiscoveredAt: discoveredAt,
				Source: ResourceSourceInput{SourceType: sourceType, SourceKey: sourceKey, Title: link.Note,
					DiscoveredAt: discoveredAt, Metadata: metadata},
				Keyword: keyword, KeywordType: keywordType,
			}
			upserted, err := s.upsertResourceTx(ctx, tx, input)
			if err != nil {
				return UpsertSummary{}, err
			}
			summary.Seen++
			if upserted.Inserted {
				summary.Inserted++
				addImmediateLinkCheckCandidate(&summary, upserted.Resource)
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
	subSource  string
}

func (r resourceSourceRef) valid() bool {
	return strings.TrimSpace(r.sourceType) != "" && strings.TrimSpace(r.sourceKey) != ""
}

func canonicalCollectionSource(sourceType, sourceKey string) resourceSourceRef {
	sourceType = strings.ToLower(strings.TrimSpace(sourceType))
	if sourceType != "tg" && sourceType != "plugin" {
		return resourceSourceRef{}
	}
	sourceKey = strings.TrimSpace(sourceKey)
	if prefix := sourceType + ":"; strings.HasPrefix(strings.ToLower(sourceKey), prefix) {
		sourceKey = strings.TrimSpace(sourceKey[len(prefix):])
	}
	if sourceKey == "" {
		return resourceSourceRef{}
	}
	return resourceSourceRef{sourceType: sourceType, sourceKey: sourceKey}
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
	return s.listResources(ctx, filter, false)
}

// ListResourceSummaries returns the lightweight shape used by the admin list.
func (s *Store) ListResourceSummaries(ctx context.Context, filter ResourceFilter) (ResourcePage, error) {
	return s.listResources(ctx, filter, true)
}

func (s *Store) listResources(ctx context.Context, filter ResourceFilter, summaryOnly bool) (ResourcePage, error) {
	if s == nil || s.pool == nil {
		return ResourcePage{}, fmt.Errorf("storage is disabled")
	}
	page, pageSize := normalizePage(filter.Page, filter.PageSize, 50, 200)
	where, args := buildResourceWhere(filter)
	var total int64
	if !filter.SkipTotal {
		if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM resources r WHERE "+where, args...).Scan(&total); err != nil {
			return ResourcePage{}, fmt.Errorf("count resources: %w", err)
		}
	}
	sortBy, sortDir := filter.SortBy, filter.SortDir
	if strings.TrimSpace(sortBy) == "" {
		switch filter.Sort {
		case "first_seen_asc":
			sortBy, sortDir = "first_seen_at", "asc"
		case "first_seen_desc":
			sortBy, sortDir = "first_seen_at", "desc"
		case "discoveries_desc":
			sortBy, sortDir = "discovery_count", "desc"
		case "last_seen_asc":
			sortBy, sortDir = "last_seen_at", "asc"
		}
	}
	sortClause, err := buildSortClause(sortBy, sortDir, "r.last_seen_at DESC, r.id DESC", resourceSortFields)
	if err != nil {
		return ResourcePage{}, err
	}
	queryArgs := append(append([]any(nil), args...), pageSize, (page-1)*pageSize)
	columns := resourceColumns
	if summaryOnly {
		columns = resourceListColumns
	}
	rows, err := s.pool.Query(ctx, "SELECT "+columns+" FROM resources r WHERE "+where+" ORDER BY "+sortClause+fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2), queryArgs...)
	if err != nil {
		return ResourcePage{}, fmt.Errorf("list resources: %w", err)
	}
	defer rows.Close()
	resources := make([]Resource, 0, pageSize)
	for rows.Next() {
		var resource Resource
		var scanErr error
		if summaryOnly {
			resource, scanErr = scanResourceListItem(rows)
		} else {
			resource, scanErr = scanResource(rows)
		}
		if scanErr != nil {
			return ResourcePage{}, fmt.Errorf("scan resource: %w", scanErr)
		}
		resources = append(resources, resource)
	}
	if err := rows.Err(); err != nil {
		return ResourcePage{}, fmt.Errorf("iterate resources: %w", err)
	}
	if summaryOnly {
		if err := s.loadResourceAssociationSummaries(ctx, resources); err != nil {
			return ResourcePage{}, err
		}
	} else if err := s.loadResourceAssociations(ctx, resources); err != nil {
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
	if query := strings.TrimSpace(filter.TitleQuery); query != "" {
		placeholder := addArg("%" + query + "%")
		conditions = append(conditions, "r.title ILIKE "+placeholder)
	}
	if values := normalizeStringList(filter.Platforms); len(values) > 0 {
		conditions = append(conditions, "r.platform=ANY("+addArg(values)+"::text[])")
	}
	if values := normalizeStringList(filter.CheckStatuses); len(values) > 0 {
		conditions = append(conditions, "r.check_status=ANY("+addArg(values)+"::text[])")
	} else if !filter.IncludeInvalid {
		conditions = append(conditions, "r.check_status NOT IN ('invalid','expired','cancelled','violation')")
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
	if err := s.loadResourceAssociationSummaries(ctx, resources); err != nil {
		return Resource{}, err
	}
	return resources[0], nil
}

func (s *Store) loadResourceAssociationSummaries(ctx context.Context, resources []Resource) error {
	if len(resources) == 0 {
		return nil
	}
	ids := make([]int64, len(resources))
	byID := make(map[int64]*Resource, len(resources))
	for index := range resources {
		ids[index] = resources[index].ID
		byID[resources[index].ID] = &resources[index]
	}
	rows, err := s.pool.Query(ctx, `SELECT ids.id,
		(SELECT count(*) FROM resource_sources rs WHERE rs.resource_id=ids.id),
		(SELECT count(*) FROM resource_keywords rk WHERE rk.resource_id=ids.id)
		FROM unnest($1::bigint[]) AS ids(id)`, ids)
	if err != nil {
		return fmt.Errorf("load resource association counts: %w", err)
	}
	for rows.Next() {
		var id, sourceCount, keywordCount int64
		if err := rows.Scan(&id, &sourceCount, &keywordCount); err != nil {
			rows.Close()
			return fmt.Errorf("scan resource association counts: %w", err)
		}
		if resource := byID[id]; resource != nil {
			resource.SourceCount = sourceCount
			resource.KeywordCount = keywordCount
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate resource association counts: %w", err)
	}
	rows.Close()

	rows, err = s.pool.Query(ctx, `SELECT preview.id, preview.resource_id, preview.source_type,
		preview.source_key, preview.source_identity, left(preview.title, 300), preview.last_seen_at,
		preview.discovery_count
		FROM unnest($1::bigint[]) AS ids(id)
		CROSS JOIN LATERAL (
			SELECT * FROM resource_sources rs WHERE rs.resource_id=ids.id
			ORDER BY rs.last_seen_at DESC, rs.id DESC LIMIT 2
		) preview
		ORDER BY preview.resource_id, preview.last_seen_at DESC, preview.id DESC`, ids)
	if err != nil {
		return fmt.Errorf("load resource source previews: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var source ResourceSourcePreview
		if err := rows.Scan(&source.ID, &source.ResourceID, &source.SourceType, &source.SourceKey,
			&source.SourceIdentity, &source.Title, &source.LastSeenAt, &source.DiscoveryCount); err != nil {
			return fmt.Errorf("scan resource source preview: %w", err)
		}
		if resource := byID[source.ResourceID]; resource != nil {
			resource.SourcePreview = append(resource.SourcePreview, source)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate resource source previews: %w", err)
	}
	return nil
}

func scanResourceSource(row rowScanner) (ResourceSource, error) {
	var source ResourceSource
	var metadata []byte
	err := row.Scan(&source.ID, &source.ResourceID, &source.SourceType, &source.SourceKey,
		&source.SourceIdentity, &source.MessageID, &source.UniqueID, &source.Title, &source.Content,
		&source.DiscoveredAt, &source.FirstSeenAt, &source.LastSeenAt, &source.DiscoveryCount, &metadata)
	if err != nil {
		return ResourceSource{}, err
	}
	source.SourceMetadata = decodeMetadata(metadata)
	return source, nil
}

func scanResourceSourceListItem(row rowScanner) (ResourceSource, error) {
	var source ResourceSource
	err := row.Scan(&source.ID, &source.ResourceID, &source.SourceType, &source.SourceKey,
		&source.SourceIdentity, &source.MessageID, &source.UniqueID, &source.Title,
		&source.DiscoveredAt, &source.FirstSeenAt, &source.LastSeenAt, &source.DiscoveryCount)
	return source, err
}

func (s *Store) ListResourceSources(ctx context.Context, resourceID int64, filter ResourceAssociationFilter) (ResourceSourcePage, error) {
	if s == nil || s.pool == nil {
		return ResourceSourcePage{}, fmt.Errorf("storage is disabled")
	}
	page, pageSize := normalizePage(filter.Page, filter.PageSize, 50, 100)
	var total int64
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM resource_sources WHERE resource_id=$1", resourceID).Scan(&total); err != nil {
		return ResourceSourcePage{}, fmt.Errorf("count resource sources: %w", err)
	}
	if total == 0 {
		var exists bool
		if err := s.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM resources WHERE id=$1)", resourceID).Scan(&exists); err != nil {
			return ResourceSourcePage{}, fmt.Errorf("check resource: %w", err)
		}
		if !exists {
			return ResourceSourcePage{}, ErrNotFound
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT id, resource_id, source_type, source_key,
		source_identity, message_id, unique_id, left(title, 500), discovered_at,
		first_seen_at, last_seen_at, discovery_count
		FROM resource_sources WHERE resource_id=$1
		ORDER BY last_seen_at DESC, id DESC LIMIT $2 OFFSET $3`, resourceID, pageSize, (page-1)*pageSize)
	if err != nil {
		return ResourceSourcePage{}, fmt.Errorf("list resource sources: %w", err)
	}
	defer rows.Close()
	items := make([]ResourceSource, 0, pageSize)
	for rows.Next() {
		item, scanErr := scanResourceSourceListItem(rows)
		if scanErr != nil {
			return ResourceSourcePage{}, fmt.Errorf("scan resource source: %w", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return ResourceSourcePage{}, fmt.Errorf("iterate resource sources: %w", err)
	}
	return ResourceSourcePage{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func (s *Store) ListResourceKeywords(ctx context.Context, resourceID int64, filter ResourceAssociationFilter) (ResourceKeywordPage, error) {
	if s == nil || s.pool == nil {
		return ResourceKeywordPage{}, fmt.Errorf("storage is disabled")
	}
	page, pageSize := normalizePage(filter.Page, filter.PageSize, 50, 100)
	var total int64
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM resource_keywords WHERE resource_id=$1", resourceID).Scan(&total); err != nil {
		return ResourceKeywordPage{}, fmt.Errorf("count resource keywords: %w", err)
	}
	if total == 0 {
		var exists bool
		if err := s.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM resources WHERE id=$1)", resourceID).Scan(&exists); err != nil {
			return ResourceKeywordPage{}, fmt.Errorf("check resource: %w", err)
		}
		if !exists {
			return ResourceKeywordPage{}, ErrNotFound
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT resource_id, keyword_id, keyword,
		normalized_keyword, keyword_type, first_seen_at, last_seen_at, discovery_count
		FROM resource_keywords WHERE resource_id=$1
		ORDER BY last_seen_at DESC, normalized_keyword LIMIT $2 OFFSET $3`, resourceID, pageSize, (page-1)*pageSize)
	if err != nil {
		return ResourceKeywordPage{}, fmt.Errorf("list resource keywords: %w", err)
	}
	defer rows.Close()
	items := make([]ResourceKeyword, 0, pageSize)
	for rows.Next() {
		var item ResourceKeyword
		if err := rows.Scan(&item.ResourceID, &item.KeywordID, &item.Keyword, &item.NormalizedKeyword,
			&item.KeywordType, &item.FirstSeenAt, &item.LastSeenAt, &item.DiscoveryCount); err != nil {
			return ResourceKeywordPage{}, fmt.Errorf("scan resource keyword: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return ResourceKeywordPage{}, fmt.Errorf("iterate resource keywords: %w", err)
	}
	return ResourceKeywordPage{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
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
		SET check_status=$2, last_checked_at=$3,
			candidate_check_status=NULL, candidate_checked_at=NULL, updated_at=now()
		WHERE id=$1`, id, status, checkedAt)
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
	if s == nil || s.pool == nil {
		return fmt.Errorf("storage is disabled")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin complete resource check: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := s.completeResourceCheckTx(ctx, tx, ResourceCheckCompletion{ResourceID: id, Status: status, CheckedAt: checkedAt}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit resource check: %w", err)
	}
	return nil
}

// CompleteResourceChecks persists a small result batch in one transaction.
// Each row still uses the same locking and negative-confirmation semantics as
// CompleteResourceCheck; a failure rolls back the entire batch.
func (s *Store) CompleteResourceChecks(ctx context.Context, completions []ResourceCheckCompletion) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("storage is disabled")
	}
	if len(completions) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin complete resource checks: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, completion := range completions {
		if err := s.completeResourceCheckTx(ctx, tx, completion); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit resource checks: %w", err)
	}
	return nil
}

func (s *Store) completeResourceCheckTx(ctx context.Context, tx pgx.Tx, completion ResourceCheckCompletion) error {
	id, status, checkedAt := completion.ResourceID, completion.Status, completion.CheckedAt
	if id == 0 || !validCheckStatus(status) || status == CheckPending {
		return fmt.Errorf("%w: completed check status %q", ErrInvalid, status)
	}
	if checkedAt.IsZero() {
		checkedAt = s.now()
	}

	var currentStatus string
	var candidateStatus *string
	var candidateCheckedAt *time.Time
	err := tx.QueryRow(ctx, `SELECT check_status, candidate_check_status, candidate_checked_at
		FROM resources WHERE id=$1 FOR UPDATE`, id).Scan(&currentStatus, &candidateStatus, &candidateCheckedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock resource check: %w", err)
	}

	if !definitiveNegativeCheckStatus(status) || !checkStatusNeedsNegativeConfirmation(currentStatus) {
		_, err = tx.Exec(ctx, `UPDATE resources
			SET check_status=$2, last_checked_at=$3,
				candidate_check_status=NULL, candidate_checked_at=NULL, updated_at=now()
			WHERE id=$1`, id, status, checkedAt)
	} else if candidateStatus != nil && candidateCheckedAt != nil && !checkedAt.Before(candidateCheckedAt.Add(time.Hour)) {
		_, err = tx.Exec(ctx, `UPDATE resources
			SET check_status=$2, last_checked_at=$3,
				candidate_check_status=NULL, candidate_checked_at=NULL, updated_at=now()
			WHERE id=$1`, id, status, checkedAt)
	} else if candidateStatus != nil && candidateCheckedAt != nil {
		_, err = tx.Exec(ctx, `UPDATE resources
			SET last_checked_at=$3, candidate_check_status=$2, updated_at=now()
			WHERE id=$1`, id, status, checkedAt)
	} else {
		_, err = tx.Exec(ctx, `UPDATE resources
			SET last_checked_at=$3, candidate_check_status=$2,
				candidate_checked_at=$3, updated_at=now()
			WHERE id=$1`, id, status, checkedAt)
	}
	if err != nil {
		return fmt.Errorf("complete resource check: %w", err)
	}
	return nil
}

const resourcesDueForCheckWhere = `(check_status='pending' AND candidate_check_status IS NULL)
	OR (check_status='pending'
		AND candidate_check_status IS NOT NULL
		AND candidate_checked_at <= $3)
	OR ($1::boolean
		AND check_status=ANY($2::text[])
		AND (
			(candidate_check_status IS NOT NULL AND candidate_checked_at <= $3)
			OR (candidate_check_status IS NULL AND (last_checked_at IS NULL OR last_checked_at <= $4))
		))`

func (s *Store) linkCheckDueParameters(policy LinkCheckPolicy, at time.Time) (LinkCheckPolicy, time.Time, time.Time, error) {
	normalizedPolicy, err := normalizeLinkCheckPolicy(policy.Enabled, policy.Statuses, policy.IntervalSeconds)
	if err != nil {
		return LinkCheckPolicy{}, time.Time{}, time.Time{}, err
	}
	if at.IsZero() {
		at = s.now()
	}
	return normalizedPolicy, at.Add(-time.Hour), at.Add(-time.Duration(normalizedPolicy.IntervalSeconds) * time.Second), nil
}

func (s *Store) CountResourcesDueForCheck(ctx context.Context, policy LinkCheckPolicy, at time.Time) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("storage is disabled")
	}
	normalizedPolicy, confirmationDueBefore, regularDueBefore, err := s.linkCheckDueParameters(policy, at)
	if err != nil {
		return 0, err
	}
	var count int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM resources WHERE `+resourcesDueForCheckWhere,
		normalizedPolicy.Enabled, normalizedPolicy.Statuses, confirmationDueBefore, regularDueBefore).Scan(&count); err != nil {
		return 0, fmt.Errorf("count resources due for check: %w", err)
	}
	return count, nil
}

func (s *Store) ListResourcesDueForCheck(ctx context.Context, policy LinkCheckPolicy, limit int, at time.Time) ([]Resource, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("storage is disabled")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	normalizedPolicy, confirmationDueBefore, regularDueBefore, err := s.linkCheckDueParameters(policy, at)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, "SELECT "+resourceColumns+` FROM resources
		WHERE `+resourcesDueForCheckWhere+`
		ORDER BY
			CASE
				WHEN check_status='pending' AND candidate_check_status IS NULL THEN 0
				WHEN candidate_check_status IS NOT NULL THEN 1
				ELSE 2
			END,
			CASE
				WHEN check_status='pending' AND candidate_check_status IS NULL THEN first_seen_at
				WHEN candidate_check_status IS NOT NULL THEN candidate_checked_at
				ELSE last_checked_at
			END ASC NULLS FIRST,
			id ASC
		LIMIT $5`, normalizedPolicy.Enabled, normalizedPolicy.Statuses, confirmationDueBefore, regularDueBefore, limit)
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

func definitiveNegativeCheckStatus(status string) bool {
	switch status {
	case CheckInvalid, CheckExpired, CheckCancelled, CheckViolation:
		return true
	default:
		return false
	}
}

func checkStatusNeedsNegativeConfirmation(status string) bool {
	switch status {
	case CheckPending, CheckValid, CheckUnknown, CheckLocked:
		return true
	default:
		return false
	}
}

func validCheckStatus(status string) bool {
	switch status {
	case CheckPending, CheckValid, CheckInvalid, CheckExpired, CheckCancelled, CheckViolation, CheckLocked, CheckUnknown, CheckUnsupported:
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
