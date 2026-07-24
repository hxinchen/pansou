package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pansou/model"
)

type checkRoundTripFunc func(*http.Request) (*http.Response, error)

func (f checkRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func checkTestResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func newCheckTestService(client *http.Client) *CheckService {
	return &CheckService{
		cache:        make(map[string]cachedCheckResult),
		inflight:     make(map[string]*activeCheckCall),
		client:       client,
		xunleiTokens: make(map[string]xunleiCaptchaTokenEntry),
	}
}

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

func TestXunleiCaptchaTokenCacheUsesSingleflightAndTTL(t *testing.T) {
	var requests atomic.Int32
	client := &http.Client{Transport: checkRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if !strings.Contains(request.URL.Host, "xluser-ssl.xunlei.com") {
			t.Fatalf("unexpected request URL: %s", request.URL)
		}
		requests.Add(1)
		time.Sleep(20 * time.Millisecond)
		return checkTestResponse(http.StatusOK, `{"captcha_token":"token-1"}`), nil
	})}
	service := newCheckTestService(client)
	ctx := contextWithCheckHTTPClient(context.Background(), client)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := service.getXunleiCaptchaToken(ctx, "default")
			if err != nil || token != "token-1" {
				t.Errorf("token=%q err=%v", token, err)
			}
		}()
	}
	wg.Wait()
	if got := requests.Load(); got != 1 {
		t.Fatalf("captcha requests=%d, want 1", got)
	}

	service.xunleiMu.Lock()
	entry := service.xunleiTokens["default"]
	entry.expiresAt = time.Now().Add(-time.Second)
	service.xunleiTokens["default"] = entry
	service.xunleiMu.Unlock()
	if _, err := service.getXunleiCaptchaToken(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("captcha requests after expiry=%d, want 2", got)
	}
}

func TestXunleiRejectedCaptchaTokenIsInvalidatedAndRetried(t *testing.T) {
	var tokenRequests atomic.Int32
	var shareRequests atomic.Int32
	client := &http.Client{Transport: checkRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Host {
		case "xluser-ssl.xunlei.com":
			index := tokenRequests.Add(1)
			return checkTestResponse(http.StatusOK, fmt.Sprintf(`{"captcha_token":"token-%d"}`, index)), nil
		case "api-pan.xunlei.com":
			shareRequests.Add(1)
			if request.Header.Get("x-captcha-token") == "token-1" {
				return checkTestResponse(http.StatusUnauthorized, `{"error":"captcha token invalid"}`), nil
			}
			return checkTestResponse(http.StatusOK, `{"share_status":"OK","share_id":"abc"}`), nil
		default:
			return nil, errors.New("unexpected host: " + request.URL.Host)
		}
	})}
	service := newCheckTestService(client)
	result, err := service.checkXunlei(context.Background(), model.CheckItem{
		DiskType: "xunlei", URL: "https://pan.xunlei.com/s/abc",
	}, "https://pan.xunlei.com/s/abc", client, "default")
	if err != nil {
		t.Fatal(err)
	}
	if result.State != checkStateOK || tokenRequests.Load() != 2 || shareRequests.Load() != 2 {
		t.Fatalf("result=%+v tokenRequests=%d shareRequests=%d", result, tokenRequests.Load(), shareRequests.Load())
	}
}

func TestCheckItemContextCancelsUnderlyingRequest(t *testing.T) {
	client := &http.Client{Transport: checkRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	service := newCheckTestService(client)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := service.CheckItemContext(ctx, model.CheckItem{
		DiskType: "aliyun", URL: "https://www.aliyundrive.com/s/abc",
	}, "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("cancellation took %s", elapsed)
	}
}

func TestPruneExpiredCachesRemovesMemoryAndTokenEntries(t *testing.T) {
	service := newCheckTestService(http.DefaultClient)
	now := time.Now()
	service.cache["expired"] = cachedCheckResult{expiresAt: now.Add(-time.Second)}
	service.cache["fresh"] = cachedCheckResult{expiresAt: now.Add(time.Hour)}
	service.xunleiTokens["expired"] = xunleiCaptchaTokenEntry{token: "old", expiresAt: now.Add(-time.Second)}
	service.xunleiTokens["fresh"] = xunleiCaptchaTokenEntry{token: "new", expiresAt: now.Add(time.Hour)}
	service.pruneExpiredCaches()
	if _, ok := service.cache["expired"]; ok {
		t.Fatal("expired result cache entry was not pruned")
	}
	if _, ok := service.cache["fresh"]; !ok {
		t.Fatal("fresh result cache entry was pruned")
	}
	if _, ok := service.xunleiTokens["expired"]; ok {
		t.Fatal("expired token was not pruned")
	}
	if _, ok := service.xunleiTokens["fresh"]; !ok {
		t.Fatal("fresh token was pruned")
	}
}
