package stream

import (
	"net/http/httptest"
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/reqctx"
)

func cacheStartUsage(t *testing.T, model string, est int, frac, frac1h float64) string {
	t.Helper()
	p := basicParams()
	p.ModelName = model
	st := &reqctx.State{EstInputTokens: est, CachePrefixFrac: frac, CacheFrac1h: frac1h}
	rec := httptest.NewRecorder()
	f := NewFSM(NewSafeSSEWriter(1, rec, rec, nil), 1, p, st)
	if err := f.EnsureMessageStart(); err != nil {
		t.Fatal(err)
	}
	return parseEventPayload(rec.Body.String(), "message_start")
}

func TestCacheUsage_BelowModelMinimumInventsNothing(t *testing.T) {
	// 1234 tokens, prefix ~617 < 1024 minimum.
	pl := cacheStartUsage(t, "claude-sonnet-4-5", 1234, 0.5, 0)
	if v := gjson.Get(pl, "message.usage.cache_creation_input_tokens").Int(); v != 0 {
		t.Fatalf("cache_creation=%d want 0", v)
	}
	if v := gjson.Get(pl, "message.usage.input_tokens").Int(); v != 1234 {
		t.Fatalf("input_tokens=%d want 1234", v)
	}
}

func TestCacheUsage_TTLBreakdownObject(t *testing.T) {
	pl := cacheStartUsage(t, "claude-sonnet-4-5", 10000, 0.5, 0.2)
	u := gjson.Get(pl, "message.usage")
	if u.Get("cache_creation_input_tokens").Int() != 5000 ||
		u.Get("cache_creation.ephemeral_1h_input_tokens").Int() != 2000 ||
		u.Get("cache_creation.ephemeral_5m_input_tokens").Int() != 3000 {
		t.Fatalf("unexpected usage: %s", u.Raw)
	}
	if u.Get("input_tokens").Int() != 5000 {
		t.Fatalf("input_tokens=%d", u.Get("input_tokens").Int())
	}
}
