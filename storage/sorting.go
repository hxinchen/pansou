package storage

import (
	"fmt"
	"strings"
)

type sortField struct {
	Expression string
	TieBreaker string
	NullsLast  bool
}

var resourceSortFields = map[string]sortField{
	"resource":        {Expression: "lower(COALESCE(r.title,''))", TieBreaker: "r.id", NullsLast: true},
	"platform":        {Expression: "lower(r.platform)", TieBreaker: "r.id", NullsLast: true},
	"check_status":    {Expression: "CASE r.check_status WHEN 'valid' THEN 0 WHEN 'pending' THEN 1 WHEN 'unknown' THEN 2 WHEN 'unsupported' THEN 3 WHEN 'invalid' THEN 4 ELSE 5 END", TieBreaker: "r.id"},
	"source_count":    {Expression: "(SELECT count(*) FROM resource_sources rs_sort WHERE rs_sort.resource_id=r.id)", TieBreaker: "r.id"},
	"discovery_count": {Expression: "r.discovery_count", TieBreaker: "r.id"},
	"last_seen_at":    {Expression: "r.last_seen_at", TieBreaker: "r.id", NullsLast: true},
	"first_seen_at":   {Expression: "r.first_seen_at", TieBreaker: "r.id", NullsLast: true},
}

var keywordSortFields = map[string]sortField{
	"keyword":          {Expression: "lower(keyword)", TieBreaker: "id", NullsLast: true},
	"keyword_type":     {Expression: "lower(keyword_type)", TieBreaker: "id", NullsLast: true},
	"priority":         {Expression: "priority", TieBreaker: "id"},
	"cooldown_seconds": {Expression: "COALESCE(cooldown_seconds,604800)", TieBreaker: "id"},
	"next_eligible_at": {Expression: "COALESCE(next_eligible_at,'-infinity'::timestamptz)", TieBreaker: "id"},
	"enabled":          {Expression: "CASE WHEN enabled THEN 0 ELSE 1 END", TieBreaker: "id"},
}

var keywordAPISourceSortFields = map[string]sortField{
	"name":                  {Expression: "lower(name)", TieBreaker: "id", NullsLast: true},
	"request_url":           {Expression: "lower(request_url)", TieBreaker: "id", NullsLast: true},
	"sync_interval_seconds": {Expression: "sync_interval_seconds", TieBreaker: "id"},
	"last_status":           {Expression: "CASE last_status WHEN 'running' THEN 0 WHEN 'queued' THEN 1 WHEN 'pending' THEN 2 WHEN 'success' THEN 3 WHEN 'partial' THEN 4 WHEN 'failed' THEN 5 WHEN 'interrupted' THEN 6 WHEN 'cancelled' THEN 7 ELSE 8 END", TieBreaker: "id"},
	"last_item_count":       {Expression: "last_item_count", TieBreaker: "id"},
}

var keywordAPISyncRunSortFields = map[string]sortField{
	"source_name":  {Expression: "lower(source_name_snapshot)", TieBreaker: "id", NullsLast: true},
	"status":       {Expression: "CASE status WHEN 'running' THEN 0 WHEN 'queued' THEN 1 WHEN 'pending' THEN 2 WHEN 'success' THEN 3 WHEN 'partial' THEN 4 WHEN 'failed' THEN 5 WHEN 'interrupted' THEN 6 WHEN 'cancelled' THEN 7 WHEN 'legacy' THEN 8 ELSE 9 END", TieBreaker: "id"},
	"progress":     {Expression: "CASE WHEN trigger='legacy' OR status='legacy' THEN 1 WHEN total_iterations IS NOT NULL AND total_iterations>0 THEN completed_iterations::numeric/total_iterations WHEN status IN ('success','partial','failed','interrupted','cancelled') THEN 1 ELSE 0 END", TieBreaker: "id"},
	"unique_count": {Expression: "unique_count", TieBreaker: "id"},
	"trigger":      {Expression: "lower(trigger)", TieBreaker: "id", NullsLast: true},
	"started_at":   {Expression: "COALESCE(started_at,queued_at,created_at)", TieBreaker: "id", NullsLast: true},
}

var keywordAPISyncIterationSortFields = map[string]sortField{
	"sequence":          {Expression: "sequence", TieBreaker: "id"},
	"status":            {Expression: "CASE status WHEN 'running' THEN 0 WHEN 'queued' THEN 1 WHEN 'success' THEN 2 WHEN 'failed' THEN 3 WHEN 'skipped' THEN 4 WHEN 'interrupted' THEN 5 ELSE 6 END", TieBreaker: "id"},
	"http_status":       {Expression: "NULLIF(http_status,0)", TieBreaker: "id", NullsLast: true},
	"duration_ms":       {Expression: "duration_ms", TieBreaker: "id"},
	"raw_item_count":    {Expression: "raw_item_count", TieBreaker: "id"},
	"new_keyword_count": {Expression: "new_keyword_count", TieBreaker: "id"},
	"detail":            {Expression: "COALESCE(NULLIF(error_message,''),NULLIF(samples->>0,''))", TieBreaker: "id", NullsLast: true},
}

