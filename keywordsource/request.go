package keywordsource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"
	"golang.org/x/net/proxy"
)

type HTTPStatusError struct {
	StatusCode int
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("keyword source returned HTTP status %d", e.StatusCode)
}

func canonicalMethod(method string) string {
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		return http.MethodGet
	}
	return method
}

func canonicalBodyType(bodyType BodyType) BodyType {
	if bodyType == "" {
		return BodyNone
	}
	return BodyType(strings.ToLower(strings.TrimSpace(string(bodyType))))
}

func effectiveTimeout(seconds int) int {
	if seconds == 0 {
		return DefaultTimeoutSeconds
	}
	return seconds
}

func effectiveMaxRedirects(limit int) int {
	if limit == 0 {
		return DefaultMaxRedirects
	}
	return limit
}

// ValidateRequestConfig validates without sending a request.
func ValidateRequestConfig(config RequestConfig) error {
	switch canonicalMethod(config.Method) {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch:
	default:
		return fmt.Errorf("%w: unsupported method", ErrInvalidConfig)
	}

	target, err := url.Parse(strings.TrimSpace(config.URL))
	if err != nil || target.Host == "" {
		return fmt.Errorf("%w: request URL must be absolute", ErrInvalidConfig)
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return fmt.Errorf("%w: request URL must use http or https", ErrInvalidConfig)
	}

	for key, value := range config.Headers {
		if !httpguts.ValidHeaderFieldName(key) || !httpguts.ValidHeaderFieldValue(value) {
			return fmt.Errorf("%w: invalid request header", ErrInvalidConfig)
		}
	}
	for key := range config.Query {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("%w: query parameter name is empty", ErrInvalidConfig)
		}
	}

	switch canonicalBodyType(config.BodyType) {
	case BodyNone:
	case BodyJSON:
		if !json.Valid([]byte(config.Body)) {
			return fmt.Errorf("%w: JSON body is invalid", ErrInvalidConfig)
		}
	case BodyForm:
		for key := range config.Form {
			if strings.TrimSpace(key) == "" {
				return fmt.Errorf("%w: form field name is empty", ErrInvalidConfig)
			}
		}
	case BodyRaw:
	default:
		return fmt.Errorf("%w: unsupported body type", ErrInvalidConfig)
	}

	timeout := effectiveTimeout(config.TimeoutSeconds)
	if timeout < MinTimeoutSeconds || timeout > MaxTimeoutSeconds {
		return fmt.Errorf("%w: timeout must be between 1 and 60 seconds", ErrInvalidConfig)
	}
	redirects := effectiveMaxRedirects(config.MaxRedirects)
	if redirects < 1 || redirects > MaxRedirects {
		return fmt.Errorf("%w: redirect limit must be between 1 and 10", ErrInvalidConfig)
	}
	if err := validateProxyURL(config.ProxyURL); err != nil {
		return err
	}
	return nil
}

func validateProxyURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("%w: proxy URL must be absolute", ErrInvalidConfig)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
		return nil
	default:
		return fmt.Errorf("%w: unsupported proxy scheme", ErrInvalidConfig)
	}
}

