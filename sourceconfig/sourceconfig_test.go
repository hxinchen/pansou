package sourceconfig

import (
	"context"
	"testing"
	"time"

	"pansou/plugin"
)

func TestCatalogRejectsUnknownAndSecretFields(t *testing.T) {
	catalog, err := DefaultCatalog([]string{"gying"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = catalog.Validate(Config{Plugins: map[string]PluginConfig{"gying": {Enabled: true, Config: map[string]any{"password": "secret"}}}})
	if err == nil {
		t.Fatal("expected forbidden field error")
	}
	_, err = catalog.Validate(Config{Plugins: map[string]PluginConfig{"missing": {Enabled: true}}})
	if err == nil {
		t.Fatal("expected unknown plugin error")
	}
}

func TestCatalogNormalizesChannelsAndBaseURL(t *testing.T) {
	catalog, _ := DefaultCatalog([]string{"gying"})
	config, err := catalog.Validate(Config{Channels: []Channel{{Key: "https://t.me/s/Example_Channel", Enabled: true}}, Plugins: map[string]PluginConfig{"GYING": {Enabled: true, Config: map[string]any{"base_url": "https://example.test/path"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if config.Channels[0].Key != "example_channel" {
		t.Fatalf("channel = %q", config.Channels[0].Key)
	}
	if got := config.Plugins["gying"].Config["base_url"]; got != "https://example.test" {
		t.Fatalf("base_url = %v", got)
	}
}

func TestCatalogRejectsInvalidPublicChannel(t *testing.T) {
	catalog, _ := DefaultCatalog(nil)
	if _, err := catalog.Validate(Config{Channels: []Channel{{Key: "https://t.me/+private"}}}); err == nil {
		t.Fatal("expected invalid public channel error")
	}
}

func TestDefaultCatalogDescribesAisoupanAsTokenCredential(t *testing.T) {
	catalog, err := DefaultCatalog([]string{"aisoupan"})
	if err != nil {
		t.Fatal(err)
	}
	descriptors := catalog.Plugins()
	if len(descriptors) != 1 || !descriptors[0].RequiresAccount || descriptors[0].LoginType != "token" || len(descriptors[0].AllowedConfigKeys) != 0 {
		t.Fatalf("descriptors = %#v", descriptors)
	}
}

func TestManagerDefersRetiredSnapshotClose(t *testing.T) {
	first := NewSnapshot(1, Config{}, plugin.NewPluginManager())
	manager := NewManager(first, nil)
	lease, err := manager.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	second := NewSnapshot(2, Config{}, plugin.NewPluginManager())
	if err := manager.Publish(second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.done:
		t.Fatal("retired snapshot closed while leased")
	default:
	}
	lease.Release()
	select {
	case <-first.done:
	case <-time.After(time.Second):
		t.Fatal("retired snapshot not closed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
