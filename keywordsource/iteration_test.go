package keywordsource

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestIterationValues(t *testing.T) {
	t.Parallel()
	values, err := IterationValues(IterationConfig{Enabled: true, Start: 0, Step: 20, Count: 10})
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{0, 20, 40, 60, 80, 100, 120, 140, 160, 180}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("values = %#v, want %#v", values, want)
	}
	values, err = IterationValues(IterationConfig{Enabled: true, Start: 5, Step: -2, Count: 4})
	if err != nil || !reflect.DeepEqual(values, []int64{5, 3, 1, -1}) {
		t.Fatalf("negative values = %#v, %v", values, err)
	}
	if _, err := IterationValues(IterationConfig{Enabled: true, Start: math.MaxInt64, Step: 1, Count: 2}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("overflow error = %v", err)
	}
	if _, err := IterationValues(IterationConfig{Enabled: true, Unlimited: true, Count: 1}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("unlimited materialization error = %v", err)
	}
}

func TestIterationValueSupportsUnlimitedIndexes(t *testing.T) {
	t.Parallel()
	iteration := IterationConfig{Enabled: true, Unlimited: true, Start: 5, Step: 3}
	value, err := IterationValue(iteration, 1_000)
	if err != nil || value != 3_005 {
		t.Fatalf("IterationValue = %d, %v", value, err)
	}
	if _, err := IterationValue(iteration, -1); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("negative index error = %v", err)
	}
	if _, err := IterationValue(IterationConfig{Enabled: true, Count: 2}, 2); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("finite out-of-range error = %v", err)
	}
	if _, err := IterationValue(IterationConfig{Enabled: true, Unlimited: true, Start: math.MaxInt64, Step: 1}, 1); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("unlimited overflow error = %v", err)
	}
}

func TestValidateIterationConfig(t *testing.T) {
	t.Parallel()
	base := RequestConfig{BodyType: BodyJSON, Body: `{"pagination":{"offset":0}}`}
	valid := []IterationConfig{
		{Enabled: true, Location: IterationQuery, Path: "page", Count: 1},
		{Enabled: true, Location: IterationHeader, Path: "X-Page", Count: 100, DelaySeconds: 3600},
		{Enabled: true, Location: IterationBody, Path: "pagination.offset", Count: 2},
		{Enabled: true, Location: IterationQuery, Path: "page", Count: 1, Unlimited: true, NoKeywordStopCount: 1, RandomDelayMinSeconds: -3600, RandomDelayMaxSeconds: 3600},
		{Enabled: true, Location: IterationQuery, Path: "page", Count: 1, NoKeywordStopCount: 100, RandomDelayMinSeconds: -2, RandomDelayMaxSeconds: -1},
	}
	for _, iteration := range valid {
		if err := ValidateIterationConfig(base, iteration); err != nil {
			t.Fatalf("valid iteration %#v: %v", iteration, err)
		}
	}
	invalid := []IterationConfig{
		{Enabled: true, Location: IterationQuery, Path: "", Count: 1},
		{Enabled: true, Location: "cookie", Path: "page", Count: 1},
		{Enabled: true, Location: IterationHeader, Path: "Bad Header", Count: 1},
		{Enabled: true, Location: IterationQuery, Path: "page", Count: 0},
		{Enabled: true, Location: IterationQuery, Path: "page", Count: 101},
		{Enabled: true, Location: IterationQuery, Path: "page", Count: 1, DelaySeconds: 3601},
		{Enabled: true, Location: IterationQuery, Path: "page", Count: 1, NoKeywordStopCount: -1},
		{Enabled: true, Location: IterationQuery, Path: "page", Count: 1, NoKeywordStopCount: 101},
		{Enabled: true, Location: IterationQuery, Path: "page", Count: 1, Unlimited: true},
		{Enabled: true, Location: IterationQuery, Path: "page", Count: 1, RandomDelayMinSeconds: -3601},
		{Enabled: true, Location: IterationQuery, Path: "page", Count: 1, RandomDelayMaxSeconds: 3601},
		{Enabled: true, Location: IterationQuery, Path: "page", Count: 1, RandomDelayMinSeconds: 2, RandomDelayMaxSeconds: 1},
		{Enabled: true, Location: IterationBody, Path: "missing.offset", Count: 1},
		{Enabled: true, Location: IterationBody, Path: "pagination[0]", Count: 1},
	}
	for _, iteration := range invalid {
		if err := ValidateIterationConfig(base, iteration); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("invalid iteration %#v error = %v", iteration, err)
		}
	}
	for _, bodyType := range []BodyType{BodyNone, BodyRaw} {
		request := RequestConfig{BodyType: bodyType, Body: `{}`}
		err := ValidateIterationConfig(request, IterationConfig{Enabled: true, Location: IterationBody, Path: "page", Count: 1})
		if !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("body type %q error = %v", bodyType, err)
		}
	}
}

