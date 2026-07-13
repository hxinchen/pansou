package tgchannel

import (
	"errors"
	"reflect"
	"testing"
)

func TestNormalize(t *testing.T) {
	tests := map[string]string{
		"Example_Channel":                 "example_channel",
		"@Example_Channel":                "example_channel",
		"https://t.me/Example_Channel":    "example_channel",
		"https://t.me/s/Example_Channel":  "example_channel",
		"telegram.me/Example_Channel/123": "example_channel",
	}
	for input, want := range tests {
		got, err := Normalize(input)
		if err != nil {
			t.Fatalf("Normalize(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("Normalize(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeListDeduplicates(t *testing.T) {
	got, err := NormalizeList([]string{"@Example_Channel", "https://t.me/example_channel", "Other_Channel"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"example_channel", "other_channel"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeList() = %v, want %v", got, want)
	}
}

func TestNormalizeRejectsNonPublicChannels(t *testing.T) {
	for _, input := range []string{"", "abc", "https://t.me/+invite", "bad-channel", "https://example.com/channel"} {
		if _, err := Normalize(input); !errors.Is(err, ErrInvalidChannel) {
			t.Fatalf("Normalize(%q) error = %v, want ErrInvalidChannel", input, err)
		}
	}
}
