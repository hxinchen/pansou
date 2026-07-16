package credential

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"pansou/storage"
)

func TestRefreshReplacesSecretAndPreservesCredentialMetadata(t *testing.T) {
	repository := &metadataRepository{}
	cipher, err := NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(repository, cipher)
	expires := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Microsecond)
	created, err := service.Create(context.Background(), CreateInput{
		PluginKey: "gying", Scope: storage.CredentialScopePublicShared, DisplayName: "Gying account",
		PublicMetadata: map[string]any{"account_hint": "user", "keep": "value"},
		Secret:         []byte(`{"username":"user","password":"pass","cookie":"old"}`), StableID: []byte("user"),
		ConfigBinding: []byte("https://example.test"), Status: storage.CredentialStatusActive, ExpiresAt: &expires,
	})
	if err != nil {
		t.Fatal(err)
	}
	newSecret := []byte(`{"username":"user","password":"pass","cookie":"new"}`)
	if err := service.Refresh(context.Background(), created.PublicID, LoginMaterial{
		Secret: newSecret, StableID: []byte("user"), ConfigBinding: []byte("https://example.test"),
	}); err != nil {
		t.Fatal(err)
	}
	if repository.current.Revision != created.Revision+1 {
		t.Fatalf("revision = %d, want %d", repository.current.Revision, created.Revision+1)
	}
	if repository.current.DisplayName != created.DisplayName || repository.current.PublicMetadata["keep"] != "value" {
		t.Fatalf("credential metadata changed: %#v", repository.current)
	}
	plaintext, err := service.OpenStored(repository.current)
	if err != nil {
		t.Fatal(err)
	}
	if string(plaintext) != `{"username":"user","password":"pass","cookie":"new"}` {
		t.Fatalf("refreshed secret = %q", plaintext)
	}
	for _, value := range newSecret {
		if value != 0 {
			t.Fatal("caller secret was not cleared")
		}
	}
}
