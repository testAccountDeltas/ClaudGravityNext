package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// target is the gateway under test.
type target struct {
	base  string
	key   string
	model string
	http  *http.Client
}

func (t *target) do(method, path string, body any, extra map[string]string) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, t.base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	if t.key != "" {
		req.Header.Set("Authorization", "Bearer "+t.key)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	return t.http.Do(req)
}

// msg is a decoded /v1/messages response (non-stream or reassembled stream).
type msg struct {
	Status       int
	Raw          map[string]any
	Text         string
	Thinking     bool
	ToolUses     []toolUse
	StopReason   string
	StopSequence string
	InputTokens  float64
	OutputTokens float64
	CacheRead    float64
	Events       []string
	Elapsed      time.Duration
}

type toolUse struct {
	ID    string
	Name  string
	Input map[string]any
}

// messages posts a request; stream selects SSE and reassembles it.
func (t *target) messages(body map[string]any, stream bool) (*msg, error) {
	if body["model"] == nil {
		body["model"] = t.model
	}
	if body["max_tokens"] == nil {
		body["max_tokens"] = 512
	}
	if stream {
		body["stream"] = true
	}
	start := time.Now()
	resp, err := t.do("POST", "/v1/messages", body, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	m := &msg{Status: resp.StatusCode}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		m.Raw = map[string]any{}
		_ = json.Unmarshal(b, &m.Raw)
		m.Elapsed = time.Since(start)
		return m, fmt.Errorf("HTTP %d: %.300s", resp.StatusCode, b)
	}
	if !stream {
		var raw map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
			return m, fmt.Errorf("decode: %w", err)
		}
		m.fromMessage(raw)
		m.Elapsed = time.Since(start)
		return m, nil
	}
	err = m.readSSE(resp.Body)
	m.Elapsed = time.Since(start)
	return m, err
}

func (m *msg) fromMessage(raw map[string]any) {
	m.Raw = raw
	m.StopReason, _ = raw["stop_reason"].(string)
	m.StopSequence, _ = raw["stop_sequence"].(string)
	if u, ok := raw["usage"].(map[string]any); ok {
		m.InputTokens, _ = u["input_tokens"].(float64)
		m.OutputTokens, _ = u["output_tokens"].(float64)
		m.CacheRead, _ = u["cache_read_input_tokens"].(float64)
	}
	blocks, _ := raw["content"].([]any)
	for _, b := range blocks {
		blk, _ := b.(map[string]any)
		switch blk["type"] {
		case "text":
			s, _ := blk["text"].(string)
			m.Text += s
		case "thinking":
			m.Thinking = true
		case "tool_use":
			in, _ := blk["input"].(map[string]any)
			id, _ := blk["id"].(string)
			name, _ := blk["name"].(string)
			m.ToolUses = append(m.ToolUses, toolUse{ID: id, Name: name, Input: in})
		}
	}
}

// readSSE reassembles a streamed message into the same shape as a plain one.
func (m *msg) readSSE(r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	blocks := map[int]map[string]any{}
	partial := map[int]*strings.Builder{}
	var order []int
	final := map[string]any{"type": "message", "role": "assistant"}
	usage := map[string]any{}
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event: ") {
			m.Events = append(m.Events, strings.TrimPrefix(line, "event: "))
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line[6:]), &ev); err != nil {
			return fmt.Errorf("bad SSE data %q: %w", line, err)
		}
		switch ev["type"] {
		case "error":
			return fmt.Errorf("stream error event: %v", ev["error"])
		case "message_start":
			if mm, ok := ev["message"].(map[string]any); ok {
				final["id"], final["model"] = mm["id"], mm["model"]
				if u, ok := mm["usage"].(map[string]any); ok {
					for k, v := range u {
						usage[k] = v
					}
				}
			}
		case "content_block_start":
			i := int(ev["index"].(float64))
			cb, _ := ev["content_block"].(map[string]any)
			blocks[i] = cb
			partial[i] = &strings.Builder{}
			order = append(order, i)
		case "content_block_delta":
			i := int(ev["index"].(float64))
			d, _ := ev["delta"].(map[string]any)
			switch d["type"] {
			case "text_delta":
				s, _ := d["text"].(string)
				cur, _ := blocks[i]["text"].(string)
				blocks[i]["text"] = cur + s
			case "input_json_delta":
				s, _ := d["partial_json"].(string)
				partial[i].WriteString(s)
			}
		case "content_block_stop":
			i := int(ev["index"].(float64))
			if blocks[i]["type"] == "tool_use" {
				var in map[string]any
				if s := partial[i].String(); s != "" {
					if err := json.Unmarshal([]byte(s), &in); err != nil {
						return fmt.Errorf("tool_use input is not valid JSON: %w (%q)", err, s)
					}
				}
				blocks[i]["input"] = in
			}
		case "message_delta":
			if d, ok := ev["delta"].(map[string]any); ok {
				final["stop_reason"] = d["stop_reason"]
				final["stop_sequence"] = d["stop_sequence"]
			}
			if u, ok := ev["usage"].(map[string]any); ok {
				for k, v := range u {
					usage[k] = v
				}
			}
			if cm, ok := ev["context_management"]; ok {
				final["context_management"] = cm
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if len(m.Events) == 0 || m.Events[len(m.Events)-1] != "message_stop" {
		return fmt.Errorf("stream did not end with message_stop (events: %v)", m.Events)
	}
	content := make([]any, 0, len(order))
	for _, i := range order {
		content = append(content, blocks[i])
	}
	final["content"], final["usage"] = content, usage
	m.fromMessage(final)
	return nil
}
