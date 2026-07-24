package api

import (
	"testing"

	"pansou/sourceconfig"
	"pansou/storage"
)

func TestApplySuggestedTiersOnlyUsesEligibleSources(t *testing.T) {
	config := sourceconfig.Config{
		Channels: []sourceconfig.Channel{{Key: "alpha", Tier: "collection"}, {Key: "beta", Tier: "realtime"}},
		Plugins:  map[string]sourceconfig.PluginConfig{"demo": {Tier: "deep"}},
	}
	suggestions := []storage.SourceTierSuggestion{
		{Source: "tg:alpha", SuggestedTier: "realtime", Eligible: true},
		{Source: "tg:beta", SuggestedTier: "collection", Eligible: false},
		{Source: "plugin:demo", SuggestedTier: "primary", Eligible: true},
	}
	updated, applied, eligible := applySuggestedTiers(config, suggestions)
	if applied != 2 || eligible != 2 {
		t.Fatalf("applied=%d eligible=%d", applied, eligible)
	}
	if updated.Channels[0].Tier != "realtime" || updated.Channels[1].Tier != "realtime" || updated.Plugins["demo"].Tier != "primary" {
		t.Fatalf("updated config=%+v", updated)
	}
}
