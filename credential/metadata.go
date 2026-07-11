package credential

import "strings"

// NormalizePublicMetadata clones metadata and normalizes the account selectors
// whose API representation accepts either a delimited string or an array.
func NormalizePublicMetadata(pluginKey string, metadata map[string]any) map[string]any {
	result := make(map[string]any, len(metadata))
	for key, value := range metadata {
		result[key] = value
	}
	var field string
	switch strings.ToLower(strings.TrimSpace(pluginKey)) {
	case "qqpd":
		field = "channels"
	case "weibo":
		field = "user_ids"
	default:
		return result
	}
	value, exists := result[field]
	if !exists {
		return result
	}
	result[field] = splitMetadataValues(value)
	return result
}

func splitMetadataValues(value any) []string {
	parts := make([]string, 0)
	appendValue := func(value string) {
		parts = append(parts, strings.FieldsFunc(value, func(r rune) bool {
			switch r {
			case ',', '，', ';', '；', '\n', '\r':
				return true
			default:
				return false
			}
		})...)
	}
	switch typed := value.(type) {
	case string:
		appendValue(typed)
	case []string:
		for _, item := range typed {
			appendValue(item)
		}
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				appendValue(text)
			}
		}
	default:
		return []string{}
	}
	seen := make(map[string]struct{}, len(parts))
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, exists := seen[part]; exists {
			continue
		}
		seen[part] = struct{}{}
		result = append(result, part)
	}
	return result
}
