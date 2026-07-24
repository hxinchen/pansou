package sourceconfig

import "testing"

func TestRealtimeChannelsUsesExplicitTiers(t *testing.T) {
	snapshot := NewSnapshot(1, Config{Channels: []Channel{
		{Key: "fast", Enabled: true, Tier: "realtime"},
		{Key: "archive", Enabled: true, Tier: "collection"},
		{Key: "disabled", Enabled: false, Tier: "realtime"},
	}}, nil)
	channels := snapshot.RealtimeChannels()
	if len(channels) != 1 || channels[0] != "fast" {
		t.Fatalf("channels=%v", channels)
	}
	if all := snapshot.Channels(); len(all) != 2 {
		t.Fatalf("all channels=%v", all)
	}
}

func TestRealtimeChannelsColdStartCapsAtThirty(t *testing.T) {
	config := Config{Channels: make([]Channel, 35)}
	for i := range config.Channels {
		config.Channels[i] = Channel{Key: string(rune('a' + i)), Enabled: true}
	}
	if got := len(NewSnapshot(1, config, nil).RealtimeChannels()); got != 30 {
		t.Fatalf("got=%d", got)
	}
}

func TestPluginNamesOrdersTiersBeforeConfiguredOrder(t *testing.T) {
	snapshot := NewSnapshot(1, Config{AsyncPluginsEnabled: true, Plugins: map[string]PluginConfig{
		"deep":      {Enabled: true, Order: 0, Tier: "deep"},
		"primary":   {Enabled: true, Order: 9, Tier: "primary"},
		"secondary": {Enabled: true, Order: 1, Tier: "secondary"},
	}}, nil)
	got := snapshot.PluginNames()
	want := []string{"primary", "secondary", "deep"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got=%v", got)
		}
	}
}
