package orchestrate

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"claudgravity/internal/accounts"
	"claudgravity/internal/config"
	"claudgravity/internal/stream"
	"claudgravity/internal/testutil"
)

func cooldownOf(t *testing.T, email, model string) (acct, perModel time.Duration) {
	t.Helper()
	rec, ok := accounts.Manager().GetAccount(email)
	if !ok {
		t.Fatalf("account %s missing", email)
	}
	if !rec.CooldownUntil.IsZero() {
		acct = time.Until(rec.CooldownUntil)
	}
	if until, ok := rec.ModelCooldowns[model]; ok {
		perModel = time.Until(until)
	}
	return acct, perModel
}

func TestCooldownPolicy_Table(t *testing.T) {
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	future := time.Now().Add(90 * time.Second).UTC().Format(time.RFC3339)
	body429 := func(details string) error {
		return errors.New(`HTTP 429: {"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[` + details + `]}}`)
	}
	const email = "tu-0@example.com"
	cases := []struct {
		name     string
		status   int
		err      error
		model    string
		wantAcct [2]time.Duration // [min,max]; both 0 = no account-wide cooldown
		wantMdl  [2]time.Duration
	}{
		{"403 bans the account for an hour", 403, errors.New("HTTP 403: PERMISSION_DENIED"), "", [2]time.Duration{59 * time.Minute, 61 * time.Minute}, [2]time.Duration{}},
		{"non-InBand model-turn failure never cools", 429, &stream.UpstreamTurnError{ErrType: "overloaded_error", Status: 503, Msg: "RESOURCE_EXHAUSTED looking text"}, "", [2]time.Duration{}, [2]time.Duration{}},
		{"InBand 429 turn failure does cool", 429, &stream.UpstreamTurnError{ErrType: "rate_limit_error", Status: 429, InBand: true, Msg: "quota"}, "", [2]time.Duration{14 * time.Minute, 16 * time.Minute}, [2]time.Duration{}},
		{"resetTs in the past falls back to 15m", 429, body429(`{"metadata":{"quotaResetTimeStamp":"` + past + `"}}`), "", [2]time.Duration{14 * time.Minute, 16 * time.Minute}, [2]time.Duration{}},
		{"future resetTs is honoured", 429, body429(`{"metadata":{"quotaResetTimeStamp":"` + future + `"}}`), "", [2]time.Duration{80 * time.Second, 95 * time.Second}, [2]time.Duration{}},
		{"fractional retryDelay 3.5s", 429, body429(`{"retryDelay":"3.5s"}`), "", [2]time.Duration{3 * time.Second, 4 * time.Second}, [2]time.Duration{}},
		{"negative retryDelay is ignored (15m)", 429, body429(`{"retryDelay":"-5s"}`), "", [2]time.Duration{14 * time.Minute, 16 * time.Minute}, [2]time.Duration{}},
		{"retryDelay beats resetTs", 429, body429(`{"retryDelay":"20s","metadata":{"quotaResetTimeStamp":"` + future + `"}}`), "", [2]time.Duration{19 * time.Second, 21 * time.Second}, [2]time.Duration{}},
		{"per-minute quota is 30s", 429, body429(`{"metadata":{"quotaLimit":"GenerateRequestsPerMinutePerProject"}}`), "", [2]time.Duration{29 * time.Second, 31 * time.Second}, [2]time.Duration{}},
		{"model-scoped 429 leaves the account usable", 429, body429(`{"retryDelay":"45s"}`), "gem-x", [2]time.Duration{}, [2]time.Duration{44 * time.Second, 46 * time.Second}},
		{"503 cools 3 minutes", 503, errors.New("HTTP 503"), "", [2]time.Duration{179 * time.Second, 181 * time.Second}, [2]time.Duration{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			testutil.SeedAccounts(t, 1)
			acc, _ := accounts.Manager().GetAccount(email)
			applyPostFailureCooldownModel(acc, c.model, c.status, c.err)
			gotAcct, gotMdl := cooldownOf(t, email, c.model)
			check := func(what string, got time.Duration, want [2]time.Duration) {
				if want == [2]time.Duration{} {
					if got > 0 {
						t.Fatalf("%s cooldown = %v, want none", what, got)
					}
					return
				}
				if got < want[0] || got > want[1] {
					t.Fatalf("%s cooldown = %v, want within %v", what, got, want)
				}
			}
			check("account", gotAcct, c.wantAcct)
			check("model", gotMdl, c.wantMdl)
		})
	}
}

func TestCooldownPolicy_403Unsticks_ConversationMovesToAnotherAccount(t *testing.T) {
	testutil.SeedAccounts(t, 2)
	sel := accounts.GlobalSelector()
	tenant := config.TenantConfig{Name: "t", GoogleAuthType: "oauth"}
	anchor := fmt.Sprintf("anc-%s", t.Name())
	sel.RememberAffinity(anchor, "tu-0@example.com")
	acc, _ := accounts.Manager().GetAccount("tu-0@example.com")
	applyPostFailureCooldownModel(acc, "", http.StatusForbidden, errors.New("HTTP 403"))
	got, err := sel.SelectAccountForModel("key", anchor, tenant, nil, "gemini-3.8-flash-tiered")
	if err != nil || got.Email != "tu-1@example.com" {
		t.Fatalf("a banned account must lose its sticky binding: got %v err=%v", got, err)
	}
}
