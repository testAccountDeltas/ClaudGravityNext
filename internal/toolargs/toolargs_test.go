package toolargs

import (
	"encoding/json"
	"strings"
	"testing"
)

// GAP-ARG-01: Gemini commonly emits a literal, unescaped newline inside a
// multi-line string argument (a Bash heredoc, a Write tool's file
// content) — Go's encoding/json correctly rejects that per the JSON
// spec, and the old behavior silently collapsed the whole call to "{}",
// discarding every argument including the required one.
func TestNormalizeToArgsObject_UnescapedNewlineInString_IsRepaired(t *testing.T) {
	raw := "{\"command\":\"echo line1\nline2\"}"
	out := NormalizeToArgsObject("Bash", raw)
	var v map[string]interface{}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("expected repaired JSON to parse, got %q: %v", out, err)
	}
	cmd, _ := v["command"].(string)
	if cmd != "echo line1\nline2" {
		t.Fatalf("expected command to survive with its real newline, got %q", cmd)
	}
}

func TestNormalizeToArgsObject_UnescapedCRLFAndTab_IsRepaired(t *testing.T) {
	raw := "{\"content\":\"a\tb\r\nc\"}"
	out := NormalizeToArgsObject("Write", raw)
	var v map[string]interface{}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("expected repaired JSON to parse, got %q: %v", out, err)
	}
	if v["content"] != "a\tb\r\nc" {
		t.Fatalf("expected content preserved, got %q", v["content"])
	}
}

func TestNormalizeToArgsObject_GenuinelyMalformedJSON_FallsBackToEmptyObject(t *testing.T) {
	// Not a control-character issue — just broken JSON. Must still fall
	// back to "{}" rather than panic or return garbage.
	out := NormalizeToArgsObject("Bash", `{"command": not valid json`)
	if out != "{}" {
		t.Fatalf("expected fallback to {}, got %q", out)
	}
}

func TestNormalizeToArgsObject_PlainObject_Unchanged(t *testing.T) {
	raw := `{"command":"ls -la"}`
	if out := NormalizeToArgsObject("Bash", raw); out != raw {
		t.Fatalf("expected a plain already-valid object to pass through unchanged, got %q", out)
	}
}

func TestNormalizeToArgsObject_BareStringMapsToPrimaryParam(t *testing.T) {
	out := NormalizeToArgsObject("Bash", `"git status"`)
	var v map[string]interface{}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("expected valid JSON, got %q: %v", out, err)
	}
	if v["command"] != "git status" {
		t.Fatalf("expected bare string mapped onto 'command', got %v", v)
	}

	searchOut := NormalizeToArgsObject("web_search", `"golang 1.24 release"`)
	var sv map[string]interface{}
	if err := json.Unmarshal([]byte(searchOut), &sv); err != nil || sv["query"] != "golang 1.24 release" {
		t.Fatalf("expected web_search mapped to 'query', got %s", searchOut)
	}

	fetchOut := NormalizeToArgsObject("web_fetch", `"https://example.com"`)
	var fv map[string]interface{}
	if err := json.Unmarshal([]byte(fetchOut), &fv); err != nil || fv["url"] != "https://example.com" {
		t.Fatalf("expected web_fetch mapped to 'url', got %s", fetchOut)
	}
}

func TestNormalizeToArgsObject_LineContinuation_IsRepaired(t *testing.T) {
	// Model emits backslash followed by literal newline (line continuation) inside a string literal
	raw := "{\"command\":\"echo line1 \\\nline2\"}"
	out := NormalizeToArgsObject("Bash", raw)
	var v map[string]interface{}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("expected repaired JSON to parse, got %q: %v", out, err)
	}
	cmd, _ := v["command"].(string)
	if !strings.Contains(cmd, "line1") || !strings.Contains(cmd, "line2") {
		t.Fatalf("expected line continuation repaired into valid string, got %q", cmd)
	}
}

