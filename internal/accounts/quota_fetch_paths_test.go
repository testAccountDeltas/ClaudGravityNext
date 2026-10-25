package accounts

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type quotaCall struct{ host, body string }

// quotaServer scripts FetchQuota's upstream: reply gets the call number
// (from 0) and the request; calls are recorded.
func quotaServer(t *testing.T, reply func(n int, c quotaCall) (int, string, error)) *[]quotaCall {
	t.Helper()
	var mu sync.Mutex
	var calls []quotaCall
	withStubbedHTTPClient(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		c := quotaCall{r.URL.Host, string(b)}
		n := len(calls)
		calls = append(calls, c)
		mu.Unlock()
		code, body, err := reply(n, c)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	}))
	return &calls
}

func fqAcc(email, project string) *Record {
	a := catalogAccount(email)
	a.ProjectID = project
	return a
}

func TestFetchQuota_UnknownAccount(t *testing.T) {
	seedAccounts(t)
	if err := globalAccountManager.FetchQuota("ghost@example.com"); err == nil {
		t.Fatal("want an error")
	}
}

func TestFetchQuota_TokenRefreshFailureIsRecorded(t *testing.T) {
	a := &Record{Email: "fq-tok@example.com", RefreshToken: "dead", ExpiresAt: time.Now().Add(-time.Hour)}
	seedAccounts(t, a)
	quotaServer(t, func(int, quotaCall) (int, string, error) { return 400, `{"error":"invalid_grant"}`, nil })
	if err := globalAccountManager.FetchQuota(a.Email); err == nil {
		t.Fatal("want the refresh error")
	}
	got, _ := globalAccountManager.GetAccount(a.Email)
	if got.LastError == "" {
		t.Fatal("the failure must be visible on the account")
	}
}

func TestFetchQuota_ProjectPayloadFirstThenEmptyOnRejection(t *testing.T) {
	seedAccounts(t, fqAcc("fq-proj@example.com", "real-project-1"))
	calls := quotaServer(t, func(n int, _ quotaCall) (int, string, error) {
		if n == 0 {
			return 403, `denied`, nil
		}
		return 200, `{"groups":[]}`, nil
	})
	if err := globalAccountManager.FetchQuota("fq-proj@example.com"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 || !strings.Contains((*calls)[0].body, "real-project-1") || (*calls)[1].body != "{}" || (*calls)[0].host != (*calls)[1].host {
		t.Fatalf("want project payload then {} on the same host, got %+v", *calls)
	}
}

func TestFetchQuota_RateLimitOrTransportErrorMovesToTheNextHost(t *testing.T) {
	for name, first := range map[string]func() (int, string, error){
		"429":       func() (int, string, error) { return 429, `slow`, nil },
		"503":       func() (int, string, error) { return 503, `down`, nil },
		"transport": func() (int, string, error) { return 0, "", errors.New("connection reset") },
	} {
		t.Run(name, func(t *testing.T) {
			email := "fq-host-" + name + "@example.com"
			seedAccounts(t, fqAcc(email, fallbackProjectID))
			calls := quotaServer(t, func(n int, _ quotaCall) (int, string, error) {
				if n == 0 {
					return first()
				}
				return 200, `{"groups":[]}`, nil
			})
			if err := globalAccountManager.FetchQuota(email); err != nil {
				t.Fatal(err)
			}
			if len(*calls) != 2 || (*calls)[0].host == (*calls)[1].host {
				t.Fatalf("second attempt must target another host: %+v", *calls)
			}
		})
	}
}

func TestFetchQuota_UnauthorizedInvalidatesTheTokenAndStops(t *testing.T) {
	a := fqAcc("fq-401@example.com", fallbackProjectID)
	seedAccounts(t, a)
	calls := quotaServer(t, func(int, quotaCall) (int, string, error) { return 401, `expired`, nil })
	err := globalAccountManager.FetchQuota(a.Email)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("a 401 must not be retried elsewhere: %+v", *calls)
	}
	if got, _ := globalAccountManager.GetAccount(a.Email); got.AccessToken != "" {
		t.Fatal("the rejected access token must be dropped")
	}
}

func TestFetchQuota_AllHostsFailing(t *testing.T) {
	a := fqAcc("fq-all@example.com", fallbackProjectID)
	seedAccounts(t, a)
	calls := quotaServer(t, func(int, quotaCall) (int, string, error) { return 500, `x`, nil })
	if err := globalAccountManager.FetchQuota(a.Email); err == nil {
		t.Fatal("want an error")
	}
	if len(*calls) != len(QuotaHosts) {
		t.Fatalf("each host once: %d calls for %d hosts", len(*calls), len(QuotaHosts))
	}
}

func TestFetchQuota_MalformedBodyKeepsThePreviousSnapshot(t *testing.T) {
	a := fqAcc("fq-bad@example.com", fallbackProjectID)
	a.Quotas = weeklyQuota(0.5)
	seedAccounts(t, a)
	quotaServer(t, func(int, quotaCall) (int, string, error) { return 200, `<html>`, nil })
	if err := globalAccountManager.FetchQuota(a.Email); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := globalAccountManager.GetAccount(a.Email); len(got.Quotas) != 1 || got.Quotas[0].Buckets[0].RemainingFraction != 0.5 {
		t.Fatalf("snapshot lost: %+v", got.Quotas)
	}
}

func TestFetchQuota_FractionNormalisation(t *testing.T) {
	a := fqAcc("fq-frac@example.com", fallbackProjectID)
	seedAccounts(t, a)
	quotaServer(t, func(int, quotaCall) (int, string, error) {
		return 200, `{"groups":[{"displayName":"Claude","buckets":[
			{"bucketId":"missing","window":"5h"},
			{"bucketId":"negative","window":"5h","remainingFraction":-1},
			{"bucketId":"over","window":"5h","remainingFraction":7},
			{"bucketId":"half","window":"5h","remainingFraction":0.5}]}]}`, nil
	})
	if err := globalAccountManager.FetchQuota(a.Email); err != nil {
		t.Fatal(err)
	}
	got, _ := globalAccountManager.GetAccount(a.Email)
	b := got.Quotas[0].Buckets
	if !b[0].FractionUnknown || !b[1].FractionUnknown || b[2].RemainingFraction != 1 || b[3].RemainingFraction != 0.5 || b[3].FractionUnknown {
		t.Fatalf("buckets = %+v", b)
	}
}
