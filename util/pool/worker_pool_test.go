package pool

import (
	"testing"
	"time"
)

func TestExecuteBatchWithTimeoutDoesNotWaitForSlowTaskShutdown(t *testing.T) {
	started := time.Now()
	results := ExecuteBatchWithTimeout([]Task{
		func() interface{} {
			time.Sleep(200 * time.Millisecond)
			return "late"
		},
	}, 1, 20*time.Millisecond)
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("timeout returned after %v, want under 100ms", elapsed)
	}
	if len(results) != 0 {
		t.Fatalf("results = %#v, want none", results)
	}
}