func TestNormalizeToArgsObject_UnwrapsScalarArg(t *testing.T) {
	// Custom tool with bare string schema emits {"__scalar_arg__": "SELECT * FROM users"}
	// Anthropic wire specification requires tool_use.input to be a JSON object, so unwraps to {"value": ...}
	sqlOut := NormalizeToArgsObject("sql_query", `{"__scalar_arg__":"SELECT * FROM users"}`)
	if sqlOut != `{"value":"SELECT * FROM users"}` {
		t.Fatalf("expected object-wrapped scalar, got %s", sqlOut)
	}

	// Tool with primary parameter (e.g. Bash) wraps to {"command": "git pull"}
	bashOut := NormalizeToArgsObject("Bash", `{"__scalar_arg__":"git pull"}`)
	var bv map[string]interface{}
	if err := json.Unmarshal([]byte(bashOut), &bv); err != nil || bv["command"] != "git pull" {
		t.Fatalf("expected primary param mapping for Bash, got %s", bashOut)
	}

	// Numeric scalar wraps to {"value": 42}
	numOut := NormalizeToArgsObject("get_status", `{"__scalar_arg__":42}`)
	if numOut != `{"value":42}` {
		t.Fatalf("expected object-wrapped number, got %s", numOut)
	}

	// Array scalar wraps to {"value": ["id1","id2"]}
	arrOut := NormalizeToArgsObject("batch_ids", `{"__scalar_arg__":["id1","id2"]}`)
	if arrOut != `{"value":["id1","id2"]}` {
		t.Fatalf("expected object-wrapped array, got %s", arrOut)
	}

	// Object wrapped in scalar
	objOut := NormalizeToArgsObject("custom_obj", `{"__scalar_arg__":{"key":"val"}}`)
	var ov map[string]interface{}
	if err := json.Unmarshal([]byte(objOut), &ov); err != nil || ov["key"] != "val" {
		t.Fatalf("expected unwrapped object, got %s", objOut)
	}
}

func TestNormalizeToArgsObject_DatedAndHyphenatedTools(t *testing.T) {
	// Dated tool names
	bashDated := NormalizeToArgsObject("bash_20250124", `"ls -la"`)
	var bv map[string]interface{}
	if err := json.Unmarshal([]byte(bashDated), &bv); err != nil || bv["command"] != "ls -la" {
		t.Fatalf("expected dated bash mapped to 'command', got %s", bashDated)
	}

	// Hyphenated tool names
	searchHyphen := NormalizeToArgsObject("web-search", `"golang 1.25"`)
	var sv map[string]interface{}
	if err := json.Unmarshal([]byte(searchHyphen), &sv); err != nil || sv["query"] != "golang 1.25" {
		t.Fatalf("expected hyphenated web-search mapped to 'query', got %s", searchHyphen)
	}

	fetchHyphen := NormalizeToArgsObject("web-fetch", `"https://example.com"`)
	var fv map[string]interface{}
	if err := json.Unmarshal([]byte(fetchHyphen), &fv); err != nil || fv["url"] != "https://example.com" {
		t.Fatalf("expected hyphenated web-fetch mapped to 'url', got %s", fetchHyphen)
	}

	// Underscored notebook_edit
	nbOut := NormalizeToArgsObject("notebook_edit", `"test.ipynb"`)
	var nv map[string]interface{}
	if err := json.Unmarshal([]byte(nbOut), &nv); err != nil || nv["notebook_path"] != "test.ipynb" {
		t.Fatalf("expected notebook_edit mapped to 'notebook_path', got %s", nbOut)
	}
}

