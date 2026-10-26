package orchestrate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"claudgravity/internal/accounts"
	"claudgravity/internal/config"
	"claudgravity/internal/reqctx"
	"claudgravity/internal/translate"
)

// execOpts drives Execute() against a scriptable fake upstream. Unlike
// tfRunN it allows custom response bodies/errors, a cancellable client
// context, token-refresh failures and a hook after the pool is seeded.
type execOpts struct {
	nAcc      int
	streaming bool
	claude    string
	ctx       context.Context
	// refreshFail: refresh tokens ("ref1", ...) whose OAuth refresh returns 400.
	refreshFail map[string]bool
	afterSeed   func()
	up          func(n int, req *http.Request, body string) (*http.Response, error)
	// timeouts, seconds (0 -> 5)
	ttft  int
	panic int // PanicTimeoutSeconds (0 -> 5)
	// direct runs the request as a plain API-key tenant (no Antigravity pool).
	direct bool
	// suppressTools mirrors Params.SuppressToolCalls (tool_choice "none").
	suppressTools bool
	// tenant tweaks the tenant before translation.
	tenant func(*config.TenantConfig)
}

type execRes struct {
	ok       bool
	status   int
	err      error
	body     string
	attempts int
	bodies   []string
	auths    []string
}

func respBody(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func runExec(t *testing.T, o execOpts) execRes {
	t.Helper()
	if o.nAcc == 0 {
		o.nAcc = 2
	}
	if o.claude == "" {
		o.claude = fmt.Sprintf(tfReq, "")
	}
	if o.ctx == nil {
		o.ctx = context.Background()
	}
	if o.ttft == 0 {
		o.ttft = 5
	}
	if o.panic == 0 {
		o.panic = 5
	}
	restoreOAuth := accounts.SetHTTPClientForTest(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(req.Body)
		for rt := range o.refreshFail {
			if strings.Contains(string(b), "refresh_token="+rt) || strings.Contains(string(b), `"`+rt+`"`) {
				return respBody(400, `{"error":"invalid_grant"}`), nil
			}
		}
		return respBody(200, `{"access_token":"fresh","expires_in":3600}`), nil
	})})
	t.Cleanup(restoreOAuth)
	t.Cleanup(accounts.ResetForTest)
	dir := t.TempDir()
	tag := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	for i := 0; i < o.nAcc; i++ {
		e := fmt.Sprintf("ex-%d-%s@example.com", i, tag)
		_ = os.WriteFile(filepath.Join(dir, e+".json"), []byte(fmt.Sprintf(`{"email":%q,"access_token":"token-%d","refresh_token":"ref%d"}`, e, i+1, i+1)), 0o644)
	}
	if err := accounts.InitAccountManager(dir, config.ServerConfig{}); err != nil {
		t.Fatal(err)
	}
	if o.afterSeed != nil {
		o.afterSeed()
	}
	var res execRes
	var mu sync.Mutex
	restoreTransport := SetTransportForTest(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(req.Body)
		mu.Lock()
		res.attempts++
		n := res.attempts
		res.bodies = append(res.bodies, string(b))
		res.auths = append(res.auths, req.Header.Get("Authorization"))
		mu.Unlock()
		return o.up(n, req, string(b))
	}))
	t.Cleanup(restoreTransport)

	tenant := config.TenantConfig{Name: "t", GoogleAuthType: "oauth", DefaultModel: "gemini-3.8-flash-tiered"}
	sess := "sess-" + t.Name()
	if o.tenant != nil {
		o.tenant(&tenant)
	}
	tr, err := translate.ConvertClaudeRequestToGemini([]byte(o.claude), tenant, "", "main", sess, "", !o.direct)
	if err != nil {
		t.Fatal(err)
	}
	p := &reqctx.Params{RID: 960, SessionID: sess, ConvAnchor: "anc-" + t.Name(), Tenant: tenant,
		ServerCfg: config.ServerConfig{PanicTimeoutSeconds: o.panic, TTFTTimeoutSeconds: o.ttft, ReadTimeoutSeconds: 5, MaxStreamDurationSeconds: 10},
		ModelName: "claude-sonnet-4-5", IsStreaming: o.streaming, IsAntigravity: !o.direct, PrefillText: tr.PrefillText, SuppressToolCalls: o.suppressTools,
		SyntheticReasonTools: tr.SyntheticReasonTools, ReverseToolNameMap: tr.ReverseToolNameMap, RawClaudeBody: []byte(o.claude)}
	st := &reqctx.State{StartedAt: time.Now(), ResolvedModelName: tr.ResolvedModel, GeminiBody: tr.GeminiBody, EstInputTokens: tr.EstimatedInputTokens}
	rec := httptest.NewRecorder()
	res.ok, res.status, res.err = (&Orchestrator{}).Execute(o.ctx, p, st, rec)
	res.body = rec.Body.String()
	return res
}

// ctxBody is a response body whose Read honours the request context like a
// real net/http body does (a fake transport otherwise ignores cancellation).
type ctxBody struct {
	ctx    context.Context
	chunks []string // served first, one per Read
	// after: what happens once chunks are exhausted: "hang" blocks until the
	// context is cancelled, "error" fails with errAfter, "" is a clean EOF.
	after    string
	errAfter error
	onHang   func() // called once, right before blocking in "hang" mode
	closed   chan struct{}
	once     sync.Once
}

func newCtxBody(ctx context.Context, after string, errAfter error, chunks ...string) *ctxBody {
	return &ctxBody{ctx: ctx, chunks: chunks, after: after, errAfter: errAfter, closed: make(chan struct{})}
}

func (b *ctxBody) Read(p []byte) (int, error) {
	if len(b.chunks) > 0 {
		c := b.chunks[0]
		b.chunks = b.chunks[1:]
		return copy(p, c), nil
	}
	switch b.after {
	case "hang":
		if b.onHang != nil {
			b.onHang()
		}
		select {
		case <-b.ctx.Done():
			return 0, b.ctx.Err()
		case <-b.closed:
			return 0, io.ErrClosedPipe
		}
	case "error":
		return 0, b.errAfter
	}
	return 0, io.EOF
}

func (b *ctxBody) Close() error { b.once.Do(func() { close(b.closed) }); return nil }

func okResp(body io.ReadCloser) *http.Response {
	return &http.Response{StatusCode: 200, Body: body, Header: make(http.Header)}
}
