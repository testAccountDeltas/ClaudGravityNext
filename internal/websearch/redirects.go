// Part of the websearch package — see search.go. Google's grounding returns
// every source as a redirect through vertexaisearch.cloud.google.com
// ("/grounding-api-redirect/<token>"), so Claude Code showed those opaque
// links as the sources of a search. The redirect endpoint answers with a plain
// 302 to the real page; one request per source (without following it) gives the
// real address.
package websearch

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// groundingRedirectHosts are the hosts whose links are grounding redirects.
// A variable so tests can add their own server.
var groundingRedirectHosts = map[string]bool{"vertexaisearch.cloud.google.com": true}

const (
	redirectResolveTimeout = 3 * time.Second
	redirectResolveWorkers = 5
)

func isGroundingRedirect(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && groundingRedirectHosts[u.Host] && strings.Contains(u.Path, "/grounding-api-redirect/")
}

// resolveGroundingRedirects replaces redirect links in results by the real
// addresses they point to. A link that cannot be resolved quickly stays as it
// was: nothing here may make a search fail or wait for long.
func resolveGroundingRedirects(ctx context.Context, base *http.Client, results []WebSearchResultItem) {
	if base == nil {
		return
	}
	client := &http.Client{
		Transport: base.Transport,
		Timeout:   redirectResolveTimeout,
		// The answer is the redirect itself; do not visit the target page.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	sem := make(chan struct{}, redirectResolveWorkers)
	var wg sync.WaitGroup
	for i := range results {
		if !isGroundingRedirect(results[i].URL) {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(item *WebSearchResultItem) {
			defer wg.Done()
			defer func() { <-sem }()
			if real := followOnce(ctx, client, item.URL); real != "" {
				item.URL = real
			}
		}(&results[i])
	}
	wg.Wait()
}

// followOnce returns the Location of a redirect, or "" when there is none or
// it does not point to an ordinary web address.
func followOnce(ctx context.Context, client *http.Client, link string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 300 || resp.StatusCode > 399 {
		return ""
	}
	loc, err := resp.Location()
	if err != nil || (loc.Scheme != "http" && loc.Scheme != "https") || loc.Host == "" {
		return ""
	}
	if groundingRedirectHosts[loc.Host] {
		return "" // another hop of the same service: not the page
	}
	return loc.String()
}
