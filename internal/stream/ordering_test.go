package stream

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/reqctx"
)

func orderParams(session string, stop ...string) *reqctx.Params {
	return &reqctx.Params{ModelName: "claude-opus-4-5", SessionID: session, StopSequences: stop}
}

func TestOrdering_ThinkingAfterToolUseIsDropped(t *testing.T) {
	_, body := feedChunks(orderParams("ord-think-after-tool"),
		chunk(`{"functionCall":{"name":"Read","args":{"file_path":"/a"}}}`, ""),
		chunk(`{"thought":true,"text":"late musing"}`, "STOP"))
	if got := blockSummary(body); len(got) != 1 || got[0] != "tool_use:Read" {
		t.Fatalf("blocks = %v: %s", got, body)
	}
	if strings.Contains(body, "late musing") {
		t.Fatal("thinking after the tool call leaked")
	}
	if r, _ := stopOf(body); r != "tool_use" {
		t.Fatalf("stop = %s", r)
	}
}

func TestOrdering_TextToolTrailingText_HoldBufferFlushedBeforeToolUse(t *testing.T) {
	// A stop sequence keeps a tail of the text in the hold-back buffer.
	_, body := feedChunks(orderParams("ord-hold-flush", "ZZZZZZ"),
		chunk(`{"text":"Let me check that file"}`, ""),
		chunk(`{"functionCall":{"name":"Read","args":{"file_path":"/a"}}}`, ""),
		chunk(`{"text":"trailing words after the call"}`, "STOP"))
	if got := textOf(body); got != "Let me check that file" {
		t.Fatalf("held text must be fully flushed, trailing text dropped: %q", got)
	}
	blocks := blockSummary(body)
	if len(blocks) != 2 || blocks[0] != "text" || blocks[1] != "tool_use:Read" {
		t.Fatalf("blocks = %v", blocks)
	}
	names := eventNames(body)
	if names[len(names)-1] != "message_stop" {
		t.Fatalf("events = %v", names)
	}
	if r, _ := stopOf(body); r != "tool_use" {
		t.Fatalf("stop = %s", r)
	}
}

func TestOrdering_ParallelToolCalls_InheritTheTurnSignature(t *testing.T) {
	_, body := feedChunks(orderParams("ord-parallel-sig"),
		chunk(`{"thoughtSignature":"sig-turn-1","functionCall":{"name":"Read","args":{"file_path":"/a"}}},`+
			`{"functionCall":{"name":"Grep","args":{"pattern":"x"}}}`, "STOP"))
	var ids []string
	for _, e := range parseEvents(body) {
		if e.Name == "content_block_start" && gjson.Get(e.Data, "content_block.type").String() == "tool_use" {
			ids = append(ids, gjson.Get(e.Data, "content_block.id").String())
		}
	}
	if len(ids) != 2 {
		t.Fatalf("want 2 parallel tool_use blocks, got %d: %s", len(ids), body)
	}
	if ids[0] == ids[1] {
		t.Fatal("tool_use ids must be unique")
	}
	for _, id := range ids {
		if got := getToolSignature(id); got != "sig-turn-1" {
			t.Fatalf("tool %s signature = %q, want the turn's sig-turn-1", id, got)
		}
	}
}

func TestOrdering_DisableParallelKeepsOnlyTheFirstCall(t *testing.T) {
	p := orderParams("ord-no-parallel")
	p.DisableParallelToolUse = true
	_, body := feedChunks(p, chunk(`{"functionCall":{"name":"Read","args":{"file_path":"/a"}}},{"functionCall":{"name":"Grep","args":{"pattern":"x"}}}`, "STOP"))
	if got := blockSummary(body); len(got) != 1 || got[0] != "tool_use:Read" {
		t.Fatalf("blocks = %v", got)
	}
}

func TestOrdering_UnparseableArgsStillProduceValidPartialJSON(t *testing.T) {
	cases := map[string]string{
		"missing args":     `{"functionCall":{"name":"Bash"}}`,
		"null args":        `{"functionCall":{"name":"Bash","args":null}}`,
		"truncated string": `{"functionCall":{"name":"Bash","args":"{\"command\": \"ls -"}}`,
		"plain string":     `{"functionCall":{"name":"Bash","args":"just words"}}`,
		"array":            `{"functionCall":{"name":"Bash","args":[1,2,3]}}`,
		"number":           `{"functionCall":{"name":"Bash","args":42}}`,
	}
	for name, part := range cases {
		t.Run(name, func(t *testing.T) {
			_, body := feedChunks(orderParams("ord-args-"+name), chunk(part, "STOP"))
			var pj string
			for _, e := range parseEvents(body) {
				if gjson.Get(e.Data, "delta.type").String() == "input_json_delta" {
					pj += gjson.Get(e.Data, "delta.partial_json").String()
				}
			}
			if !gjson.Valid(pj) || !gjson.Parse(pj).IsObject() {
				t.Fatalf("partial_json %q must be a valid JSON object (tool_use.input is an object)", pj)
			}
			if r, _ := stopOf(body); r != "tool_use" {
				t.Fatalf("stop = %s: %s", r, body)
			}
		})
	}
}

func TestOrdering_InterleavedThinkingGetsItsOwnBlockAndNeverLeaksIntoText(t *testing.T) {
	_, body := feedChunks(orderParams("ord-think-twice"),
		chunk(`{"thought":true,"text":"first"}`, ""),
		chunk(`{"text":"answer"}`, ""),
		chunk(`{"thought":true,"text":"second"}`, ""),
		chunk(`{"text":" more"}`, "STOP"))
	if got := strings.Join(blockSummary(body), ","); got != "thinking,text,thinking,text" {
		t.Fatalf("blocks = %s", got)
	}
	if textOf(body) != "answer more" {
		t.Fatalf("reasoning leaked into the visible text: %q", textOf(body))
	}
}
