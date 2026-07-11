package keywordsource

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type SegmentKind uint8

const (
	SegmentField SegmentKind = iota + 1
	SegmentIndex
	SegmentWildcard
)

type PathSegment struct {
	Kind  SegmentKind
	Field string
	Index int
}

// ParsePath parses field access, non-negative array indexes and [] wildcards.
// Examples: data.keyword, data.items[0].name and data.items[].meta.keyword.
func ParsePath(path string) ([]PathSegment, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("%w: path is empty", ErrInvalidPath)
	}
	segments := make([]PathSegment, 0, 6)
	for i := 0; i < len(path); {
		if path[i] == '.' {
			return nil, fmt.Errorf("%w: empty field at byte %d", ErrInvalidPath, i)
		}
		if path[i] != '[' {
			start := i
			for i < len(path) && path[i] != '.' && path[i] != '[' {
				i++
			}
			field := strings.TrimSpace(path[start:i])
			if field == "" || strings.IndexFunc(field, unicode.IsSpace) >= 0 {
				return nil, fmt.Errorf("%w: invalid field at byte %d", ErrInvalidPath, start)
			}
			segments = append(segments, PathSegment{Kind: SegmentField, Field: field})
		}

		for i < len(path) && path[i] == '[' {
			end := strings.IndexByte(path[i:], ']')
			if end < 0 {
				return nil, fmt.Errorf("%w: unclosed array selector at byte %d", ErrInvalidPath, i)
			}
			end += i
			selector := strings.TrimSpace(path[i+1 : end])
			if selector == "" {
				segments = append(segments, PathSegment{Kind: SegmentWildcard})
			} else {
				index, err := strconv.Atoi(selector)
				if err != nil || index < 0 {
					return nil, fmt.Errorf("%w: invalid array index %q", ErrInvalidPath, selector)
				}
				segments = append(segments, PathSegment{Kind: SegmentIndex, Index: index})
			}
			i = end + 1
		}

		if i == len(path) {
			break
		}
		if path[i] != '.' {
			return nil, fmt.Errorf("%w: unexpected character at byte %d", ErrInvalidPath, i)
		}
		i++
		if i == len(path) || path[i] == '.' || path[i] == '[' {
			return nil, fmt.Errorf("%w: missing field at byte %d", ErrInvalidPath, i)
		}
	}
	if len(segments) == 0 {
		return nil, fmt.Errorf("%w: path has no segments", ErrInvalidPath)
	}
	return segments, nil
}

// Extract resolves path and converts terminal scalar values to text. Terminal
// arrays are flattened; terminal objects require the caller to choose a field.
func Extract(document any, path string) ([]string, error) {
	segments, err := ParsePath(path)
	if err != nil {
		return nil, err
	}
	nodes := []any{document}
	for _, segment := range segments {
		next := make([]any, 0, len(nodes))
		for _, node := range nodes {
			if node == nil {
				continue
			}
			switch segment.Kind {
			case SegmentField:
				object, ok := node.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("%w: field %q requires an object", ErrInvalidPath, segment.Field)
				}
				if value, exists := object[segment.Field]; exists {
					next = append(next, value)
				}
			case SegmentIndex:
				array, ok := node.([]any)
				if !ok {
					return nil, fmt.Errorf("%w: index %d requires an array", ErrInvalidPath, segment.Index)
				}
				if segment.Index < len(array) {
					next = append(next, array[segment.Index])
				}
			case SegmentWildcard:
				array, ok := node.([]any)
				if !ok {
					return nil, fmt.Errorf("%w: [] requires an array", ErrInvalidPath)
				}
				next = append(next, array...)
			}
		}
		nodes = next
	}

	values := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if err := appendScalarValues(&values, node); err != nil {
			return nil, err
		}
	}
	return values, nil
}

