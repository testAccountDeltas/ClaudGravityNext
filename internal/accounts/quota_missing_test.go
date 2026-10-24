package accounts

import (
	"testing"
	"time"
)

// D2: a bucket without remainingFraction used to decode as 0 and cool the
// whole account for 15 minutes.
func quotaAcc(t *testing.T, email string) *Record {
	t.Helper()
	acc := &Record{Email: email, AccessToken: "t", ExpiresAt: time.Now().Add(time.Hour), ProjectID: fallbackProjectID}
	seedAccounts(t, acc)
	return acc
}

func TestFetchQuota_MissingFractionIsUnknownNotExhausted(t *testing.T) {
	acc := quotaAcc(t, "d2-missing@example.com")
	fetchQuotaStub(t, `{"groups":[{"displayName":"Gemini Models","buckets":[
		{"bucketId":"w","window":"weekly"},{"bucketId":"h","window":"5h","remainingFraction":0.7}]}]}`)
	if err := globalAccountManager.FetchQuota(acc.Email); err != nil {
		t.Fatal(err)
	}
	if !acc.CooldownUntil.IsZero() {
		t.Fatalf("missing fraction cooled the account until %v", acc.CooldownUntil)
	}
	if !acc.Quotas[0].Buckets[0].FractionUnknown || acc.Quotas[0].Buckets[1].FractionUnknown {
		t.Fatalf("unknown flags wrong: %+v", acc.Quotas[0].Buckets)
	}
	if !hasRemainingQuota(acc) {
		t.Fatal("account must stay usable")
	}
	if f := getEffectiveQuotaFraction(acc); f != 0.7 {
		t.Fatalf("ranking must use the known bucket only, got %v", f)
	}
}

func TestFetchQuota_AllBucketsUnknown_NeutralRank(t *testing.T) {
	acc := quotaAcc(t, "d2-allunknown@example.com")
	fetchQuotaStub(t, `{"groups":[{"displayName":"Gemini Models","buckets":[{"bucketId":"w"}]}]}`)
	if err := globalAccountManager.FetchQuota(acc.Email); err != nil {
		t.Fatal(err)
	}
	if !acc.CooldownUntil.IsZero() || getEffectiveQuotaFraction(acc) != 0.5 {
		t.Fatalf("want usable at neutral 0.5, cooldown=%v frac=%v", acc.CooldownUntil, getEffectiveQuotaFraction(acc))
	}
}

func TestFetchQuota_ExplicitZeroStillExhausts(t *testing.T) {
	acc := quotaAcc(t, "d2-zero@example.com")
	fetchQuotaStub(t, `{"groups":[{"displayName":"Gemini Models","buckets":[{"bucketId":"w","remainingFraction":0}]}]}`)
	if err := globalAccountManager.FetchQuota(acc.Email); err != nil {
		t.Fatal(err)
	}
	if acc.CooldownUntil.IsZero() {
		t.Fatal("explicit 0 must still block the account")
	}
}

func TestFetchQuota_OutOfRangeFractions(t *testing.T) {
	acc := quotaAcc(t, "d2-range@example.com")
	fetchQuotaStub(t, `{"groups":[{"displayName":"Gemini Models","buckets":[
		{"bucketId":"hi","remainingFraction":7.5},{"bucketId":"neg","remainingFraction":-0.4}]}]}`)
	if err := globalAccountManager.FetchQuota(acc.Email); err != nil {
		t.Fatal(err)
	}
	b := acc.Quotas[0].Buckets
	if b[0].RemainingFraction != 1 || b[0].FractionUnknown {
		t.Fatalf(">1 must clamp to 1: %+v", b[0])
	}
	if !b[1].FractionUnknown {
		t.Fatalf("negative must be ignored as unknown: %+v", b[1])
	}
	if !acc.CooldownUntil.IsZero() {
		t.Fatal("garbage fractions must not cool the account")
	}
}

func TestFetchQuota_TolerantBodies(t *testing.T) {
	cases := map[string]string{
		"groups null":  `{"groups":null}`,
		"empty object": `{}`,
		"empty groups": `{"groups":[]}`,
		"buckets null": `{"groups":[{"displayName":"Gemini Models","buckets":null}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			acc := quotaAcc(t, "d2-tol@example.com")
			fetchQuotaStub(t, body)
			if err := globalAccountManager.FetchQuota(acc.Email); err != nil {
				t.Fatal(err)
			}
			if !acc.CooldownUntil.IsZero() || !hasRemainingQuota(acc) {
				t.Fatal("empty quota data must not block the account")
			}
		})
	}
}

func TestFetchQuota_MalformedBodyKeepsPreviousState(t *testing.T) {
	for name, body := range map[string]string{
		"garbage":       `<html>oops`,
		"truncated":     `{"groups":[{"displayName":"Gem`,
		"wrong type":    `{"groups":[{"buckets":[{"remainingFraction":"lots"}]}]}`,
		"empty body":    ``,
		"groups string": `{"groups":"x"}`,
	} {
		t.Run(name, func(t *testing.T) {
			acc := quotaAcc(t, "d2-bad@example.com")
			acc.Quotas = []QuotaGroup{{DisplayName: "Gemini Models", Buckets: []QuotaBucket{{BucketID: "keep", RemainingFraction: 0.9}}}}
			fetchQuotaStub(t, body)
			if err := globalAccountManager.FetchQuota(acc.Email); err == nil {
				t.Fatal("expected an error for a malformed body")
			}
			if len(acc.Quotas) != 1 || acc.Quotas[0].Buckets[0].BucketID != "keep" {
				t.Fatalf("previous snapshot clobbered: %+v", acc.Quotas)
			}
			if !acc.CooldownUntil.IsZero() || acc.LastError == "" {
				t.Fatalf("want no cooldown and LastError set, got cooldown=%v err=%q", acc.CooldownUntil, acc.LastError)
			}
		})
	}
}
