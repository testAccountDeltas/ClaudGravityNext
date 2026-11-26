package orchestrate

import (
	"net/http"
	"strings"
	"testing"

	"claudgravity/internal/accounts"
)

const (
	claudeEmpty    = `{"groups":[{"displayName":"Claude and GPT","buckets":[{"bucketId":"b","window":"5h","remainingFraction":0.01}]}]}`
	claudeFull     = `{"groups":[{"displayName":"Claude and GPT","buckets":[{"bucketId":"b","window":"5h","remainingFraction":0.9}]}]}`
	sonnet46       = `{"model":"claude-sonnet-4-6","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
	opus46Thinking = `{"model":"claude-opus-4-6-thinking","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
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
	for _, m := range []string{"gemini-4-hydrogen-tiered-xhigh", "gemini-3.8-flash-tiered", "claude-sonnet-4-6", "claude-opus-4-6-thinking"} {
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

func TestPipeline_ExhaustedNativeOpusThinkingIsRemappedToHydrogen(t *testing.T) {
	r := runExec(t, execOpts{nAcc: 1, streaming: true, claude: opus46Thinking, afterSeed: withQuota(t, claudeEmpty),
		up: func(int, *http.Request, string) (*http.Response, error) {
			return respBody(200, tfSSE(tfWrap(tfGood))), nil
		}})
	if !r.ok || r.status != http.StatusOK || r.attempts != 1 {
		t.Fatalf("ok=%v status=%d attempts=%d err=%v", r.ok, r.status, r.attempts, r.err)
	}
	if got := upstreamModelOf(r.bodies[0]); got != "gemini-4-hydrogen-tiered-xhigh" {
		t.Fatalf("upstream model = %s, want gemini-4-hydrogen-tiered-xhigh\n%.300s", got, r.bodies[0])
	}
}

func TestPipeline_PublicAPI_Gemini4NormalizesModelAndThinkingLevel(t *testing.T) {
	var reqPath string
	r := runExec(t, execOpts{
		nAcc:      0,
		direct:    true,
		streaming: true,
		claude:    `{"model":"claude-opus-4-6","max_tokens":128,"temperature":0.2,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`,
		up: func(_ int, req *http.Request, _ string) (*http.Response, error) {
			reqPath = req.URL.Path
			return respBody(200, tfSSE(tfWrap(tfGood))), nil
		},
	})
	if !r.ok || r.status != http.StatusOK {
		t.Fatalf("ok=%v status=%d err=%v", r.ok, r.status, r.err)
	}
	if !strings.Contains(reqPath, "/models/gemini-4-hydrogen:") {
		t.Fatalf("public API path = %q, want normalized /models/gemini-4-hydrogen:", reqPath)
	}
	if !strings.Contains(r.bodies[0], `"thinkingLevel":"high"`) || strings.Contains(r.bodies[0], `"thinkingBudget"`) || strings.Contains(r.bodies[0], `"temperature"`) {
		t.Fatalf("unexpected public Gemini 4 payload: %s", r.bodies[0])
	}
}
