package main

import (
	"fmt"
	"strings"
)

var weatherTool = map[string]any{
	"name":        "get_weather",
	"description": "Get the current temperature in a city, in Celsius.",
	"input_schema": map[string]any{
		"type":       "object",
		"properties": map[string]any{"city": map[string]any{"type": "string"}},
		"required":   []string{"city"},
	},
}

// typedTool has one parameter of each JSON type, to catch a gateway or
// upstream that turns numbers/booleans/arrays into strings.
var typedTool = map[string]any{
	"name":        "record_entry",
	"description": "Record an entry. Always call this tool with every field filled in.",
	"input_schema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title":    map[string]any{"type": "string"},
			"count":    map[string]any{"type": "integer"},
			"ratio":    map[string]any{"type": "number"},
			"urgent":   map[string]any{"type": "boolean"},
			"tags":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"priority": map[string]any{"type": "string", "enum": []string{"low", "high"}},
		},
		"required": []string{"title", "count", "ratio", "urgent", "tags", "priority"},
	},
}

func toolScenarios() []scenario {
	return []scenario{
		{"tool_roundtrip_nonstream", func(t *target) error { return toolRoundtrip(t, false) }},
		{"tool_roundtrip_stream", func(t *target) error { return toolRoundtrip(t, true) }},
		{"tool_parallel_calls", toolParallel},
		{"tool_argument_types", toolTypes},
		{"tool_choice_forced", func(t *target) error { return forcedTool(t, "What is the weather in Rome?", false) }},
		// Gemini ignores toolConfig mode ANY when the prompt has nothing to do
		// with the tool (observed live); that is upstream behaviour, so a soft check.
		{"tool_choice_forced_offtopic", func(t *target) error { return forcedTool(t, "Hello there.", true) }},
		{"tool_result_error", func(t *target) error {
			first, err := t.messages(map[string]any{"tools": []any{weatherTool},
				"messages": []any{user("What is the weather in Paris? Use the tool.")}}, true)
			if err != nil {
				return err
			}
			if len(first.ToolUses) == 0 {
				return fmt.Errorf("no tool call; text=%q", first.Text)
			}
			m, err := t.messages(map[string]any{"tools": []any{weatherTool}, "messages": []any{
				user("What is the weather in Paris? Use the tool."),
				assistantTurn(first),
				map[string]any{"role": "user", "content": []any{map[string]any{
					"type": "tool_result", "tool_use_id": first.ToolUses[0].ID, "is_error": true,
					"content": "service unavailable"}}},
			}}, true)
			if err != nil {
				return err
			}
			return need(m.Text != "" || len(m.ToolUses) > 0, "model produced nothing after an error tool_result")
		}},
	}
}

// assistantTurn rebuilds the assistant message from a response so it can be
// replayed as history (tool_use ids and inputs preserved).
func assistantTurn(m *msg) map[string]any {
	var content []any
	if m.Text != "" {
		content = append(content, map[string]any{"type": "text", "text": m.Text})
	}
	for _, tu := range m.ToolUses {
		content = append(content, map[string]any{"type": "tool_use", "id": tu.ID, "name": tu.Name, "input": tu.Input})
	}
	return map[string]any{"role": "assistant", "content": content}
}

