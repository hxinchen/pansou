package tgchannel

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var ErrInvalidChannel = errors.New("invalid Telegram channel")

var publicChannelPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{4,31}$`)

// Normalize converts a public Telegram channel name or t.me URL to its
// canonical lower-case channel key.
func Normalize(value string) (string, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return "", fmt.Errorf("%w: channel is empty", ErrInvalidChannel)
	}

	withoutQuery := strings.SplitN(raw, "?", 2)[0]
	withoutQuery = strings.SplitN(withoutQuery, "#", 2)[0]
	channel := strings.TrimSpace(withoutQuery)

	lower := strings.ToLower(channel)
	for _, prefix := range []string{
		"https://www.t.me/", "http://www.t.me/",
		"https://t.me/", "http://t.me/",
		"https://www.telegram.me/", "http://www.telegram.me/",
		"https://telegram.me/", "http://telegram.me/",
		"www.t.me/", "t.me/", "www.telegram.me/", "telegram.me/",
	} {
		if strings.HasPrefix(lower, prefix) {
			channel = channel[len(prefix):]
			break
		}
	}

	channel = strings.TrimSpace(strings.Trim(channel, "/"))
	if strings.HasPrefix(strings.ToLower(channel), "s/") {
		channel = channel[2:]
	}
	channel = strings.TrimPrefix(channel, "@")
	if slash := strings.Index(channel, "/"); slash >= 0 {
		channel = channel[:slash]
	}
	channel = strings.ToLower(strings.TrimSpace(channel))

	if !publicChannelPattern.MatchString(channel) {
		return "", fmt.Errorf("%w: %q", ErrInvalidChannel, value)
	}
	return channel, nil
}

// NormalizeList canonicalizes channels, removes duplicates, and preserves the
// first occurrence order.
func NormalizeList(values []string) ([]string, error) {
	if values == nil {
		return nil, nil
	}
	if len(values) == 0 {
		return []string{}, nil
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		channel, err := Normalize(value)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[channel]; exists {
			continue
		}
		seen[channel] = struct{}{}
		result = append(result, channel)
	}
	return result, nil
}
