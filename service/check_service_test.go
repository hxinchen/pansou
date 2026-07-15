package service

import (
	"testing"
	"time"
)

func TestClassifyKnownFailureState(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   string
	}{
		{name: "expired chinese", values: []string{"分享链接已过期"}, want: checkStateExpired},
		{name: "expired tianyi code", values: []string{"ShareExpiredError"}, want: checkStateExpired},
		{name: "cancelled chinese", values: []string{"分享已被取消"}, want: checkStateCancelled},
		{name: "cancelled english", values: []string{"share canceled by owner"}, want: checkStateCancelled},
		{name: "violation chinese", values: []string{"分享内容违规"}, want: checkStateViolation},
		{name: "violation tianyi code", values: []string{"ShareAuditNotPass"}, want: checkStateViolation},
		{name: "invalid chinese", values: []string{"分享信息不存在"}, want: checkStateInvalid},
		{name: "invalid english", values: []string{"share not found"}, want: checkStateInvalid},
		{name: "unknown", values: []string{"remote service busy"}, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyKnownFailureState(tt.values...); got != tt.want {
				t.Fatalf("classifyKnownFailureState() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClassifyFailureStateDefaultsToInvalid(t *testing.T) {
	if got := classifyFailureState("remote service busy"); got != checkStateInvalid {
		t.Fatalf("classifyFailureState() = %q, want %q", got, checkStateInvalid)
	}
}

func TestIsLockedMessage(t *testing.T) {
	tests := []string{
		"请输入提取码",
		"访问码错误",
		"receive_code invalid",
		"pass_code required",
	}

	for _, tt := range tests {
		t.Run(tt, func(t *testing.T) {
			if !isLockedMessage(tt) {
				t.Fatalf("isLockedMessage(%q) = false, want true", tt)
			}
		})
	}
}

func TestHasQuarkFilteredFile(t *testing.T) {
	tests := []struct {
		name  string
		files []quarkShareFile
		want  bool
	}{
		{
			name:  "risk type",
			files: []quarkShareFile{{FileName: "movie.mkv", RiskType: 1}},
			want:  true,
		},
		{
			name:  "banned",
			files: []quarkShareFile{{FileName: "movie.mkv", Ban: true}},
			want:  true,
		},
		{
			name:  "status abnormal",
			files: []quarkShareFile{{FileName: "movie.mkv", Status: 3}},
			want:  true,
		},
		{
			name:  "filtered marker",
			files: []quarkShareFile{{FileName: "和谐太快了，在线解压吧", Status: 1}},
			want:  true,
		},
		{
			name:  "normal",
			files: []quarkShareFile{{FileName: "电影合集", Status: 1}},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasQuarkFilteredFile(tt.files); got != tt.want {
				t.Fatalf("hasQuarkFilteredFile() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestBuild123APIURLsPrefersOriginalHost(t *testing.T) {
	got := build123APIURLs("https://123865.com/s/IpPUVv-JRRj?pwd=JZMM", "IpPUVv-JRRj")
	if len(got) == 0 || got[0] != "https://123865.com/api/share/info?shareKey=IpPUVv-JRRj" {
		t.Fatalf("first API URL = %q", got)
	}
	if len(got) < 4 {
		t.Fatalf("fallback API URLs = %q, want multiple fallbacks", got)
	}
}

func TestExtractMobileShareInfo(t *testing.T) {
	raw := "https://yun.139.com/shareweb/#/w/i/2qidXH7CYnrze?pwd=lv5e"
	if got := extractMobileShareID(raw); got != "2qidXH7CYnrze" {
		t.Fatalf("extractMobileShareID() = %q", got)
	}
	if got := extractMobilePassword(raw, "fallback"); got != "lv5e" {
		t.Fatalf("extractMobilePassword() = %q", got)
	}
}

func TestTTLForDetailedFailureStates(t *testing.T) {
	for _, state := range []string{
		checkStateBad,
		checkStateInvalid,
		checkStateExpired,
		checkStateCancelled,
		checkStateViolation,
	} {
		t.Run(state, func(t *testing.T) {
			if got := ttlForState(state); got != 6*time.Hour {
				t.Fatalf("ttlForState(%q) = %s, want %s", state, got, 6*time.Hour)
			}
		})
	}
}