func toolRoundtrip(t *target, stream bool) error {
	q := user("What is the temperature in Paris right now? Use the get_weather tool, then answer in one sentence.")
	first, err := t.messages(map[string]any{"tools": []any{weatherTool}, "messages": []any{q}}, stream)
	if err != nil {
		return err
	}
	if err := firstErr(
		need(first.StopReason == "tool_use", "stop_reason=%q, text=%q", first.StopReason, first.Text),
		need(len(first.ToolUses) == 1, "want 1 tool_use, got %d", len(first.ToolUses)),
	); err != nil {
		return err
	}
	tu := first.ToolUses[0]
	city, _ := tu.Input["city"].(string)
	if err := firstErr(need(tu.Name == "get_weather", "tool name %q", tu.Name),
		need(strings.EqualFold(city, "Paris"), "city=%v", tu.Input["city"]),
		need(strings.HasPrefix(tu.ID, "toolu_"), "tool_use id %q", tu.ID)); err != nil {
		return err
	}
	final, err := t.messages(map[string]any{"tools": []any{weatherTool}, "messages": []any{
		q, assistantTurn(first),
		map[string]any{"role": "user", "content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": tu.ID, "content": "The temperature is 21493 degrees."}}},
	}}, stream)
	if err != nil {
		return fmt.Errorf("second leg: %w", err)
	}
	return firstErr(need(strings.Contains(final.Text, "21493") || strings.Contains(final.Text, "21,493"),
		"final answer does not use the tool result: %q", final.Text),
		need(final.StopReason == "end_turn", "final stop_reason=%q", final.StopReason))
}

func toolParallel(t *target) error {
	q := user("Get the temperature in Paris AND in Tokyo. Call get_weather once per city, both calls in the same turn.")
	first, err := t.messages(map[string]any{"tools": []any{weatherTool}, "messages": []any{q}}, true)
	if err != nil {
		return err
	}
	if len(first.ToolUses) < 2 {
		return fmt.Errorf("want 2 parallel tool calls, got %d (text %q)", len(first.ToolUses), first.Text)
	}
	results := []any{}
	for i, tu := range first.ToolUses {
		results = append(results, map[string]any{"type": "tool_result", "tool_use_id": tu.ID,
			"content": fmt.Sprintf("%v: %d degrees", tu.Input["city"], 10+i*7)})
	}
	final, err := t.messages(map[string]any{"tools": []any{weatherTool}, "messages": []any{
		q, assistantTurn(first), map[string]any{"role": "user", "content": results},
	}}, true)
	if err != nil {
		return fmt.Errorf("second leg: %w", err)
	}
	return need(strings.Contains(final.Text, "10") && strings.Contains(final.Text, "17"),
		"answer lacks both results: %q", final.Text)
}

func toolTypes(t *target) error {
	m, err := t.messages(map[string]any{"tools": []any{typedTool},
		"tool_choice": map[string]any{"type": "tool", "name": "record_entry"},
		"messages":    []any{user("Record an entry titled 'launch', count 3, ratio 0.5, urgent true, tags a and b, priority high.")}}, true)
	if err != nil {
		return err
	}
	if len(m.ToolUses) != 1 {
		return fmt.Errorf("want 1 tool_use, got %d", len(m.ToolUses))
	}
	in := m.ToolUses[0].Input
	_, countNum := in["count"].(float64)
	_, ratioNum := in["ratio"].(float64)
	_, urgentBool := in["urgent"].(bool)
	tags, tagsArr := in["tags"].([]any)
	return firstErr(
		need(countNum && in["count"] == float64(3), "count=%#v (want number 3)", in["count"]),
		need(ratioNum, "ratio=%#v (want number)", in["ratio"]),
		need(urgentBool && in["urgent"] == true, "urgent=%#v (want boolean true)", in["urgent"]),
		need(tagsArr && len(tags) == 2, "tags=%#v (want 2-element array)", in["tags"]),
		need(in["priority"] == "high", "priority=%#v", in["priority"]))
}

func forcedTool(t *target, prompt string, soft bool) error {
	m, err := t.messages(map[string]any{"tools": []any{weatherTool},
		"tool_choice": map[string]any{"type": "tool", "name": "get_weather"},
		"messages":    []any{user(prompt)}}, false)
	if err != nil {
		return err
	}
	err = firstErr(need(len(m.ToolUses) == 1 && m.ToolUses[0].Name == "get_weather", "forced tool not called: text=%q", m.Text),
		need(m.StopReason == "tool_use", "stop_reason=%q", m.StopReason))
	if err != nil && soft {
		return warnError{err.Error()}
	}
	return err
}
