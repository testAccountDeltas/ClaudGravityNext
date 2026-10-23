// Part of the websearch package's port from superrelay (v1)'s
// websearch.go — see search.go for the split rationale and the
// dependency-inversion changes.
package websearch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"
)

func searchWithPublicFallback(ctx context.Context, query string, httpClient *http.Client) ([]WebSearchResultItem, string, error) {
	// Query Wikipedia API for general knowledge
	wikiURL := fmt.Sprintf("https://en.wikipedia.org/w/api.php?action=query&list=search&srsearch=%s&utf8=&format=json", url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, "GET", wikiURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "SuperRelay/1.0 (WebSearchFallback)")

	resp, err := httpClient.Do(req)
	if err == nil && resp.StatusCode == http.StatusOK {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		root := gjson.ParseBytes(body)
		items := root.Get("query.search")
		if items.IsArray() && len(items.Array()) > 0 {
			var results []WebSearchResultItem
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("Search results for %q:\n\n", query))
			for i, item := range items.Array() {
				if i >= 5 {
					break
				}
				title := item.Get("title").String()
				snippet := item.Get("snippet").String()
				cleanSnippet := stripHTMLTags(snippet)
				pageURL := fmt.Sprintf("https://en.wikipedia.org/wiki/%s", url.PathEscape(title))
				results = append(results, WebSearchResultItem{
					Title:   title,
					URL:     pageURL,
					Snippet: cleanSnippet,
				})
				sb.WriteString(fmt.Sprintf("%d. **%s**: %s (%s)\n\n", i+1, title, cleanSnippet, pageURL))
			}
			return results, strings.TrimSpace(sb.String()), nil
		}
	}

	// Minimal fallback if all else fails
	results := []WebSearchResultItem{
		{
			Title:   "Google Search for: " + query,
			URL:     "https://www.google.com/search?q=" + url.QueryEscape(query),
			Snippet: "Search query performed for: " + query,
		},
	}
	return results, fmt.Sprintf("Search completed for query: %q", query), nil
}

func stripHTMLTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
		} else if r == '>' {
			inTag = false
		} else if !inTag {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}
