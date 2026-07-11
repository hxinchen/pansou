package keywordsource

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"golang.org/x/net/http/httpguts"
)

const (
	MinIterationCount        = 1
	MaxIterationCount        = 100
	MinIterationDelaySeconds = 0
	MaxIterationDelaySeconds = 3600
)

type IterationLocation string

const (
	IterationQuery  IterationLocation = "query"
	IterationHeader IterationLocation = "header"
	IterationBody   IterationLocation = "body"
)

// IterationConfig describes a persistence-neutral numeric request sequence.
// The value for an index is Start + Step*index.
type IterationConfig struct {
	Enabled      bool              `json:"enabled"`
	Location     IterationLocation `json:"location"`
	Path         string            `json:"path"`
	Start        int64             `json:"start"`
	Step         int64             `json:"step"`
	Count        int               `json:"count"`
	DelaySeconds int               `json:"delay_seconds"`
}

func canonicalIterationLocation(location IterationLocation) IterationLocation {
	if location == "" {
		return IterationQuery
	}
	return IterationLocation(strings.ToLower(strings.TrimSpace(string(location))))
}

// ValidateIterationConfig validates iteration rules against the base request.
// Disabled iteration is deliberately accepted unchanged for compatibility.
func ValidateIterationConfig(base RequestConfig, iteration IterationConfig) error {
	if !iteration.Enabled {
		return nil
	}
	if iteration.Count < MinIterationCount || iteration.Count > MaxIterationCount {
		return fmt.Errorf("%w: iteration count must be between 1 and 100", ErrInvalidConfig)
	}
	if iteration.DelaySeconds < MinIterationDelaySeconds || iteration.DelaySeconds > MaxIterationDelaySeconds {
		return fmt.Errorf("%w: iteration delay must be between 0 and 3600 seconds", ErrInvalidConfig)
	}
	path := strings.TrimSpace(iteration.Path)
	if path == "" {
		return fmt.Errorf("%w: iteration path is empty", ErrInvalidConfig)
	}

	switch canonicalIterationLocation(iteration.Location) {
	case IterationQuery:
		if strings.ContainsAny(path, "\r\n") {
			return fmt.Errorf("%w: invalid iteration query name", ErrInvalidConfig)
		}
	case IterationHeader:
		if !httpguts.ValidHeaderFieldName(path) {
			return fmt.Errorf("%w: invalid iteration header name", ErrInvalidConfig)
		}
	case IterationBody:
		switch canonicalBodyType(base.BodyType) {
		case BodyJSON:
			if _, err := parseIterationJSONPath(path); err != nil {
				return err
			}
			if err := validateJSONIterationTarget(base.Body, path); err != nil {
				return err
			}
		case BodyForm:
			if strings.ContainsAny(path, "\r\n") {
				return fmt.Errorf("%w: invalid iteration form field", ErrInvalidConfig)
			}
		default:
			return fmt.Errorf("%w: body iteration requires JSON or form body", ErrInvalidConfig)
		}
	default:
		return fmt.Errorf("%w: unsupported iteration location", ErrInvalidConfig)
	}

	_, err := IterationValues(iteration)
	return err
}

// IterationValues returns the complete numeric sequence. Disabled iteration
// represents the compatible single request and therefore returns Start once.
func IterationValues(iteration IterationConfig) ([]int64, error) {
	count := iteration.Count
	if !iteration.Enabled {
		count = 1
	} else if count < MinIterationCount || count > MaxIterationCount {
		return nil, fmt.Errorf("%w: iteration count must be between 1 and 100", ErrInvalidConfig)
	}
	values := make([]int64, count)
	for index := 0; index < count; index++ {
		value, err := iterationValue(iteration.Start, iteration.Step, index)
		if err != nil {
			return nil, err
		}
		values[index] = value
	}
	return values, nil
}

