// Part of the websearch package's port from superrelay (v1)'s
// websearch.go — see search.go for the split rationale and the
// dependency-inversion changes.
package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/tidwall/gjson"

	"claudgravity/internal/accounts"
	"claudgravity/internal/logx"
	"claudgravity/internal/ratelimit"
	"claudgravity/internal/requestlog"
)

func sendSSEEvent(w http.ResponseWriter, flusher http.Flusher, event string, data interface{}) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("event: %s\ndata: %s\n\n", event, string(b))
	_, err = w.Write([]byte(msg))
	if err == nil && flusher != nil {
		flusher.Flush()
	}
	return err
}

// HandleClaudeCodeDirectWebSearch fulfills Claude Code's native search
// sub-request. See search.go's doc comment for why hasActiveAccount/
// projectID/httpClient/antigravityUserAgent replace v1's *AccountRecord
// and package-level globals, and why the dead `tenant TenantConfig`
// parameter was dropped.
func HandleClaudeCodeDirectWebSearch(
	baseCtx context.Context,
	rid uint64,
	t0 time.Time,
	clientKey string,
	accLabel string,
	googleToken string,
	isAntigravity bool,
	hasActiveAccount bool,
	projectID string,
	httpClient *http.Client,
	antigravityUserAgent string,
	bodyBytes []byte,
	query string,
	w http.ResponseWriter,
	activeAcc *accounts.Record,
) {
	ctx, cancel := context.WithTimeout(baseCtx, 25*time.Second)
	defer cancel()

	results, summaryText, err := PerformWebSearch(ctx, query, googleToken, isAntigravity, hasActiveAccount, projectID, httpClient, antigravityUserAgent)
	if err != nil {
		logx.LogGateway("[#%d] !! WebSearch execution error: %v", rid, err)
		results = []WebSearchResultItem{
			{
				Title:   "Search Error",
				URL:     "https://www.google.com/search?q=" + url.QueryEscape(query),
				Snippet: fmt.Sprintf("Web search error: %v", err),
			},
		}
		summaryText = fmt.Sprintf("Unable to complete search for %q: %v", query, err)
	}

	modelName := gjson.GetBytes(bodyBytes, "model").String()
	if modelName == "" {
		modelName = "claude-3-5-haiku-20241022"
	}
	isStream := gjson.GetBytes(bodyBytes, "stream").Bool()
	hasSafeguards := gjson.GetBytes(bodyBytes, "safeguards").Exists()
	msgID := fmt.Sprintf("msg_%d", time.Now().UnixNano())
	srvToolID := newServerToolUseID()

	// Format result blocks for Claude Code
	searchResultBlocks := make([]map[string]interface{}, 0, len(results))
	for _, r := range results {
		searchResultBlocks = append(searchResultBlocks, map[string]interface{}{
			"type":              "web_search_result",
			"title":             r.Title,
			"url":               r.URL,
			"snippet":           r.Snippet,
			"encrypted_content": r.Snippet,
			"page_age":          nil,
		})
	}

	inputTokens := int64(len(query)/4 + 30)
	outputTokens := int64(len(summaryText)/4 + len(results)*35 + 40)

	flusher, hasFlusher := w.(http.Flusher)
	if isStream && hasFlusher {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		// An audit caught that this fast-path response never set the
		// anthropic-ratelimit-* headers the normal /v1/messages path sets
		// via orchestrate/streaming.go's commitHeaders() — Claude Code's
		// own quota/usage indicator reads those headers on every response,
		// including this one, and silently glitches/resets without them.
		ratelimit.ApplyHeaders(w.Header(), activeAcc, modelName)

		// 1. message_start
		_ = sendSSEEvent(w, flusher, "message_start", map[string]interface{}{
			"type": "message_start",
			"message": map[string]interface{}{
				"id":            msgID,
				"type":          "message",
				"role":          "assistant",
				"model":         modelName,
				"content":       []interface{}{},
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage": map[string]interface{}{
					"input_tokens":                inputTokens,
					"output_tokens":               0,
					"cache_creation_input_tokens": 0,
					"cache_read_input_tokens":     0,
				},
			},
		})

		// 2. content_block 0: server_tool_use
		_ = sendSSEEvent(w, flusher, "content_block_start", map[string]interface{}{
			"type":  "content_block_start",
			"index": 0,
			"content_block": map[string]interface{}{
				"type":  "server_tool_use",
				"id":    srvToolID,
				"name":  "web_search",
				"input": map[string]interface{}{},
			},
		})

		queryJSON, _ := json.Marshal(map[string]interface{}{"query": query})
		_ = sendSSEEvent(w, flusher, "content_block_delta", map[string]interface{}{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]interface{}{
				"type":         "input_json_delta",
				"partial_json": string(queryJSON),
			},
		})

		_ = sendSSEEvent(w, flusher, "content_block_stop", map[string]interface{}{
			"type":  "content_block_stop",
			"index": 0,
		})

		// 3. content_block 1: web_search_tool_result
		_ = sendSSEEvent(w, flusher, "content_block_start", map[string]interface{}{
			"type":  "content_block_start",
			"index": 1,
			"content_block": map[string]interface{}{
				"type":        "web_search_tool_result",
				"tool_use_id": srvToolID,
				"content":     searchResultBlocks,
			},
		})

		_ = sendSSEEvent(w, flusher, "content_block_stop", map[string]interface{}{
			"type":  "content_block_stop",
			"index": 1,
		})

		// 4. content_block 2: text summary
		_ = sendSSEEvent(w, flusher, "content_block_start", map[string]interface{}{
			"type":  "content_block_start",
			"index": 2,
			"content_block": map[string]interface{}{
				"type": "text",
				"text": "",
			},
		})

		runes := []rune(summaryText)
		chunkSize := 100
		for i := 0; i < len(runes); i += chunkSize {
			end := i + chunkSize
			if end > len(runes) {
				end = len(runes)
			}
			_ = sendSSEEvent(w, flusher, "content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": 2,
				"delta": map[string]interface{}{
					"type": "text_delta",
					"text": string(runes[i:end]),
				},
			})
		}

		_ = sendSSEEvent(w, flusher, "content_block_stop", map[string]interface{}{
			"type":  "content_block_stop",
			"index": 2,
		})

		// 5. message_delta
		deltaMap := map[string]interface{}{
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
		}
		if hasSafeguards {
			deltaMap["safeguard_results"] = []interface{}{
				map[string]interface{}{
					"type": "dangerous_tool_use",
					"status": map[string]interface{}{
						"type": "available",
						"tool_uses": map[string]interface{}{
							srvToolID: map[string]interface{}{
								"type":    "evaluated",
								"outcome": "not_flagged",
							},
						},
					},
				},
			}
		}

		_ = sendSSEEvent(w, flusher, "message_delta", map[string]interface{}{
			"type":  "message_delta",
			"delta": deltaMap,
			"usage": map[string]interface{}{
				"input_tokens":                inputTokens,
				"output_tokens":               outputTokens,
				"cache_creation_input_tokens": 0,
				"cache_read_input_tokens":     0,
				"server_tool_use": map[string]interface{}{
					"web_search_requests": 1,
				},
			},
		})

		// 6. message_stop
		_ = sendSSEEvent(w, flusher, "message_stop", map[string]interface{}{"type": "message_stop"})
	} else {
		// Non-streaming JSON response
		content := []map[string]interface{}{
			{
				"type":  "server_tool_use",
				"id":    srvToolID,
				"name":  "web_search",
				"input": map[string]interface{}{"query": query},
			},
			{
				"type":        "web_search_tool_result",
				"tool_use_id": srvToolID,
				"content":     searchResultBlocks,
			},
			{
				"type": "text",
				"text": summaryText,
			},
		}

		respMap := map[string]interface{}{
			"id":            msgID,
			"type":          "message",
			"role":          "assistant",
			"model":         modelName,
			"content":       content,
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
			"usage": map[string]interface{}{
				"input_tokens":                inputTokens,
				"output_tokens":               outputTokens,
				"cache_creation_input_tokens": 0,
				"cache_read_input_tokens":     0,
				"server_tool_use": map[string]interface{}{
					"web_search_requests": 1,
				},
			},
		}
		if hasSafeguards {
			respMap["safeguard_results"] = []interface{}{
				map[string]interface{}{
					"type": "dangerous_tool_use",
					"status": map[string]interface{}{
						"type": "available",
						"tool_uses": map[string]interface{}{
							srvToolID: map[string]interface{}{
								"type":    "evaluated",
								"outcome": "not_flagged",
							},
						},
					},
				},
			}
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		ratelimit.ApplyHeaders(w.Header(), activeAcc, modelName)
		_ = json.NewEncoder(w).Encode(respMap)
	}

	dur := time.Since(t0).Milliseconds()
	logx.LogGateway("[#%d] << WebSearch finished: query=%q results=%d dur=%dms", rid, query, len(results), dur)
	requestlog.Add(requestlog.RequestLog{
		Timestamp:    t0,
		Key:          clientKey,
		KeyMasked:    requestlog.MaskKey(clientKey),
		Account:      accLabel,
		SessionID:    "websearch",
		AgentKey:     "web_search",
		AgentType:    "web_search",
		SubagentName: "",
		Model:        modelName,
		TTFT:         dur,
		TotalDur:     dur,
		WasPanicked:  false,
		Shrunk:       false,
		Status:       http.StatusOK,
	})
}
