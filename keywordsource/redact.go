package keywordsource

import (
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

const redactedValue = "[REDACTED]"

var sensitiveAssignmentPattern = regexp.MustCompile(`(?i)(authorization|proxy-authorization|cookie|set-cookie|access[_-]?token|refresh[_-]?token|api[_-]?key|token|password|passwd|secret)\s*([:=])\s*([^\s,;&]+)`)

type sanitizedError struct {
	message string
	cause   error
}

func (e *sanitizedError) Error() string { return e.message }
func (e *sanitizedError) Unwrap() error { return e.cause }

// RedactError preserves errors.Is/errors.As while ensuring Error() does not
// expose configured credentials, request values or proxy user information.
func RedactError(err error, config RequestConfig) error {
	if err == nil {
		return nil
	}
	message := err.Error()

	replacements := make(map[string]string)
	if raw := strings.TrimSpace(config.URL); raw != "" {
		replacements[raw] = sanitizeURL(raw)
	}
	if raw := strings.TrimSpace(config.ProxyURL); raw != "" {
		replacements[raw] = sanitizeURL(raw)
	}
	for _, value := range config.Headers {
		if value != "" {
			replacements[value] = redactedValue
		}
	}
	for _, value := range config.Query {
		if value != "" {
			replacements[value] = redactedValue
		}
	}
	for key, value := range config.Form {
		if isSensitiveKey(key) && value != "" {
			replacements[value] = redactedValue
		}
	}
	if canonicalBodyType(config.BodyType) == BodyJSON {
		var document any
		if json.Unmarshal([]byte(config.Body), &document) == nil {
			collectSensitiveJSONValues(document, replacements)
		}
	}
	collectURLUserInfo(config.URL, replacements)
	collectURLUserInfo(config.ProxyURL, replacements)
	collectURLQueryValues(config.URL, replacements)
	collectURLQueryValues(config.ProxyURL, replacements)

	secrets := make([]string, 0, len(replacements))
	for secret := range replacements {
		if secret != "" && secret != redactedValue {
			secrets = append(secrets, secret)
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		message = strings.ReplaceAll(message, secret, replacements[secret])
	}
	message = sensitiveAssignmentPattern.ReplaceAllString(message, `$1$2`+redactedValue)
	return &sanitizedError{message: message, cause: err}
}

func sanitizeURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "[invalid URL]"
	}
	if parsed.User != nil {
		username := parsed.User.Username()
		if _, hasPassword := parsed.User.Password(); hasPassword {
			parsed.User = url.UserPassword(username, redactedValue)
		}
	}
	query := parsed.Query()
	for key := range query {
		query.Set(key, redactedValue)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func collectURLUserInfo(raw string, result map[string]string) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.User == nil {
		return
	}
	if username := parsed.User.Username(); username != "" {
		result[username] = redactedValue
	}
	if password, ok := parsed.User.Password(); ok && password != "" {
		result[password] = redactedValue
	}
}

func collectURLQueryValues(raw string, result map[string]string) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return
	}
	for _, values := range parsed.Query() {
		for _, value := range values {
			if value != "" {
				result[value] = redactedValue
			}
		}
	}
}

func collectSensitiveJSONValues(node any, result map[string]string) {
	switch value := node.(type) {
	case map[string]any:
		for key, child := range value {
			if isSensitiveKey(key) {
				if text, ok := scalarText(child); ok && text != "" {
					result[text] = redactedValue
				}
			}
			collectSensitiveJSONValues(child, result)
		}
	case []any:
		for _, child := range value {
			collectSensitiveJSONValues(child, result)
		}
	}
}

func isSensitiveKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.ReplaceAll(key, "-", "_")
	switch key {
	case "authorization", "proxy_authorization", "cookie", "set_cookie", "token", "access_token", "refresh_token", "api_key", "apikey", "password", "passwd", "secret", "client_secret":
		return true
	default:
		return strings.HasSuffix(key, "_token") || strings.HasSuffix(key, "_password") || strings.HasSuffix(key, "_secret")
	}
}

var _ interface{ Unwrap() error } = (*sanitizedError)(nil)