func buildRequest(ctx context.Context, config RequestConfig) (*http.Request, error) {
	target, err := url.Parse(strings.TrimSpace(config.URL))
	if err != nil {
		return nil, err
	}
	query := target.Query()
	for key, value := range config.Query {
		query.Set(key, value)
	}
	target.RawQuery = query.Encode()

	var body io.Reader
	switch canonicalBodyType(config.BodyType) {
	case BodyJSON, BodyRaw:
		body = strings.NewReader(config.Body)
	case BodyForm:
		values := make(url.Values, len(config.Form))
		for key, value := range config.Form {
			values.Set(key, value)
		}
		body = strings.NewReader(values.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, canonicalMethod(config.Method), target.String(), body)
	if err != nil {
		return nil, err
	}
	for key, value := range config.Headers {
		if strings.EqualFold(key, "Host") {
			req.Host = value
			continue
		}
		req.Header.Set(key, value)
	}
	if req.Header.Get("Content-Type") == "" {
		switch canonicalBodyType(config.BodyType) {
		case BodyJSON:
			req.Header.Set("Content-Type", "application/json")
		case BodyForm:
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		case BodyRaw:
			req.Header.Set("Content-Type", "text/plain; charset=utf-8")
		}
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	return req, nil
}

func buildHTTPClient(config RequestConfig) (*http.Client, *http.Transport, error) {
	baseDialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           baseDialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		MaxIdleConnsPerHost:   5,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		ResponseHeaderTimeout: time.Duration(effectiveTimeout(config.TimeoutSeconds)) * time.Second,
	}

	if rawProxy := strings.TrimSpace(config.ProxyURL); rawProxy != "" {
		proxyURL, err := url.Parse(rawProxy)
		if err != nil {
			return nil, nil, err
		}
		switch strings.ToLower(proxyURL.Scheme) {
		case "http", "https":
			transport.Proxy = http.ProxyURL(proxyURL)
		case "socks5", "socks5h":
			clone := *proxyURL
			clone.Scheme = "socks5"
			dialer, err := proxy.FromURL(&clone, baseDialer)
			if err != nil {
				return nil, nil, err
			}
			transport.Proxy = nil
			if contextDialer, ok := dialer.(proxy.ContextDialer); ok {
				transport.DialContext = contextDialer.DialContext
			} else {
				transport.DialContext = func(_ context.Context, network, address string) (net.Conn, error) {
					return dialer.Dial(network, address)
				}
			}
		}
	}

	redirectLimit := effectiveMaxRedirects(config.MaxRedirects)
	client := &http.Client{
		Transport: transport,
		Timeout:   time.Duration(effectiveTimeout(config.TimeoutSeconds)) * time.Second,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) > redirectLimit {
				return fmt.Errorf("redirect limit of %d exceeded", redirectLimit)
			}
			return nil
		},
	}
	return client, transport, nil
}

// Execute sends a bounded request and decodes exactly one JSON value.
func Execute(ctx context.Context, config RequestConfig) (response Response, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err = ValidateRequestConfig(config); err != nil {
		return Response{}, err
	}
	req, err := buildRequest(ctx, config)
	if err != nil {
		return Response{}, RedactError(fmt.Errorf("build keyword source request: %w", err), config)
	}
	client, transport, err := buildHTTPClient(config)
	if err != nil {
		return Response{}, RedactError(fmt.Errorf("configure keyword source client: %w", err), config)
	}
	defer transport.CloseIdleConnections()

	started := time.Now()
	httpResponse, err := client.Do(req)
	response.Duration = time.Since(started)
	if err != nil {
		return response, RedactError(fmt.Errorf("request %s failed: %w", sanitizeURL(config.URL), err), config)
	}
	defer httpResponse.Body.Close()
	response.StatusCode = httpResponse.StatusCode
	response.ContentType = httpResponse.Header.Get("Content-Type")

	body, err := io.ReadAll(io.LimitReader(httpResponse.Body, MaxResponseBytes+1))
	response.SizeBytes = len(body)
	if err != nil {
		return response, RedactError(fmt.Errorf("read keyword source response: %w", err), config)
	}
	if len(body) > MaxResponseBytes {
		response.SizeBytes = MaxResponseBytes
		return response, ErrResponseTooLarge
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&response.JSON); err != nil {
		return response, fmt.Errorf("%w: %v", ErrInvalidJSON, compactJSONError(err))
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return response, fmt.Errorf("%w: multiple JSON values", ErrInvalidJSON)
	}
	if httpResponse.StatusCode < http.StatusOK || httpResponse.StatusCode >= http.StatusMultipleChoices {
		return response, &HTTPStatusError{StatusCode: httpResponse.StatusCode}
	}
	return response, nil
}

func compactJSONError(err error) string {
	var syntaxError *json.SyntaxError
	if errors.As(err, &syntaxError) {
		return fmt.Sprintf("syntax error at byte %d", syntaxError.Offset)
	}
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &typeError) {
		return fmt.Sprintf("type error at byte %d", typeError.Offset)
	}
	if errors.Is(err, io.EOF) {
		return "empty response"
	}
	return "decode failed"
}

// Test combines execution, field discovery and an optional extraction preview.
func Test(ctx context.Context, config RequestConfig, responsePath string) (TestResult, error) {
	response, err := Execute(ctx, config)
	result := TestResult{Response: response}
	if err != nil {
		return result, err
	}
	result.Candidates = DiscoverFields(response.JSON)
	if strings.TrimSpace(responsePath) != "" {
		extraction, err := ExtractKeywords(response.JSON, responsePath)
		if err != nil {
			return result, err
		}
		result.Extraction = &extraction
	}
	return result, nil
}
