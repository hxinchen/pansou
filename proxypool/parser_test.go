package proxypool

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestParseLine(t *testing.T) {
	parsed, err := ParseLine("http://user:pass@8.8.8.8:8080")
	if err != nil {
		t.Fatalf("ParseLine: %v", err)
	}
	if parsed.Canonical != "http://user:pass@8.8.8.8:8080" || parsed.DisplayURL != "http://user:***@8.8.8.8:8080" || !parsed.HasAuth {
		t.Fatalf("parsed proxy = %+v", parsed)
	}
	if empty, err := ParseLine("  # comment"); err != nil || empty.Canonical != "" {
		t.Fatalf("comment = %+v, err=%v", empty, err)
	}
}

func TestParseLineRejectsPrivateAndUnsupported(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:8080", "http://10.0.0.1:8080", "ftp://8.8.8.8:21", "http://8.8.8.8"} {
		if _, err := ParseLine(raw); err == nil {
			t.Fatalf("ParseLine(%q) succeeded", raw)
		}
	}
}

func TestCipherRoundTripAndDomainSeparation(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	cipher, err := NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	ciphertext, nonce, fingerprint, err := cipher.Encrypt("http://user:pass@8.8.8.8:8080")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	value, err := cipher.Decrypt(ciphertext, nonce, fingerprint)
	if err != nil || value != "http://user:pass@8.8.8.8:8080" {
		t.Fatalf("Decrypt = %q, err=%v", value, err)
	}
	if strings.EqualFold(string(cipher.Fingerprint([]byte("one"))), string(cipher.Fingerprint([]byte("two")))) {
		t.Fatal("fingerprints unexpectedly match")
	}
}