func TestDeriveRequestInjectsQueryAndHeaderWithoutMutatingBase(t *testing.T) {
	t.Parallel()
	base := RequestConfig{Query: map[string]string{"page": "old"}, Headers: map[string]string{"X-Page": "old"}}
	query, value, err := DeriveRequest(base, IterationConfig{Enabled: true, Location: IterationQuery, Path: "page", Start: 10, Step: 5, Count: 3}, 2)
	if err != nil || value != 20 || query.Query["page"] != "20" || base.Query["page"] != "old" {
		t.Fatalf("query derived = %#v value=%d err=%v base=%#v", query, value, err, base)
	}
	header, value, err := DeriveRequest(base, IterationConfig{Enabled: true, Location: IterationHeader, Path: "x-page", Start: -2, Step: -3, Count: 2}, 1)
	if err != nil || value != -5 || header.Headers["X-Page"] != "-5" || base.Headers["X-Page"] != "old" {
		t.Fatalf("header derived = %#v value=%d err=%v base=%#v", header, value, err, base)
	}
}

func TestDeriveRequestInjectsNestedJSONBody(t *testing.T) {
	t.Parallel()
	base := RequestConfig{BodyType: BodyJSON, Body: `{"pagination":{"offset":0,"limit":20},"uid":9007199254740993}`}
	derived, value, err := DeriveRequest(base, IterationConfig{Enabled: true, Location: IterationBody, Path: "pagination.offset", Start: 40, Step: 20, Count: 2}, 1)
	if err != nil || value != 60 || base.Body == derived.Body {
		t.Fatalf("derived body = %q value=%d err=%v", derived.Body, value, err)
	}
	decoder := json.NewDecoder(strings.NewReader(derived.Body))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}
	pagination := document["pagination"].(map[string]any)
	if pagination["offset"].(json.Number).String() != "60" || document["uid"].(json.Number).String() != "9007199254740993" {
		t.Fatalf("document = %#v", document)
	}
}

func TestDeriveRequestInjectsFormAndDisabledIsUnchanged(t *testing.T) {
	t.Parallel()
	base := RequestConfig{BodyType: BodyForm, Form: map[string]string{"page": "old"}}
	derived, value, err := DeriveRequest(base, IterationConfig{Enabled: true, Location: IterationBody, Path: "page", Start: 1, Step: 1, Count: 2}, 1)
	if err != nil || value != 2 || derived.Form["page"] != "2" || base.Form["page"] != "old" {
		t.Fatalf("form derived = %#v value=%d err=%v", derived, value, err)
	}
	disabled, _, err := DeriveRequest(base, IterationConfig{Enabled: false, Location: "invalid", Path: "", Start: 7}, 0)
	if err != nil || disabled.Form["page"] != "old" {
		t.Fatalf("disabled derived = %#v err=%v", disabled, err)
	}
	disabled.Form["page"] = "changed"
	if base.Form["page"] != "old" {
		t.Fatal("disabled derivation aliased base map")
	}
}

func TestDeriveRequestRejectsOutOfRangeIndex(t *testing.T) {
	t.Parallel()
	base := RequestConfig{}
	iteration := IterationConfig{Enabled: true, Location: IterationQuery, Path: "page", Count: 2}
	for _, index := range []int{-1, 2} {
		if _, _, err := DeriveRequest(base, iteration, index); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("index %d error = %v", index, err)
		}
	}
}

func TestDeriveRequestAllowsUnlimitedIndex(t *testing.T) {
	t.Parallel()
	base := RequestConfig{Query: map[string]string{"page": "old"}}
	iteration := IterationConfig{
		Enabled: true, Unlimited: true, NoKeywordStopCount: 2,
		Location: IterationQuery, Path: "page", Start: 1, Step: 2, Count: 1,
	}
	derived, value, err := DeriveRequest(base, iteration, 150)
	if err != nil || value != 301 || derived.Query["page"] != "301" || base.Query["page"] != "old" {
		t.Fatalf("derived = %#v value=%d err=%v base=%#v", derived, value, err, base)
	}
}

func TestIterationConfigJSONFields(t *testing.T) {
	t.Parallel()
	payload, err := json.Marshal(IterationConfig{
		Unlimited: true, NoKeywordStopCount: 3,
		RandomDelayMinSeconds: -2, RandomDelayMaxSeconds: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(payload)
	for _, field := range []string{`"unlimited":true`, `"no_keyword_stop_count":3`, `"random_delay_min_seconds":-2`, `"random_delay_max_seconds":4`} {
		if !strings.Contains(encoded, field) {
			t.Fatalf("JSON %s does not contain %s", encoded, field)
		}
	}
}
