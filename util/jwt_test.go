package util

import (
	"testing"
	"time"
)

func TestGenerateUserTokenRoundTrip(t *testing.T) {
	identity := TokenIdentity{
		UserID:      42,
		Username:    "alice",
		Role:        "admin",
		AuthVersion: 7,
	}
	token, err := GenerateUserToken(identity, "test-secret", time.Hour)
	if err != nil {
		t.Fatalf("GenerateUserToken() error = %v", err)
	}
	claims, err := ValidateToken(token, "test-secret")
	if err != nil {
		t.Fatalf("ValidateToken() error = %v", err)
	}
	if claims.UserID != identity.UserID || claims.Username != identity.Username || claims.Role != identity.Role || claims.AuthVersion != identity.AuthVersion {
		t.Fatalf("claims = %#v, want identity %#v", claims, identity)
	}
	if claims.Subject != identity.Username || claims.Issuer != "pansou" {
		t.Fatalf("registered claims = %#v", claims.RegisteredClaims)
	}
}

func TestGenerateTokenLegacyCompatibility(t *testing.T) {
	token, err := GenerateToken("legacy", "test-secret", time.Hour)
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}
	claims, err := ValidateToken(token, "test-secret")
	if err != nil {
		t.Fatalf("ValidateToken() error = %v", err)
	}
	if claims.Username != "legacy" || claims.UserID != 0 {
		t.Fatalf("claims = %#v", claims)
	}
}

func TestGenerateUserTokenRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name     string
		identity TokenIdentity
		secret   string
		expiry   time.Duration
	}{
		{name: "empty username", secret: "secret", expiry: time.Hour},
		{name: "empty secret", identity: TokenIdentity{Username: "alice"}, expiry: time.Hour},
		{name: "invalid expiry", identity: TokenIdentity{Username: "alice"}, secret: "secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := GenerateUserToken(tt.identity, tt.secret, tt.expiry); err == nil {
				t.Fatal("GenerateUserToken() error = nil")
			}
		})
	}
}
