package proxypool

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

type ParsedProxy struct {
	Scheme     string
	Host       string
	Port       int
	Canonical  string
	DisplayURL string
	HasAuth    bool
}

func ParseLine(raw string) (ParsedProxy, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "#") || strings.HasPrefix(raw, ";") {
		return ParsedProxy{}, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return ParsedProxy{}, fmt.Errorf("invalid proxy URL")
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	switch scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return ParsedProxy{}, fmt.Errorf("unsupported proxy scheme %q", u.Scheme)
	}
	if u.User != nil {
		if strings.TrimSpace(u.User.Username()) == "" {
			return ParsedProxy{}, fmt.Errorf("proxy username is empty")
		}
		if _, ok := u.User.Password(); !ok {
			return ParsedProxy{}, fmt.Errorf("proxy password is required when credentials are present")
		}
	}
	host := strings.TrimSpace(u.Hostname())
	if host == "" || strings.ContainsAny(host, "\r\n") {
		return ParsedProxy{}, fmt.Errorf("proxy host is empty")
	}
	if isBlockedHost(host) {
		return ParsedProxy{}, fmt.Errorf("private or loopback proxy host is not accepted")
	}
	portValue := u.Port()
	if portValue == "" {
		return ParsedProxy{}, fmt.Errorf("proxy port is required")
	}
	port, err := strconv.Atoi(portValue)
	if err != nil || port <= 0 || port > 65535 {
		return ParsedProxy{}, fmt.Errorf("proxy port is invalid")
	}
	hostForURL := net.JoinHostPort(host, strconv.Itoa(port))
	normalized := &url.URL{Scheme: scheme, Host: hostForURL}
	if u.User != nil {
		normalized.User = url.UserPassword(u.User.Username(), mustPassword(u.User))
	}
	canonical := normalized.String()
	display := scheme + "://"
	if u.User != nil {
		display += u.User.Username() + ":***@"
	}
	display += hostForURL
	return ParsedProxy{Scheme: scheme, Host: host, Port: port, Canonical: canonical, DisplayURL: display, HasAuth: u.User != nil}, nil
}

func mustPassword(user *url.Userinfo) string { value, _ := user.Password(); return value }

func isBlockedHost(host string) bool {
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") || strings.HasSuffix(lower, ".local") {
		return true
	}
	ip := net.ParseIP(lower)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	return false
}