func TestNormalizeToArgsObject_TrailingCommasAndSingleQuotesAndWindowsPaths(t *testing.T) {
	// Trailing comma in object and array
	inTrailing := `{"command": "ls", "files": ["a", "b",],}`
	outTrailing := NormalizeToArgsObject("Bash", inTrailing)
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(outTrailing), &m); err != nil {
		t.Fatalf("expected trailing commas repaired, got error: %v, out: %s", err, outTrailing)
	}
	if m["command"] != "ls" {
		t.Fatalf("expected command ls, got: %v", m["command"])
	}

	// Single quotes JSON
	inSingle := `{'command': 'git status'}`
	outSingle := NormalizeToArgsObject("Bash", inSingle)
	if err := json.Unmarshal([]byte(outSingle), &m); err != nil {
		t.Fatalf("expected single quotes repaired, got error: %v, out: %s", err, outSingle)
	}
	if m["command"] != "git status" {
		t.Fatalf("expected command git status, got: %v", m["command"])
	}

	// Windows path with unescaped backslashes (\P, \t, etc.)
	inWin := `{"file_path": "C:\Projects\test\app.go"}`
	outWin := NormalizeToArgsObject("Read", inWin)
	if err := json.Unmarshal([]byte(outWin), &m); err != nil {
		t.Fatalf("expected Windows path backslashes repaired, got error: %v, out: %s", err, outWin)
	}

	// Exempt string fields like target, port, revision must stay strings
	inExempt := `{"target": "8080", "port": "3000", "revision": "1", "reason": "100"}`
	outExempt := NormalizeToArgsObject("CustomTool", inExempt)
	var em map[string]interface{}
	if err := json.Unmarshal([]byte(outExempt), &em); err != nil {
		t.Fatalf("unmarshal error: %v, out: %s", err, outExempt)
	}
	for _, f := range []string{"target", "port", "revision", "reason"} {
		if _, ok := em[f].(string); !ok {
			t.Fatalf("field %s expected to remain string, got %T (%v)", f, em[f], em[f])
		}
	}
}

func TestNormalizeToArgsObject_AdversarialSurrogatePairsAndEmojis(t *testing.T) {
	// Raw emoji in string
	rawEmoji := `{"command":"echo 🚀🎉"}`
	outEmoji := NormalizeToArgsObject("Bash", rawEmoji)
	if outEmoji != rawEmoji {
		t.Fatalf("expected raw emoji preserved, got %s", outEmoji)
	}

	// Escaped surrogate pairs 😀 (grinning face)
	rawSurrogate := "{\"command\":\"echo \\uD83D\\uDE00\"}"
	outSurrogate := NormalizeToArgsObject("Bash", rawSurrogate)
	var v map[string]interface{}
	if err := json.Unmarshal([]byte(outSurrogate), &v); err != nil {
		t.Fatalf("failed to parse surrogate pair: %v", err)
	}
	if v["command"] != "echo \U0001F600" && v["command"] != "echo 😀" {
		t.Fatalf("expected surrogate decoded to grinning face, got %v", v["command"])
	}

	// Lone surrogate \uD83D (malformed)
	rawLoneSurrogate := "{\"command\":\"echo \\uD83D test\"}"
	outLone := NormalizeToArgsObject("Bash", rawLoneSurrogate)
	if err := json.Unmarshal([]byte(outLone), &v); err != nil {
		t.Fatalf("lone surrogate crashed parser: %v", err)
	}
}

func TestNormalizeToArgsObject_AdversarialSlackAndGitHubMCP(t *testing.T) {
	// Slack message ts: "1503435956.000247"
	slackArgs := `{"channel":"C123456","ts":"1503435956.000247","thread_ts":"1503435956.000247"}`
	outSlack := NormalizeToArgsObject("slack_post_message", slackArgs)
	var sv map[string]interface{}
	if err := json.Unmarshal([]byte(outSlack), &sv); err != nil {
		t.Fatalf("parse error: %v", err)
	}
	t.Logf("Slack ts output type: %T value: %v", sv["ts"], sv["ts"])
	t.Logf("Slack thread_ts output type: %T value: %v", sv["thread_ts"], sv["thread_ts"])

	// GitHub repo named "2048"
	ghArgs := `{"owner":"facebook","repo":"2048","base":"1.0","head":"2.0"}`
	outGH := NormalizeToArgsObject("github_create_pull_request", ghArgs)
	var gv map[string]interface{}
	if err := json.Unmarshal([]byte(outGH), &gv); err != nil {
		t.Fatalf("parse error: %v", err)
	}
	t.Logf("GitHub repo output type: %T value: %v", gv["repo"], gv["repo"])
	t.Logf("GitHub base output type: %T value: %v", gv["base"], gv["base"])

	// Nested args string inside object
	nestedArgs := `{"args":"{\"command\":\"ls -la\"}"}`
	outNested := NormalizeToArgsObject("Bash", nestedArgs)
	t.Logf("Nested args output: %s", outNested)
}