var collectionRunSortFields = map[string]sortField{
	"id":              {Expression: "cr.id"},
	"trigger":         {Expression: "lower(cr.trigger)", TieBreaker: "cr.id", NullsLast: true},
	"status":          {Expression: "CASE cr.status WHEN 'running' THEN 0 WHEN 'pending' THEN 1 WHEN 'success' THEN 2 WHEN 'success_empty' THEN 3 WHEN 'partial' THEN 4 WHEN 'failed' THEN 5 ELSE 6 END", TieBreaker: "cr.id"},
	"progress":        {Expression: "CASE WHEN count(i.id)=0 THEN 0 ELSE count(i.id) FILTER (WHERE i.status IN ('success','success_empty','failed'))::numeric/count(i.id) END", TieBreaker: "cr.id"},
	"new_count":       {Expression: "COALESCE(sum(i.new_count),0)", TieBreaker: "cr.id"},
	"duplicate_count": {Expression: "COALESCE(sum(i.duplicate_count),0)", TieBreaker: "cr.id"},
	"started_at":      {Expression: "COALESCE(cr.started_at,cr.created_at)", TieBreaker: "cr.id", NullsLast: true},
	"duration":        {Expression: "EXTRACT(EPOCH FROM (COALESCE(cr.completed_at,now())-COALESCE(cr.started_at,cr.created_at)))", TieBreaker: "cr.id", NullsLast: true},
}

var userSortFields = map[string]sortField{
	"username":      {Expression: "lower(username)", TieBreaker: "id", NullsLast: true},
	"role":          {Expression: "CASE role WHEN 'admin' THEN 0 WHEN 'user' THEN 1 ELSE 2 END", TieBreaker: "id"},
	"status":        {Expression: "CASE WHEN NOT enabled THEN 3 WHEN expires_at IS NOT NULL AND expires_at<=now() THEN 2 WHEN must_change_password THEN 1 ELSE 0 END", TieBreaker: "id"},
	"rps_limit":     {Expression: "CASE WHEN rate_limit_disabled THEN 2147483647 ELSE rps_limit END", TieBreaker: "id"},
	"expires_at":    {Expression: "COALESCE(expires_at,'infinity'::timestamptz)", TieBreaker: "id"},
	"api_key":       {Expression: "NULLIF((SELECT ak_sort.key_prefix FROM api_keys ak_sort WHERE ak_sort.user_id=users.id),'')", TieBreaker: "id", NullsLast: true},
	"last_login_at": {Expression: "last_login_at", TieBreaker: "id", NullsLast: true},
}

var apiRequestLogSortFields = map[string]sortField{
	"created_at":   {Expression: "l.created_at", TieBreaker: "l.id", NullsLast: true},
	"username":     {Expression: "lower(u.username)", TieBreaker: "l.id", NullsLast: true},
	"request":      {Expression: "lower(l.method||' '||l.endpoint)", TieBreaker: "l.id", NullsLast: true},
	"keyword":      {Expression: "NULLIF(lower(l.keyword),'')", TieBreaker: "l.id", NullsLast: true},
	"status_code":  {Expression: "l.status_code", TieBreaker: "l.id"},
	"duration_ms":  {Expression: "l.duration_ms", TieBreaker: "l.id"},
	"result_count": {Expression: "l.result_count", TieBreaker: "l.id"},
	"cache_status": {Expression: "lower(l.cache_status)", TieBreaker: "l.id", NullsLast: true},
	"source_ip":    {Expression: "lower(l.source_ip)", TieBreaker: "l.id", NullsLast: true},
}

func buildSortClause(sortBy, sortDir, defaultClause string, fields map[string]sortField) (string, error) {
	sortBy = strings.ToLower(strings.TrimSpace(sortBy))
	sortDir = strings.ToLower(strings.TrimSpace(sortDir))
	if sortBy == "" {
		if sortDir != "" {
			return "", fmt.Errorf("%w: sort_dir requires sort_by", ErrInvalid)
		}
		return defaultClause, nil
	}
	field, ok := fields[sortBy]
	if !ok {
		return "", fmt.Errorf("%w: unsupported sort field %q", ErrInvalid, sortBy)
	}
	if sortDir == "" {
		sortDir = "asc"
	}
	if sortDir != "asc" && sortDir != "desc" {
		return "", fmt.Errorf("%w: unsupported sort direction %q", ErrInvalid, sortDir)
	}
	direction := strings.ToUpper(sortDir)
	clause := field.Expression + " " + direction
	if field.NullsLast {
		clause += " NULLS LAST"
	}
	if field.TieBreaker != "" && field.TieBreaker != field.Expression {
		clause += ", " + field.TieBreaker + " " + direction
	}
	return clause, nil
}
