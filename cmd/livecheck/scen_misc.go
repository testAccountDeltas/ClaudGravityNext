package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"strings"
	"time"
)

func miscScenarios() []scenario {
	return []scenario{
		{"count_tokens", func(t *target) error {
			resp, err := t.do("POST", "/v1/messages/count_tokens", map[string]any{"model": t.model,
				"messages": []any{user("How many tokens is this?")}}, nil)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			var out map[string]any
			_ = json.NewDecoder(resp.Body).Decode(&out)
			n, _ := out["input_tokens"].(float64)
			return firstErr(need(resp.StatusCode == 200, "HTTP %d", resp.StatusCode), need(n > 0, "input_tokens=%v", out["input_tokens"]))
		}},
		{"image_input", imageInput},
		{"context_management_edits", contextManagement},
		{"implicit_cache_hit", cacheHit},
		{"error_bad_key", func(t *target) error {
			bad := *t
			bad.key = "sk-definitely-not-a-key"
			return errEnvelope(&bad, "POST", "/v1/messages", map[string]any{"model": t.model, "max_tokens": 8,
				"messages": []any{user("hi")}}, 401, "authentication_error")
		}},
		{"error_invalid_body", func(t *target) error {
			return errEnvelope(t, "POST", "/v1/messages", map[string]any{"model": t.model, "max_tokens": 8}, 400, "invalid_request_error")
		}},
		{"error_unknown_route", func(t *target) error {
			resp, err := t.do("GET", "/v1/definitely-not-a-route", nil, nil)
			if err != nil {
				return err
			}
			resp.Body.Close()
			return need(resp.StatusCode == 404, "HTTP %d, want 404", resp.StatusCode)
		}},
		{"client_abort_then_recover", clientAbort},
	}
}

func redPNG() string {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for x := 0; x < 64; x++ {
		for y := 0; y < 64; y++ {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func imageInput(t *target) error {
	m, err := t.messages(map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": redPNG()}},
		map[string]any{"type": "text", "text": "What single color fills this image? One word."},
	}}}}, false)
	if err != nil {
		return err
	}
	return need(strings.Contains(strings.ToLower(m.Text), "red"), "image not understood: %q", m.Text)
}

// contextManagement checks the gateway-side clear_tool_uses edit: old tool
// results get cleared and the response reports what was applied.
func contextManagement(t *target) error {
	big := strings.Repeat("log line with some filler text to take up room. ", 120)
	msgs := []any{user("Inspect the logs.")}
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("toolu_cm%d", i)
		msgs = append(msgs,
			map[string]any{"role": "assistant", "content": []any{map[string]any{
				"type": "tool_use", "id": id, "name": "get_weather", "input": map[string]any{"city": fmt.Sprintf("City%d", i)}}}},
			map[string]any{"role": "user", "content": []any{map[string]any{
				"type": "tool_result", "tool_use_id": id, "content": big}}})
	}
	msgs = append(msgs, map[string]any{"role": "assistant", "content": "Looked at them."}, user("Reply with OK."))
	m, err := t.messages(map[string]any{"tools": []any{weatherTool}, "messages": msgs,
		"context_management": map[string]any{"edits": []any{map[string]any{
			"type": "clear_tool_uses_20250919", "trigger": map[string]any{"type": "input_tokens", "value": 1000},
			"keep": map[string]any{"type": "tool_uses", "value": 1}}}}}, true)
	if err != nil {
		return err
	}
	cm, _ := m.Raw["context_management"].(map[string]any)
	applied, _ := cm["applied_edits"].([]any)
	if len(applied) == 0 {
		return fmt.Errorf("no applied_edits reported: %v", m.Raw["context_management"])
	}
	e, _ := applied[0].(map[string]any)
	return firstErr(need(e["type"] == "clear_tool_uses_20250919", "applied edit %v", e),
		need(fmt.Sprint(e["cleared_tool_uses"]) == "3", "cleared_tool_uses=%v, want 3 (keep 1 of 4)", e["cleared_tool_uses"]))
}

// cacheHit is a soft check: Gemini's implicit cache is upstream behaviour.
func cacheHit(t *target) error {
	nonce := fmt.Sprintf("run-%d", time.Now().UnixNano())
	system := "Reference notes (" + nonce + "): " + strings.Repeat("The quick brown fox jumps over the lazy dog near the river bank. ", 700)
	ask := func() (*msg, error) {
		return t.messages(map[string]any{"max_tokens": 16, "system": system,
			"messages": []any{user("Reply with the single word: ready")}}, false)
	}
	if _, err := ask(); err != nil {
		return err
	}
	time.Sleep(3 * time.Second)
	m, err := ask()
	if err != nil {
		return err
	}
	if m.CacheRead <= 0 {
		return warnError{fmt.Sprintf("no cache_read_input_tokens on repeat (input=%v); upstream implicit cache may be cold", m.InputTokens)}
	}
	return nil
}

func errEnvelope(t *target, method, path string, body any, wantStatus int, wantType string) error {
	resp, err := t.do(method, path, body, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)
	return firstErr(need(resp.StatusCode == wantStatus, "HTTP %d, want %d (%.200s)", resp.StatusCode, wantStatus, raw),
		need(out.Type == "error" && out.Error.Type == wantType, "envelope %.200s, want error/%s", raw, wantType),
		need(out.Error.Message != "", "empty error message"))
}

// clientAbort drops a streaming request mid-flight and checks the gateway
// keeps serving normally afterwards.
func clientAbort(t *target) error {
	body, _ := json.Marshal(map[string]any{"model": t.model, "max_tokens": 3000, "stream": true,
		"messages": []any{user("Write a 1200 word essay about the ocean.")}})
	req, _ := http.NewRequest("POST", t.base+"/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Authorization", "Bearer "+t.key)
	resp, err := t.http.Do(req)
	if err != nil {
		return err
	}
	buf := make([]byte, 256)
	_, _ = resp.Body.Read(buf)
	resp.Body.Close() // hang up mid-stream
	time.Sleep(500 * time.Millisecond)
	return basicCheck(t, false)
}
