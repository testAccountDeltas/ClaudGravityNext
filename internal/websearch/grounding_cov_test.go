package websearch

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func covClient(h func(i int, r *http.Request) (*http.Response, error)) *http.Client {
	n := 0
	return &http.Client{Transport: stubRT(func(r *http.Request) (*http.Response, error) {
		i := n
		n++
		return h(i, r)
	})}
}

func covGround(c *http.Client) ([]WebSearchResultItem, string, error) {
	return searchWithGoogleGrounding(context.Background(), "go lang", "tok", "proj", c, "ua/1")
}

func TestCov_GroundingSnippetsPerChunkDedupAndTruncation(t *testing.T) {
	long := strings.Repeat("я", 400)
	body := `{"candidates":[{"content":{"parts":[{"text":"hidden","thought":true},{"text":"  Summary text. "},{"text":""}]},
	"groundingMetadata":{
	 "groundingSupports":[
	  {"segment":{"text":"first claim"},"groundingChunkIndices":[0]},
	  {"segment":{"text":"second claim"},"groundingChunkIndices":[0,3]},
	  {"segment":{"text":"   "},"groundingChunkIndices":[1]},
	  {"segment":{"text":"` + long + `"},"groundingChunkIndices":[4]}],
	 "groundingChunks":[
	  {"web":{"uri":"https://a.example","title":"A"}},
	  {"web":{"uri":"https://a.example","title":"A dup"}},
	  {"web":{"uri":"https://b.example"}},
	  {"web":{"uri":""}},
	  {"web":{"uri":"https://c.example","title":"C"}}]}}]}`
	var gotReq *http.Request
	items, summary, err := covGround(covClient(func(_ int, r *http.Request) (*http.Response, error) {
		gotReq = r
		return resp(200, body), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if summary != "Summary text." {
		t.Fatalf("summary must exclude thoughts and be trimmed: %q", summary)
	}
	if len(items) != 3 {
		t.Fatalf("dedup by uri, skip empty: %+v", items)
	}
	if items[0].Title != "A" || items[0].Snippet != "first claim second claim" {
		t.Fatalf("supports merge: %+v", items[0])
	}
	if items[1].Title != "https://b.example" || items[1].Snippet != "Summary text." {
		t.Fatalf("title falls back to uri, snippet to summary: %+v", items[1])
	}
	if r := []rune(items[2].Snippet); len(r) != 303 || !strings.HasSuffix(items[2].Snippet, "...") {
		t.Fatalf("snippet must truncate to 300 runes + ellipsis, got %d", len(r))
	}
	if gotReq.Header.Get("Authorization") != "Bearer tok" || gotReq.Header.Get("User-Agent") != "ua/1" {
		t.Fatalf("headers: %v", gotReq.Header)
	}
}

func TestCov_GroundingSummaryFallbackWithoutChunks(t *testing.T) {
	// Unwrapped (no "response" envelope) payload, no chunks: synthesized Google link.
	items, summary, err := covGround(covClient(func(int, *http.Request) (*http.Response, error) {
		return resp(200, `{"candidates":[{"content":{"parts":[{"text":"Answer"}]}}]}`), nil
	}))
	if err != nil || summary != "Answer" || len(items) != 1 {
		t.Fatalf("%+v %q %v", items, summary, err)
	}
	if items[0].Title != "Web Search: go lang" || items[0].URL != "https://www.google.com/search?q=go+lang" || items[0].Snippet != "Answer" {
		t.Fatalf("%+v", items[0])
	}
	// No text and no chunks => no results.
	items, _, err = covGround(covClient(func(int, *http.Request) (*http.Response, error) {
		return resp(200, `{"candidates":[{}]}`), nil
	}))
	if err != nil || len(items) != 0 {
		t.Fatalf("%+v %v", items, err)
	}
}

func TestCov_GroundingHostFailover(t *testing.T) {
	var hosts []string
	gate := `{"error":{"message":"User location is not supported for the API use."}}`
	items, _, err := covGround(covClient(func(i int, r *http.Request) (*http.Response, error) {
		hosts = append(hosts, r.URL.Host)
		if i == 0 {
			return resp(400, gate), nil // location gate => try next host
		}
		return resp(200, `{"response":{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}}`), nil
	}))
	if err != nil || len(items) != 1 || len(hosts) != 2 || hosts[0] == hosts[1] {
		t.Fatalf("failover: %v %v %+v", err, hosts, items)
	}

	// Transport error on every host => last error returned.
	_, _, err = covGround(covClient(func(int, *http.Request) (*http.Response, error) { return nil, errors.New("net down") }))
	if err == nil || !strings.Contains(err.Error(), "net down") {
		t.Fatalf("transport: %v", err)
	}
	// Non-gate HTTP error stops immediately (no second host).
	calls := 0
	_, _, err = covGround(covClient(func(int, *http.Request) (*http.Response, error) { calls++; return resp(429, "quota"), nil }))
	if err == nil || calls != 1 || !strings.Contains(err.Error(), "status 429") {
		t.Fatalf("429: %v calls=%d", err, calls)
	}
	// Location gate on all hosts => the gate error surfaces.
	_, _, err = covGround(covClient(func(int, *http.Request) (*http.Response, error) { return resp(400, gate), nil }))
	if err == nil || !strings.Contains(err.Error(), "location") {
		t.Fatalf("all gated: %v", err)
	}
	// 200 but no candidates.
	_, _, err = covGround(covClient(func(int, *http.Request) (*http.Response, error) { return resp(200, `{"response":{}}`), nil }))
	if err == nil || !strings.Contains(err.Error(), "no candidates") {
		t.Fatalf("no candidates: %v", err)
	}
	// Cancelled context fails request construction/transport.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := searchWithGoogleGrounding(ctx, "q", "t", "p", covClient(func(int, *http.Request) (*http.Response, error) {
		return nil, context.Canceled
	}), "ua"); err == nil {
		t.Fatal("cancelled ctx must error")
	}
}
