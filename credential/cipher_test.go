package credential

import (
	"encoding/base64"
	"testing"
	"time"
)

func TestCipherRoundTripAndBinding(t *testing.T) {
	c, e := NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if e != nil {
		t.Fatal(e)
	}
	b := Binding{PublicID: "p", PluginKey: "qqpd", Scope: "user_private", SecretSchemaVersion: 1}
	x, e := c.Encrypt(b, []byte("secret"), []byte("account"))
	if e != nil {
		t.Fatal(e)
	}
	p, e := c.Decrypt(b, x)
	if e != nil || string(p) != "secret" {
		t.Fatalf("%q %v", p, e)
	}
	b.Scope = "public_shared"
	if _, e = c.Decrypt(b, x); e == nil {
		t.Fatal("expected binding failure")
	}
}
func TestFlowStore(t *testing.T) {
	s := NewFlowStore(time.Minute, 1, time.Second)
	f, e := s.Create(1, "qqpd", "user_private", nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Get(f.ID, 1); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Get(f.ID, 1); e != ErrRateLimited {
		t.Fatalf("got %v", e)
	}
	if _, e = s.Get(f.ID, 2); e != ErrFlowNotFound {
		t.Fatalf("got %v", e)
	}
}