// DeriveRequest clones base and injects the value for index. When iteration is
// disabled only index zero is valid and the cloned request is not modified.
func DeriveRequest(base RequestConfig, iteration IterationConfig, index int) (RequestConfig, int64, error) {
	if err := ValidateIterationConfig(base, iteration); err != nil {
		return RequestConfig{}, 0, err
	}
	limit := iteration.Count
	if !iteration.Enabled {
		limit = 1
	}
	if index < 0 || index >= limit {
		return RequestConfig{}, 0, fmt.Errorf("%w: iteration index %d is out of range", ErrInvalidConfig, index)
	}
	value, err := iterationValue(iteration.Start, iteration.Step, index)
	if err != nil {
		return RequestConfig{}, 0, err
	}
	derived := cloneRequestConfig(base)
	if !iteration.Enabled {
		return derived, value, nil
	}
	text := strconv.FormatInt(value, 10)
	path := strings.TrimSpace(iteration.Path)
	switch canonicalIterationLocation(iteration.Location) {
	case IterationQuery:
		if derived.Query == nil {
			derived.Query = make(map[string]string)
		}
		derived.Query[path] = text
	case IterationHeader:
		if derived.Headers == nil {
			derived.Headers = make(map[string]string)
		}
		for key := range derived.Headers {
			if strings.EqualFold(key, path) {
				delete(derived.Headers, key)
			}
		}
		derived.Headers[http.CanonicalHeaderKey(path)] = text
	case IterationBody:
		switch canonicalBodyType(derived.BodyType) {
		case BodyJSON:
			derived.Body, err = injectJSONIterationValue(derived.Body, path, value)
		case BodyForm:
			if derived.Form == nil {
				derived.Form = make(map[string]string)
			}
			derived.Form[path] = text
		}
	}
	if err != nil {
		return RequestConfig{}, 0, err
	}
	return derived, value, nil
}

func iterationValue(start, step int64, index int) (int64, error) {
	if index < 0 {
		return 0, fmt.Errorf("%w: iteration index is negative", ErrInvalidConfig)
	}
	if index != 0 && (step > math.MaxInt64/int64(index) || step < math.MinInt64/int64(index)) {
		return 0, fmt.Errorf("%w: iteration value overflows int64", ErrInvalidConfig)
	}
	delta := step * int64(index)
	if (delta > 0 && start > math.MaxInt64-delta) || (delta < 0 && start < math.MinInt64-delta) {
		return 0, fmt.Errorf("%w: iteration value overflows int64", ErrInvalidConfig)
	}
	return start + delta, nil
}

func cloneRequestConfig(base RequestConfig) RequestConfig {
	clone := base
	clone.Headers = cloneStringMap(base.Headers)
	clone.Query = cloneStringMap(base.Query)
	clone.Form = cloneStringMap(base.Form)
	return clone
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func parseIterationJSONPath(path string) ([]string, error) {
	raw := strings.Split(strings.TrimSpace(path), ".")
	segments := make([]string, len(raw))
	for index, segment := range raw {
		segment = strings.TrimSpace(segment)
		if segment == "" || strings.ContainsAny(segment, "[]\r\n") {
			return nil, fmt.Errorf("%w: invalid JSON iteration path", ErrInvalidConfig)
		}
		segments[index] = segment
	}
	return segments, nil
}

func decodeJSONObject(body string) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil || document == nil {
		return nil, fmt.Errorf("%w: JSON iteration body must be an object", ErrInvalidConfig)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: JSON iteration body contains multiple or invalid values", ErrInvalidConfig)
	}
	return document, nil
}

func validateJSONIterationTarget(body, path string) error {
	document, err := decodeJSONObject(body)
	if err != nil {
		return err
	}
	segments, err := parseIterationJSONPath(path)
	if err != nil {
		return err
	}
	_, err = descendJSONObject(document, segments[:len(segments)-1])
	return err
}

func injectJSONIterationValue(body, path string, value int64) (string, error) {
	document, err := decodeJSONObject(body)
	if err != nil {
		return "", err
	}
	segments, err := parseIterationJSONPath(path)
	if err != nil {
		return "", err
	}
	parent, err := descendJSONObject(document, segments[:len(segments)-1])
	if err != nil {
		return "", err
	}
	parent[segments[len(segments)-1]] = value
	encoded, err := json.Marshal(document)
	if err != nil {
		return "", fmt.Errorf("%w: encode iterated JSON body: %v", ErrInvalidConfig, err)
	}
	return string(bytes.TrimSpace(encoded)), nil
}

func descendJSONObject(document map[string]any, segments []string) (map[string]any, error) {
	current := document
	for _, segment := range segments {
		next, exists := current[segment]
		if !exists {
			return nil, fmt.Errorf("%w: JSON iteration path object %q does not exist", ErrInvalidConfig, segment)
		}
		object, ok := next.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: JSON iteration path %q is not an object", ErrInvalidConfig, segment)
		}
		current = object
	}
	return current, nil
}
