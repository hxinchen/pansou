package config

import (
	"reflect"
	"testing"
)

func TestParseTrustedProxies(t *testing.T) {
	got, err := ParseTrustedProxies(" 172.18.0.0/16,127.0.0.1,::1,172.18.0.0/16 ")
	if err != nil {
		t.Fatalf("ParseTrustedProxies: %v", err)
	}
	want := []string{"172.18.0.0/16", "127.0.0.1", "::1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("proxies = %#v, want %#v", got, want)
	}
}

func TestParseTrustedProxiesRejectsInvalidValue(t *testing.T) {
	if _, err := ParseTrustedProxies("172.18.0.0/16,not-a-network"); err == nil {
		t.Fatal("expected invalid proxy error")
	}
}

func TestMustTrustedProxiesAlwaysIncludesLoopback(t *testing.T) {
	got := mustTrustedProxies("192.168.16.0/20")
	want := []string{"192.168.16.0/20", "127.0.0.1", "::1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("proxies = %#v, want %#v", got, want)
	}

	got = mustTrustedProxies("127.0.0.1,::1")
	want = []string{"127.0.0.1", "::1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("deduplicated proxies = %#v, want %#v", got, want)
	}
}

func TestInvalidTrustedProxyFailsStartupParsing(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected invalid TRUSTED_PROXIES to panic during startup parsing")
		}
	}()
	_ = mustTrustedProxies("invalid-proxy")
}
