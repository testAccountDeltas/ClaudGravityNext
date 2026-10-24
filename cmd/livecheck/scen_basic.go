package main

import (
	"fmt"
	"strings"
)

func user(text string) map[string]any {
	return map[string]any{"role": "user", "content": text}
}

func need(cond bool, format string, a ...any) error {
	if cond {
		return nil
	}
	return fmt.Errorf(format, a...)
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func basicScenarios() []scenario {
	return []scenario{
		{"models_list", func(t *target) error {
			resp, err := t.do("GET", "/v1/models", nil, nil)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			return need(resp.StatusCode == 200, "GET /v1/models: HTTP %d", resp.StatusCode)
		}},
		{"basic_nonstream", func(t *target) error { return basicCheck(t, false) }},
		{"basic_stream", func(t *target) error { return basicCheck(t, true) }},
		{"stop_sequence_stream", func(t *target) error { return stopSeq(t, true) }},
		{"stop_sequence_nonstream", func(t *target) error { return stopSeq(t, false) }},
		{"max_tokens_cap", func(t *target) error {
			m, err := t.messages(map[string]any{"max_tokens": 6,
				"messages": []any{user("Write a long story about a dragon.")}}, true)
			if err != nil {
				return err
			}
			return firstErr(need(m.StopReason == "max_tokens", "stop_reason=%q, want max_tokens", m.StopReason),
				need(m.OutputTokens <= 12, "output_tokens=%v far above max_tokens 6", m.OutputTokens))
		}},
		{"system_prompt", func(t *target) error {
			m, err := t.messages(map[string]any{
				"system":   "The secret code is ZEBRA-7. When the user asks for the code, reply with only the code.",
				"messages": []any{user("What is the code?")}}, false)
			if err != nil {
				return err
			}
			return need(strings.Contains(m.Text, "ZEBRA-7"), "system prompt ignored, got %q", m.Text)
		}},
		{"multi_turn_memory", func(t *target) error {
			m, err := t.messages(map[string]any{"messages": []any{
				user("My lucky number is 8731. Just say OK."),
				map[string]any{"role": "assistant", "content": "OK"},
				user("What is my lucky number? Reply with the number only."),
			}}, true)
			if err != nil {
				return err
			}
			return need(strings.Contains(m.Text, "8731"), "lost earlier turn, got %q", m.Text)
		}},
		{"thinking_enabled", func(t *target) error {
			m, err := t.messages(map[string]any{"max_tokens": 2048,
				"thinking": map[string]any{"type": "enabled", "budget_tokens": 1024},
				"messages": []any{user("What is 17 * 23? Answer with the number only.")}}, true)
			if err != nil {
				return err
			}
			return need(strings.Contains(m.Text, "391"), "wrong answer with thinking on: %q", m.Text)
		}},
		{"long_stream_complete", func(t *target) error {
			m, err := t.messages(map[string]any{"max_tokens": 2000,
				"messages": []any{user("Print the integers from 1 to 150 separated by single spaces. Nothing else.")}}, true)
			if err != nil {
				return err
			}
			return firstErr(need(m.StopReason == "end_turn", "stop_reason=%q", m.StopReason),
				need(strings.Contains(m.Text, "149") && strings.Contains(m.Text, "150"), "output truncated: ...%q", tail(m.Text, 40)))
		}},
	}
}

func basicCheck(t *target, stream bool) error {
	m, err := t.messages(map[string]any{"messages": []any{user("Reply with exactly the word: PONG")}}, stream)
	if err != nil {
		return err
	}
	if err := firstErr(
		need(strings.Contains(strings.ToUpper(m.Text), "PONG"), "text=%q", m.Text),
		need(m.StopReason == "end_turn", "stop_reason=%q", m.StopReason),
		need(m.InputTokens > 0 && m.OutputTokens > 0, "usage in=%v out=%v", m.InputTokens, m.OutputTokens),
		need(strings.HasPrefix(fmt.Sprint(m.Raw["id"]), "msg_"), "id=%v", m.Raw["id"]),
	); err != nil {
		return err
	}
	if stream {
		ev := m.Events
		return firstErr(need(len(ev) > 3 && ev[0] == "message_start", "first event %v", ev),
			need(ev[len(ev)-2] == "message_delta" && ev[len(ev)-1] == "message_stop", "tail events %v", ev[len(ev)-2:]))
	}
	return nil
}

func stopSeq(t *target, stream bool) error {
	m, err := t.messages(map[string]any{"stop_sequences": []string{"5"},
		"messages": []any{user("Count from 1 to 9, comma separated, nothing else.")}}, stream)
	if err != nil {
		return err
	}
	return firstErr(
		need(m.StopReason == "stop_sequence", "stop_reason=%q (text %q)", m.StopReason, m.Text),
		need(m.StopSequence == "5", "stop_sequence=%q", m.StopSequence),
		need(!strings.Contains(m.Text, "5") && strings.Contains(m.Text, "4"), "text not cut before the sequence: %q", m.Text))
}