func appendScalarValues(values *[]string, node any) error {
	switch value := node.(type) {
	case nil:
		return nil
	case []any:
		for _, item := range value {
			if err := appendScalarValues(values, item); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		return ErrObjectResult
	default:
		text, ok := scalarText(value)
		if !ok {
			return fmt.Errorf("%w: unsupported terminal type %T", ErrObjectResult, value)
		}
		*values = append(*values, text)
		return nil
	}
}

func scalarText(value any) (string, bool) {
	switch value := value.(type) {
	case string:
		return value, true
	case json.Number:
		return value.String(), true
	case bool:
		return strconv.FormatBool(value), true
	case float64:
		return strconv.FormatFloat(value, 'g', -1, 64), true
	case float32:
		return strconv.FormatFloat(float64(value), 'g', -1, 32), true
	case int:
		return strconv.Itoa(value), true
	case int8:
		return strconv.FormatInt(int64(value), 10), true
	case int16:
		return strconv.FormatInt(int64(value), 10), true
	case int32:
		return strconv.FormatInt(int64(value), 10), true
	case int64:
		return strconv.FormatInt(value, 10), true
	case uint:
		return strconv.FormatUint(uint64(value), 10), true
	case uint8:
		return strconv.FormatUint(uint64(value), 10), true
	case uint16:
		return strconv.FormatUint(uint64(value), 10), true
	case uint32:
		return strconv.FormatUint(uint64(value), 10), true
	case uint64:
		return strconv.FormatUint(value, 10), true
	default:
		return "", false
	}
}

func NormalizeKeyword(value string) string {
	fields := strings.FieldsFunc(strings.TrimSpace(value), unicode.IsSpace)
	return strings.ToLower(strings.Join(fields, " "))
}

func ExtractKeywords(document any, path string) (ExtractionResult, error) {
	raw, err := Extract(document, path)
	if err != nil {
		return ExtractionResult{}, err
	}
	result := ExtractionResult{Path: strings.TrimSpace(path), RawCount: len(raw)}
	seen := make(map[string]struct{}, len(raw))
	for _, value := range raw {
		value = strings.TrimSpace(value)
		normalized := NormalizeKeyword(value)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		result.Values = append(result.Values, KeywordValue{Value: value, Normalized: normalized})
	}
	result.UniqueCount = len(result.Values)
	return result, nil
}

type candidateAccumulator struct {
	count   int
	samples []string
	seen    map[string]struct{}
}

// DiscoverFields returns deterministic scalar-leaf candidates for the response
// path picker. Samples are capped to three distinct values per path.
func DiscoverFields(document any) []FieldCandidate {
	accumulators := make(map[string]*candidateAccumulator)
	walkCandidates(document, "", 0, accumulators)
	paths := make([]string, 0, len(accumulators))
	for path := range accumulators {
		if path != "" {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	result := make([]FieldCandidate, 0, len(paths))
	for _, path := range paths {
		item := accumulators[path]
		kind := "scalar"
		if strings.Contains(path, "[]") {
			kind = "array"
		}
		result = append(result, FieldCandidate{Path: path, Kind: kind, Count: item.count, Samples: item.samples})
	}
	return result
}

func walkCandidates(node any, path string, depth int, result map[string]*candidateAccumulator) {
	if depth > 24 || len(result) >= 512 {
		return
	}
	switch value := node.(type) {
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			walkCandidates(value[key], childPath, depth+1, result)
		}
	case []any:
		arrayPath := path + "[]"
		for _, item := range value {
			walkCandidates(item, arrayPath, depth+1, result)
		}
	case nil:
		return
	default:
		text, ok := scalarText(value)
		if !ok || path == "" {
			return
		}
		item := result[path]
		if item == nil {
			item = &candidateAccumulator{seen: make(map[string]struct{})}
			result[path] = item
		}
		item.count++
		if len(item.samples) < 3 {
			if _, exists := item.seen[text]; !exists {
				item.seen[text] = struct{}{}
				item.samples = append(item.samples, text)
			}
		}
	}
}
