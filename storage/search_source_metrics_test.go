package storage

import "testing"

func TestAssignSuggestedTiers(t *testing.T) {
	items := make([]SourceTierSuggestion, 35)
	for index := range items {
		items[index] = SourceTierSuggestion{Source: "tg:test", SourceType: "tg", Score: float64(35 - index), Eligible: true}
	}
	assignSuggestedTiers(items, "tg", 30, 0)
	realtime, collection := 0, 0
	for _, item := range items {
		if item.SuggestedTier == "realtime" {
			realtime++
		} else if item.SuggestedTier == "collection" {
			collection++
		}
	}
	if realtime != 30 || collection != 5 {
		t.Fatalf("realtime=%d collection=%d", realtime, collection)
	}
}

func TestSourceTierEligibility(t *testing.T) {
	cases := []struct {
		runs uint64
		days int
		want bool
	}{{99, 30, false}, {100, 13, false}, {100, 14, true}, {299, 7, false}, {300, 7, true}}
	for _, test := range cases {
		got, _ := sourceTierEligibility(test.runs, test.days)
		if got != test.want {
			t.Fatalf("eligibility(%d,%d)=%t want %t", test.runs, test.days, got, test.want)
		}
	}
}
