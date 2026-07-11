package storage

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestCredentialScopeOwnerValidation(t *testing.T) {
	t.Parallel()
	owner := int64(7)
	tests := []struct {
		name  string
		scope string
		owner *int64
		valid bool
	}{
		{name: "user private", scope: CredentialScopeUserPrivate, owner: &owner, valid: true},
		{name: "user private missing owner", scope: CredentialScopeUserPrivate, valid: false},
		{name: "admin private", scope: CredentialScopeAdminPrivate, valid: true},
		{name: "public shared", scope: CredentialScopePublicShared, valid: true},
		{name: "admin with owner", scope: CredentialScopeAdminPrivate, owner: &owner, valid: false},
		{name: "unknown", scope: "global", valid: false},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := validCredentialScopeOwner(test.scope, test.owner); got != test.valid {
				t.Fatalf("validCredentialScopeOwner(%q,%v) = %v", test.scope, test.owner, got)
			}
		})
	}
}

func TestPluginCredentialSecretsAreNeverJSONEncoded(t *testing.T) {
	t.Parallel()
	credential := PluginCredential{
		PublicID: "cred-public", Ciphertext: []byte("test-password"), Nonce: []byte("test-nonce"),
		BindingFingerprint: []byte("binding"), CredentialFingerprint: []byte("fingerprint"),
	}
	data, err := json.Marshal(credential)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, secret := range []string{"test-password", "test-nonce", "binding", "fingerprint"} {
		if containsJSONValue(data, secret) {
			t.Fatalf("credential JSON leaked %q: %s", secret, data)
		}
	}
}

func TestCredentialCandidateEligibility(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	tests := []struct {
		name       string
		credential PluginCredential
		want       bool
	}{
		{name: "active", credential: PluginCredential{OwnerEnabled: true, Status: CredentialStatusActive}, want: true},
		{name: "suspended", credential: PluginCredential{OwnerEnabled: true, Status: CredentialStatusActive, AdminSuspendedAt: &past}},
		{name: "owner disabled", credential: PluginCredential{Status: CredentialStatusActive}},
		{name: "invalid", credential: PluginCredential{OwnerEnabled: true, Status: CredentialStatusInvalid}},
		{name: "expired", credential: PluginCredential{OwnerEnabled: true, Status: CredentialStatusActive, ExpiresAt: &past}},
		{name: "cooldown", credential: PluginCredential{OwnerEnabled: true, Status: CredentialStatusActive, CooldownUntil: &future}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := test.credential.IsUsableAt(now); got != test.want {
				t.Fatalf("IsUsableAt() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestStrictJSONRejectsUnsupportedValues(t *testing.T) {
	t.Parallel()
	if _, err := marshalStrictJSON(map[string]any{"bad": make(chan int)}); err == nil {
		t.Fatal("marshalStrictJSON() accepted an unsupported value")
	}
	want := json.RawMessage(`{"enabled":true}`)
	got, err := marshalStrictJSON(map[string]any{"enabled": true})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("marshalStrictJSON() = %s, %v", got, err)
	}
}

func TestValidateRawJSONObject(t *testing.T) {
	t.Parallel()
	for _, value := range []json.RawMessage{nil, json.RawMessage(`[]`), json.RawMessage(`null`), json.RawMessage(`{"broken"`)} {
		if _, err := validateRawJSONObject(value); err == nil {
			t.Errorf("validateRawJSONObject(%s) accepted non-object", value)
		}
	}
	if _, err := validateRawJSONObject(json.RawMessage(`{"enabled":true}`)); err != nil {
		t.Fatalf("validateRawJSONObject(object) error = %v", err)
	}
}

func TestSourceConfigEventValidation(t *testing.T) {
	t.Parallel()
	resultVersion := int64(3)
	invalid := []CreateSearchSourceConfigEventInput{
		{BaseVersion: 2, Result: SourceConfigEventSuccess},
		{BaseVersion: 2, ResultVersion: &resultVersion, Result: SourceConfigEventSuccess, ErrorCode: "unexpected"},
		{BaseVersion: 2, Result: SourceConfigEventFailed},
	}
	for _, input := range invalid {
		if _, err := validateSearchSourceConfigEventInput(input); err == nil {
			t.Errorf("validateSearchSourceConfigEventInput(%+v) accepted invalid event", input)
		}
	}
}

func containsJSONValue(data []byte, value string) bool {
	var decoded map[string]any
	if json.Unmarshal(data, &decoded) != nil {
		return true
	}
	for _, candidate := range decoded {
		if text, ok := candidate.(string); ok && text == value {
			return true
		}
	}
	return false
}
