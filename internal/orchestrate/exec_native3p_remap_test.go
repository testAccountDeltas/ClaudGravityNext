package orchestrate

import (
	"net/http"
	"strings"
	"testing"

	"claudgravity/internal/accounts"
)

const (
	claudeEmpty = `{"groups":[{"displayName":"Claude and GPT","buckets":[{"bucketId":"b","window":"5h","remainingFraction":0.01}]}]}`
	claudeFull  = `{"groups":[{"displayName":"Claude and GPT","buckets":[{"bucketId":"b","window":"5h","remainingFraction":0.9}]}]}`
	sonnet46    = `{"model":"claude-sonnet-4-6","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
)

// withQuota gives every seeded account the quota summary Google would report.
func withQuota(t *testing.T, summary string) func() {
	return func() {
		restore := accounts.SetHTTPClientForTest(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return respBody(200, summary), nil
		})})
		defer restore()
		for _, a := range accounts.Manager().GetAccounts() {
			if err := accounts.Manager().FetchQuota(a.Email); err != nil {
				t.Fatalf("seeding quota: %v", err)
			}
		}
	}
}

func upstreamModelOf(body string) string {
	for _, m := range []string{"gemini-3.8-flash-tiered", "claude-sonnet-4-6"} {
		if strings.Contains(body, `"model":"`+m+`"`) {
			return m
		}
	}
	return "?"
}

func runOn3P(t *testing.T, summary string) execRes {
	return runExec(t, execOpts{nAcc: 1, streaming: true, claude: sonnet46, afterSeed: withQuota(t, summary),
		up: func(int, *http.Request, string) (*http.Response, error) {
			return respBody(200, tfSSE(tfWrap(tfGood))), nil
		}})
}

// A native Claude model whose quota group on the chosen account is spent would
// only ever 429; the pipeline serves the request on the Gemini tier instead.
func TestPipeline_ExhaustedNativeClaudeIsRemappedToGemini(t *testing.T) {
	r := runOn3P(t, claudeEmpty)
	if !r.ok || r.status != http.StatusOK || r.attempts != 1 {
		t.Fatalf("ok=%v status=%d attempts=%d err=%v", r.ok, r.status, r.attempts, r.err)
	}
	if got := upstreamModelOf(r.bodies[0]); got != "gemini-3.8-flash-tiered" {
		t.Fatalf("upstream model = %s, want the Gemini fallback\n%.300s", got, r.bodies[0])
	}
}

func TestPipeline_NativeClaudeWithQuotaKeepsItsModel(t *testing.T) {
	r := runOn3P(t, claudeFull)
	if !r.ok || r.attempts != 1 {
		t.Fatalf("ok=%v attempts=%d err=%v", r.ok, r.attempts, r.err)
	}
	if got := upstreamModelOf(r.bodies[0]); got != "claude-sonnet-4-6" {
		t.Fatalf("upstream model = %s, want the native Claude model\n%.300s", got, r.bodies[0])
	}
}
