// Part of the websearch package's port from superrelay (v1)'s
// websearch.go — see search.go for the split rationale and the
// dependency-inversion changes.
package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"

	"claudgravity/internal/warmup"
)

func searchWithGoogleGrounding(
	ctx context.Context,
	query string,
	googleToken string,
	projectID string,
	httpClient *http.Client,
	antigravityUserAgent string,
) ([]WebSearchResultItem, string, error) {
	geminiReq := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"role": "user",
				"parts": []map[string]interface{}{
					{"text": "Search the web and provide current factual information for: " + query},
				},
			},
		},
		"tools": []map[string]interface{}{
			{"googleSearch": map[string]interface{}{}},
		},
	}
	reqBytes, err := json.Marshal(geminiReq)
	if err != nil {
		return nil, "", err
	}

	wrapped := warmup.WrapAntigravityPayload("gemini-2.5-flash", reqBytes, projectID, 0, "")

	// Previously pinned to the sandbox host only — a location-gated or
	// transiently 429'd sandbox response fell straight through to the
	// Wikipedia/DuckDuckGo fallback without ever trying the other host,
	// unlike every regular request (the orchestrator's retry loop), which
	// already treats this exact host list as one pool to fail over across.
	var respBytes []byte
	var lastErr error
	for _, host := range antigravityInferenceHosts {
		endpointURL := host + "/v1internal:generateContent"

		httpReq, err := http.NewRequestWithContext(ctx, "POST", endpointURL, bytes.NewReader(wrapped))
		if err != nil {
			return nil, "", err
		}
		httpReq.Header.Set("Authorization", "Bearer "+googleToken)
		httpReq.Header.Set("Content-Type", "application/json; charset=utf-8")
		httpReq.Header.Set("User-Agent", antigravityUserAgent)
		httpReq.Header.Set("X-Goog-Api-Client", "google-cloud-sdk vscode_cloudshelleditor/0.1")
		httpReq.Header.Set("Client-Metadata", `{"ideType":"IDE_UNSPECIFIED","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}`)

		resp, err := httpClient.Do(httpReq)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			errText := string(body)
			lastErr = fmt.Errorf("Google Grounding status %d: %s", resp.StatusCode, errText)
			if warmup.IsAntigravityLocationGateError(resp.StatusCode, errText) {
				continue
			}
			return nil, "", lastErr
		}
		respBytes = body
		lastErr = nil
		break
	}
	if respBytes == nil {
		if lastErr == nil {
			lastErr = fmt.Errorf("Google Grounding: no hosts available")
		}
		return nil, "", lastErr
	}

	root := gjson.ParseBytes(respBytes)
	if respNode := root.Get("response"); respNode.Exists() {
		root = respNode
	}

	cand := root.Get("candidates.0")
	if !cand.Exists() {
		return nil, "", fmt.Errorf("no candidates in Google Grounding response")
	}

	var sb strings.Builder
	parts := cand.Get("content.parts")
	if parts.IsArray() {
		for _, p := range parts.Array() {
			if !p.Get("thought").Bool() {
				t := p.Get("text").String()
				if t != "" {
					sb.WriteString(t)
				}
			}
		}
	}
	summaryText := strings.TrimSpace(sb.String())

	// groundingSupports links specific sentences of summaryText back to the
	// specific chunk(s) that back them (segment.text + groundingChunkIndices) —
	// without this, every result's snippet was the same first-300-chars of
	// the WHOLE summary regardless of which source it actually came from.
	chunkSnippets := make(map[int]string)
	if supports := cand.Get("groundingMetadata.groundingSupports"); supports.IsArray() {
		for _, sup := range supports.Array() {
			segText := strings.TrimSpace(sup.Get("segment.text").String())
			if segText == "" {
				continue
			}
			sup.Get("groundingChunkIndices").ForEach(func(_, idx gjson.Result) bool {
				i := int(idx.Int())
				if existing, ok := chunkSnippets[i]; ok {
					chunkSnippets[i] = existing + " " + segText
				} else {
					chunkSnippets[i] = segText
				}
				return true
			})
		}
	}

	var results []WebSearchResultItem
	if chunks := cand.Get("groundingMetadata.groundingChunks"); chunks.IsArray() {
		seen := make(map[string]bool)
		for i, c := range chunks.Array() {
			uri := c.Get("web.uri").String()
			title := c.Get("web.title").String()
			if uri != "" && !seen[uri] {
				seen[uri] = true
				if title == "" {
					title = uri
				}
				snippet, ok := chunkSnippets[i]
				if !ok || snippet == "" {
					snippet = summaryText
				}
				if len([]rune(snippet)) > 300 {
					snippet = string([]rune(snippet)[:300]) + "..."
				}
				results = append(results, WebSearchResultItem{
					Title:   title,
					URL:     uri,
					Snippet: snippet,
				})
			}
		}
	}

	if len(results) == 0 && summaryText != "" {
		results = append(results, WebSearchResultItem{
			Title:   "Web Search: " + query,
			URL:     "https://www.google.com/search?q=" + url.QueryEscape(query),
			Snippet: summaryText,
		})
	}

	return results, summaryText, nil
}
