package orchestrate

import (
	"fmt"
	"testing"
)

const rl429 = `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"quota"}}`

func TestExecute_429sDoNotConsumeAttempts(t *testing.T) {
	r := tfRunN(t, 5, fmt.Sprintf(tfReq, ""), false, func(n int, _ string) (int, string) {
		if n <= 3 {
			return 429, rl429
		}
		return 200, tfWrap(tfGood)
	})
	if !r.ok || r.attempts != 4 {
		t.Fatalf("ok=%v attempts=%d status=%d err=%v body=%s", r.ok, r.attempts, r.status, r.err, r.body)
	}
}

func TestExecute_429sBoundedByPoolSize(t *testing.T) {
	r := tfRunN(t, 5, fmt.Sprintf(tfReq, ""), false, func(n int, _ string) (int, string) { return 429, rl429 })
	if r.ok || r.attempts != 5 {
		t.Fatalf("want failure after 5 tries (pool size), ok=%v attempts=%d status=%d", r.ok, r.attempts, r.status)
	}
}
