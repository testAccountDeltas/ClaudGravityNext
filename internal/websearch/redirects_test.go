package websearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestResolveGroundingRedirects_ReplacesOnlyWhatResolves(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/grounding-api-redirect/ok":
			http.Redirect(w, r, "https://example.org/the/page?x=1", http.StatusFound)
		case "/grounding-api-redirect/loop":
			http.Redirect(w, r, "http://"+r.Host+"/grounding-api-redirect/ok", http.StatusFound)
		case "/grounding-api-redirect/gone":
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	groundingRedirectHosts[u.Host] = true
	defer delete(groundingRedirectHosts, u.Host)

	results := []WebSearchResultItem{
		{Title: "a", URL: srv.URL + "/grounding-api-redirect/ok"},
		{Title: "b", URL: srv.URL + "/grounding-api-redirect/gone"},
		{Title: "c", URL: srv.URL + "/grounding-api-redirect/loop"},
		{Title: "d", URL: "https://example.com/already/real"},
	}
	resolveGroundingRedirects(context.Background(), &http.Client{}, results)
	if results[0].URL != "https://example.org/the/page?x=1" {
		t.Errorf("resolved: %q", results[0].URL)
	}
	if results[1].URL != srv.URL+"/grounding-api-redirect/gone" {
		t.Errorf("a failed lookup must keep the original link: %q", results[1].URL)
	}
	if results[2].URL != srv.URL+"/grounding-api-redirect/loop" {
		t.Errorf("a hop to the same service is not the page: %q", results[2].URL)
	}
	if results[3].URL != "https://example.com/already/real" {
		t.Errorf("an ordinary link must be left alone: %q", results[3].URL)
	}
	resolveGroundingRedirects(context.Background(), nil, results) // no client: nothing happens
}

func TestIsGroundingRedirect(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://vertexaisearch.cloud.google.com/grounding-api-redirect/AbC": true,
		"https://vertexaisearch.cloud.google.com/other":                      false,
		"https://example.com/grounding-api-redirect/AbC":                     false,
		"::bad": false,
	} {
		if got := isGroundingRedirect(raw); got != want {
			t.Errorf("%s: %v", raw, got)
		}
	}
}
