package storage

import (
	"errors"
	"reflect"
	"testing"
)

func TestNormalizeLinkCheckPolicyCanonicalizesStatuses(t *testing.T) {
	policy, err := normalizeLinkCheckPolicy(true, []string{
		" violation ", "VALID", "unknown", "valid", "cancelled", "expired", "invalid", "locked",
	}, DefaultLinkCheckIntervalSeconds)
	if err != nil {
		t.Fatalf("normalizeLinkCheckPolicy: %v", err)
	}
	want := []string{CheckValid, CheckUnknown, CheckInvalid, CheckExpired, CheckCancelled, CheckViolation, CheckLocked}
	if !reflect.DeepEqual(policy.Statuses, want) {
		t.Fatalf("statuses = %v, want %v", policy.Statuses, want)
	}
}

func TestNormalizeLinkCheckPolicyValidation(t *testing.T) {
	tests := []struct {
		name     string
		enabled  bool
		statuses []string
		interval int64
		wantErr  bool
	}{
		{name: "minimum", enabled: true, statuses: []string{CheckValid}, interval: MinLinkCheckIntervalSeconds},
		{name: "maximum", enabled: true, statuses: []string{CheckValid}, interval: MaxLinkCheckIntervalSeconds},
		{name: "disabled empty", enabled: false, statuses: nil, interval: DefaultLinkCheckIntervalSeconds},
		{name: "enabled empty", enabled: true, interval: DefaultLinkCheckIntervalSeconds, wantErr: true},
		{name: "below minimum", enabled: false, interval: MinLinkCheckIntervalSeconds - 1, wantErr: true},
		{name: "above maximum", enabled: false, interval: MaxLinkCheckIntervalSeconds + 3600, wantErr: true},
		{name: "partial hour", enabled: false, interval: MinLinkCheckIntervalSeconds + 1, wantErr: true},
		{name: "unsupported status", enabled: true, statuses: []string{CheckUnsupported}, interval: DefaultLinkCheckIntervalSeconds, wantErr: true},
		{name: "pending status", enabled: true, statuses: []string{CheckPending}, interval: DefaultLinkCheckIntervalSeconds, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeLinkCheckPolicy(test.enabled, test.statuses, test.interval)
			if test.wantErr && !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestDefinitiveNegativeCheckStatus(t *testing.T) {
	for _, status := range []string{CheckInvalid, CheckExpired, CheckCancelled, CheckViolation} {
		if !definitiveNegativeCheckStatus(status) {
			t.Errorf("%q should be definitive negative", status)
		}
	}
	for _, status := range []string{CheckPending, CheckValid, CheckUnknown, CheckLocked, CheckUnsupported} {
		if definitiveNegativeCheckStatus(status) {
			t.Errorf("%q should not be definitive negative", status)
		}
	}
}

func TestCheckStatusNeedsNegativeConfirmation(t *testing.T) {
	for _, status := range []string{CheckPending, CheckValid, CheckUnknown, CheckLocked} {
		if !checkStatusNeedsNegativeConfirmation(status) {
			t.Errorf("%q should require negative confirmation", status)
		}
	}
	for _, status := range []string{CheckInvalid, CheckExpired, CheckCancelled, CheckViolation, CheckUnsupported} {
		if checkStatusNeedsNegativeConfirmation(status) {
			t.Errorf("%q should not require negative confirmation", status)
		}
	}
}

func TestImmediateLinkCheckCandidatesAreBounded(t *testing.T) {
	summary := UpsertSummary{}
	for id := int64(1); id <= maxImmediateLinkCheckCandidates+1; id++ {
		addImmediateLinkCheckCandidate(&summary, Resource{ID: id, CheckStatus: CheckPending})
	}
	if len(summary.CheckCandidates) != maxImmediateLinkCheckCandidates {
		t.Fatalf("candidate count = %d, want %d", len(summary.CheckCandidates), maxImmediateLinkCheckCandidates)
	}
	if summary.CheckCandidates[len(summary.CheckCandidates)-1].ID != maxImmediateLinkCheckCandidates {
		t.Fatalf("last candidate ID = %d", summary.CheckCandidates[len(summary.CheckCandidates)-1].ID)
	}
	addImmediateLinkCheckCandidate(&summary, Resource{ID: 999, CheckStatus: CheckValid})
	if len(summary.CheckCandidates) != maxImmediateLinkCheckCandidates {
		t.Fatal("non-pending resource changed candidate count")
	}
}
