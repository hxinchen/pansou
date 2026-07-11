package storage

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeURLRemovesCodeAndTracking(t *testing.T) {
	t.Parallel()
	got, err := NormalizeURL("HTTPS://Pan.Example.COM:443/Share/AbC/?pwd=1234&utm_source=test&keep=yes#code")
	if err != nil {
		t.Fatalf("NormalizeURL() error = %v", err)
	}
	want := "https://pan.example.com/Share/AbC?keep=yes"
	if got != want {
		t.Fatalf("NormalizeURL() = %q, want %q", got, want)
	}
	if code := ExtractionCode("https://pan.example/share/abc?pwd=1234"); code != "1234" {
		t.Fatalf("ExtractionCode() = %q, want 1234", code)
	}
}

func TestNormalizeURLRemovesAppendedMessageText(t *testing.T) {
	t.Parallel()
	raw := "https://pan.quark.cn/s/78dd96bb598e \n\n🏷标签：电影"
	got, err := NormalizeURL(raw)
	if err != nil {
		t.Fatalf("NormalizeURL() error = %v", err)
	}
	if want := "https://pan.quark.cn/s/78dd96bb598e"; got != want {
		t.Fatalf("NormalizeURL() = %q, want %q", got, want)
	}
	if cleaned := cleanURLInput(raw); cleaned != got {
		t.Fatalf("cleanURLInput() = %q, want %q", cleaned, got)
	}
}

func TestNormalizeKeywordAndCooldown(t *testing.T) {
	t.Parallel()
	if got := NormalizeKeyword("  Go\t语言\n教程  "); got != "go 语言 教程" {
		t.Fatalf("NormalizeKeyword() = %q", got)
	}
	at := time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC)
	override := int64(90)
	if got := NextEligibleAt(at, &override, 7*24*time.Hour); !got.Equal(at.Add(90 * time.Second)) {
		t.Fatalf("NextEligibleAt() = %v", got)
	}
}

func TestBuildResourceWhereIncludeUsesOR(t *testing.T) {
	t.Parallel()
	where, args := buildResourceWhere(ResourceFilter{Include: []string{"alpha", "beta"}, IncludeInvalid: true})
	if strings.Count(where, " ILIKE ") != 4 || !strings.Contains(where, " OR r.title ILIKE ") {
		t.Fatalf("include predicate is not OR-composed: %s", where)
	}
	if strings.Contains(where, ") AND (r.title ILIKE") {
		t.Fatalf("include predicates were AND-composed: %s", where)
	}
	if len(args) != 2 {
		t.Fatalf("len(args) = %d, want 2", len(args))
	}
}

func TestStatusValidators(t *testing.T) {
	t.Parallel()
	for _, status := range []string{CheckPending, CheckValid, CheckInvalid, CheckUnknown, CheckUnsupported} {
		if !IsValidCheckStatus(status) {
			t.Errorf("IsValidCheckStatus(%q) = false", status)
		}
	}
	for _, status := range []string{RunSuccess, RunSuccessEmpty, RunFailed} {
		if !IsTerminalRunStatus(status) {
			t.Errorf("IsTerminalRunStatus(%q) = false", status)
		}
	}
	if IsValidCheckStatus("broken") || IsTerminalRunStatus(RunRunning) {
		t.Fatal("invalid or non-terminal status accepted")
	}
}
