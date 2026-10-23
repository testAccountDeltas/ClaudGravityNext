package orchestrate

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"claudgravity/internal/accounts"
)

func TestApplyPostFailureCooldownModel_429CoolsOnlyThatModel(t *testing.T) {
	acc := seedAccount(t, "w6-model429@example.com", "tok")
	body := `HTTP 429: {"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"retryDelay":"45s"}]}}`

	applyPostFailureCooldownModel(acc, "gemini-3.8-flash-tiered", http.StatusTooManyRequests, errors.New(body))

	fresh, _ := accounts.Manager().GetAccount(acc.Email)
	if !fresh.CooldownUntil.IsZero() {
		t.Fatalf("a model-scoped 429 must not set the account-wide cooldown, got %v", fresh.CooldownUntil)
	}
	d := time.Until(fresh.ModelCooldowns["gemini-3.8-flash-tiered"])
	if d < 40*time.Second || d > 50*time.Second {
		t.Fatalf("expected ~45s model cooldown, got %v", d)
	}
	if _, ok := fresh.ModelCooldowns["gemini-3.8-flash-low"]; ok {
		t.Fatal("other model must be untouched")
	}
}

func TestApplyPostFailureCooldownModel_429RefreshKeepsCooldown(t *testing.T) {
	acc := seedAccount(t, "w6-keep429@example.com", "tok")
	body := `HTTP 429: {"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"retryDelay":"120s"}]}}`
	applyPostFailureCooldownModel(acc, "m", http.StatusTooManyRequests, errors.New(body))
	// The async quota refresh the cooldown path triggers may run (and fail
	// on the network) at any point; the cooldown must survive either way.
	_ = accounts.Manager().FetchQuota(acc.Email)
	fresh, _ := accounts.Manager().GetAccount(acc.Email)
	if time.Until(fresh.ModelCooldowns["m"]) < 100*time.Second {
		t.Fatal("model cooldown lost after quota refresh")
	}
}

func TestUpstreamErrStatus_WatchdogVsClient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	if clientGone(ctx) {
		t.Fatal("live client ctx reported gone")
	}
	cancel()
	if !clientGone(ctx) {
		t.Fatal("cancelled client ctx must be gone")
	}
	if got := upstreamErrStatus(context.Canceled); got != http.StatusGatewayTimeout {
		t.Fatalf("watchdog cancel must map to 504, got %d", got)
	}
	if got := upstreamErrStatus(errors.New("boom")); got != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", got)
	}
}
