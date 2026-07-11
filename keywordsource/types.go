// Package keywordsource executes administrator-configured HTTP JSON sources
// and extracts normalized keyword values through a deliberately small path
// language. It has no storage or API dependencies so callers can reuse it for
// live tests and scheduled synchronization.
package keywordsource

import (
	"errors"
	"time"
)

const (
	DefaultTimeoutSeconds = 15
	MinTimeoutSeconds     = 1
	MaxTimeoutSeconds     = 60
	DefaultMaxRedirects   = 5
	MaxRedirects          = 10
	MaxResponseBytes      = 2 << 20
)

var (
	ErrInvalidConfig    = errors.New("invalid keyword source configuration")
	ErrResponseTooLarge = errors.New("keyword source response exceeds 2 MiB")
	ErrInvalidJSON      = errors.New("keyword source response is not valid JSON")
	ErrInvalidPath      = errors.New("invalid JSON extraction path")
	ErrObjectResult     = errors.New("extraction path resolves to an object")
)

type BodyType string

const (
	BodyNone BodyType = "none"
	BodyJSON BodyType = "json"
	BodyForm BodyType = "form"
	BodyRaw  BodyType = "raw"
)

// RequestConfig is the complete, persistence-neutral HTTP request definition.
// TimeoutSeconds and MaxRedirects use their documented defaults when zero.
type RequestConfig struct {
	Method         string            `json:"method"`
	URL            string            `json:"url"`
	Headers        map[string]string `json:"headers,omitempty"`
	Query          map[string]string `json:"query,omitempty"`
	BodyType       BodyType          `json:"body_type"`
	Body           string            `json:"body,omitempty"`
	Form           map[string]string `json:"form,omitempty"`
	ProxyURL       string            `json:"proxy_url,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	MaxRedirects   int               `json:"max_redirects,omitempty"`
}

// Response contains the bounded decoded response. Raw bytes are intentionally
// omitted to discourage accidental logging of complete upstream payloads.
type Response struct {
	StatusCode  int           `json:"status_code"`
	Duration    time.Duration `json:"duration"`
	SizeBytes   int           `json:"size_bytes"`
	ContentType string        `json:"content_type,omitempty"`
	JSON        any           `json:"json"`
}

// KeywordValue keeps the source value for relationship/audit storage and its
// normalized identity for deduplication.
type KeywordValue struct {
	Value      string `json:"value"`
	Normalized string `json:"normalized"`
}

type ExtractionResult struct {
	Path        string         `json:"path"`
	RawCount    int            `json:"raw_count"`
	UniqueCount int            `json:"unique_count"`
	Values      []KeywordValue `json:"values"`
}

// FieldCandidate is a scalar leaf that can be selected by the response-path
// UI. Paths containing [] aggregate values across array elements.
type FieldCandidate struct {
	Path    string   `json:"path"`
	Kind    string   `json:"kind"`
	Count   int      `json:"count"`
	Samples []string `json:"samples,omitempty"`
}

type TestResult struct {
	Response
	Candidates []FieldCandidate  `json:"candidates"`
	Extraction *ExtractionResult `json:"extraction,omitempty"`
}
