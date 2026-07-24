package qqpd

import (
	"strings"
	"testing"
	"time"
)

func TestBuildQQLoginCheckURLUsesCurrentActionTimestamp(t *testing.T) {
	now := time.Date(2026, time.July, 19, 10, 30, 0, 123000000, time.UTC)
	url := buildQQLoginCheckURL("123456", now)

	if !strings.Contains(url, "ptqrtoken=123456") {
		t.Fatalf("login URL is missing ptqrtoken: %s", url)
	}
	if !strings.Contains(url, "action=0-0-"+"1784457000123") {
		t.Fatalf("login URL is missing current action timestamp: %s", url)
	}
	if strings.Contains(url, "1761211119400") {
		t.Fatalf("login URL still contains the stale action timestamp: %s", url)
	}
}
