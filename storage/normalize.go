package storage

import (
	"fmt"
	"net"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"
)

var nonIdentityQueryKeys = map[string]struct{}{
	"code": {}, "pwd": {}, "passcode": {}, "password": {}, "extract_code": {}, "extraction_code": {},
	"from": {}, "source": {}, "share_from": {}, "sharefrom": {}, "spm": {}, "ref": {}, "referer": {},
}

// NormalizeKeyword creates the case-insensitive business identity for a keyword.
func NormalizeKeyword(keyword string) string {
	fields := strings.FieldsFunc(strings.TrimSpace(keyword), unicode.IsSpace)
	return strings.ToLower(strings.Join(fields, " "))
}

// NormalizeURL removes extraction codes and tracking parameters without changing
// path case, which is significant for several cloud providers.
func NormalizeURL(raw string) (string, error) {
	raw = cleanURLInput(raw)
	if raw == "" {
		return "", fmt.Errorf("%w: empty URL", ErrInvalid)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%w: URL: %v", ErrInvalid, err)
	}
	if parsed.Scheme == "" {
		return "", fmt.Errorf("%w: URL has no scheme", ErrInvalid)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Fragment = ""

	if parsed.Host != "" {
		hostname := strings.ToLower(parsed.Hostname())
		port := parsed.Port()
		if (parsed.Scheme == "https" && port == "443") || (parsed.Scheme == "http" && port == "80") {
			port = ""
		}
		if port != "" {
			parsed.Host = net.JoinHostPort(hostname, port)
		} else {
			parsed.Host = hostname
		}
	}

	query := parsed.Query()
	for key := range query {
		lower := strings.ToLower(key)
		_, remove := nonIdentityQueryKeys[lower]
		if remove || strings.HasPrefix(lower, "utm_") {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()

	if parsed.Host != "" {
		cleaned := path.Clean("/" + strings.TrimPrefix(parsed.EscapedPath(), "/"))
		if cleaned == "/." {
			cleaned = "/"
		}
		parsed.RawPath = ""
		decoded, decodeErr := url.PathUnescape(cleaned)
		if decodeErr == nil {
			parsed.Path = decoded
		}
		if parsed.Path != "/" {
			parsed.Path = strings.TrimSuffix(parsed.Path, "/")
		}
	}

	normalized := parsed.String()
	if normalized == "" {
		return "", fmt.Errorf("%w: invalid URL", ErrInvalid)
	}
	return normalized, nil
}

// ExtractionCode returns a code embedded in a share URL, if present.
func ExtractionCode(raw string) string {
	parsed, err := url.Parse(cleanURLInput(raw))
	if err != nil {
		return ""
	}
	for _, key := range []string{"pwd", "code", "passcode", "password", "extract_code", "extraction_code"} {
		if value := strings.TrimSpace(parsed.Query().Get(key)); value != "" {
			return value
		}
	}
	return ""
}

// cleanURLInput removes prose accidentally appended to a URL by message
// parsers. Raw whitespace is not valid inside an HTTP URL, so the first
// whitespace-delimited token is the complete link while later tokens are
// message text (for example a new line followed by a tag label).
func cleanURLInput(raw string) string {
	raw = strings.TrimSpace(raw)
	if index := strings.IndexFunc(raw, unicode.IsSpace); index >= 0 {
		raw = raw[:index]
	}
	return strings.TrimSpace(raw)
}

func normalizeStringList(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func nextEligibleAt(successAt time.Time, overrideSeconds *int64, defaultCooldown time.Duration) time.Time {
	if overrideSeconds != nil {
		return successAt.Add(time.Duration(*overrideSeconds) * time.Second)
	}
	return successAt.Add(defaultCooldown)
}

// NextEligibleAt computes keyword cooldown using an optional per-keyword override.
func NextEligibleAt(successAt time.Time, overrideSeconds *int64, defaultCooldown time.Duration) time.Time {
	return nextEligibleAt(successAt, overrideSeconds, defaultCooldown)
}

// IsKeywordEligible reports whether a keyword can run at the supplied instant.
func IsKeywordEligible(keyword Keyword, at time.Time) bool {
	return keyword.Enabled && (keyword.NextEligibleAt == nil || !keyword.NextEligibleAt.After(at))
}

func preferMoreComplete(current, candidate string) string {
	current = strings.TrimSpace(current)
	candidate = strings.TrimSpace(candidate)
	if len([]rune(candidate)) > len([]rune(current)) {
		return candidate
	}
	return current
}
