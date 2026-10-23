package httpapi

import "testing"

func TestValidateMessagesRequest(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantOK  bool
		wantMsg string
	}{
		{"malformed json", `{"model":`, false, "Request body must be valid JSON"},
		{"missing messages", `{"model":"m","max_tokens":1}`, false, "\"messages\": array is required and must be non-empty"},
		{"empty messages", `{"model":"m","max_tokens":1,"messages":[]}`, false, "\"messages\": array is required and must be non-empty"},
		{"missing model", `{"max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`, false, "\"model\": required field is missing"},
		{"missing max_tokens", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, false, "\"max_tokens\": required field must be an integer >= 1"},
		{"zero max_tokens", `{"model":"m","max_tokens":0,"messages":[{"role":"user","content":"hi"}]}`, false, "\"max_tokens\": required field must be an integer >= 1"},
		{"negative max_tokens", `{"model":"m","max_tokens":-1,"messages":[{"role":"user","content":"hi"}]}`, false, "\"max_tokens\": required field must be an integer >= 1"},
		{"string max_tokens", `{"model":"m","max_tokens":"100","messages":[{"role":"user","content":"hi"}]}`, false, "\"max_tokens\": required field must be an integer >= 1"},
		{"bool max_tokens", `{"model":"m","max_tokens":true,"messages":[{"role":"user","content":"hi"}]}`, false, "\"max_tokens\": required field must be an integer >= 1"},
		{"float max_tokens", `{"model":"m","max_tokens":10.5,"messages":[{"role":"user","content":"hi"}]}`, false, "\"max_tokens\": required field must be an integer >= 1"},
		{"tool_choice without tools", `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"auto"}}`, false, "\"tool_choice\": provided without any \"tools\" declared"},
		{"tool_choice invalid type", `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"t"}],"tool_choice":{"type":"bogus"}}`, false, `"tool_choice.type": "bogus" is not one of auto, any, tool, none`},
		{"tool_choice tool not present", `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"t"}],"tool_choice":{"type":"tool","name":"other"}}`, false, `"tool_choice": tool "other" is not present in "tools"`},
		{"bad role", `{"model":"m","max_tokens":1,"messages":[{"role":"tool","content":"hi"}]}`, false, `messages.0.role: Input should be 'user' or 'assistant', got "tool"`},
		{"missing role", `{"model":"m","max_tokens":1,"messages":[{"content":"hi"}]}`, false, `messages.0.role: Field required`},
		{"temperature too high", `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}],"temperature":1.5}`, false, `"temperature": must be a number between 0 and 1`},
		{"temperature negative", `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}],"temperature":-0.1}`, false, `"temperature": must be a number between 0 and 1`},
		{"top_p out of range", `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}],"top_p":2}`, false, `"top_p": must be a number between 0 and 1`},
		{"top_k negative", `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}],"top_k":-1}`, false, `"top_k": must be an integer >= 0`},
		{"stop_sequences non-string", `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}],"stop_sequences":["a",1]}`, false, `"stop_sequences.1": must be a string`},
		{"tool_choice required rejected", `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"t"}],"tool_choice":"required"}`, false, `"tool_choice.type": "required" is not one of auto, any, tool, none`},
		{"valid 8 stop_sequences", `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}],"stop_sequences":["1","2","3","4","5","6","7","8"]}`, true, ""},
		{"valid sampling bounds and metadata", `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}],"temperature":0,"top_p":1,"top_k":0,"metadata":{"user_id":"u"},"service_tier":"auto","mcp_servers":[],"context_management":{}}`, true, ""},
		{"valid assistant first", `{"model":"m","max_tokens":1,"messages":[{"role":"assistant","content":"hi"},{"role":"user","content":"x"}]}`, true, ""},
		{"valid minimal", `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`, true, ""},
		{"valid tool_choice tool present", `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"t"}],"tool_choice":{"type":"tool","name":"t"}}`, true, ""},
		{"valid tool_choice builtin tool by type", `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"bash_20250124"}],"tool_choice":{"type":"tool","name":"bash"}}`, true, ""},
		{"valid tool_choice any", `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"t"}],"tool_choice":{"type":"any"}}`, true, ""},
		// GAP-P2-02: thinking.budget_tokens validation.
		{"thinking enabled missing budget_tokens", `{"model":"m","max_tokens":2000,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled"}}`, false, `"thinking.budget_tokens": required when "thinking.type" is "enabled"`},
		{"thinking enabled budget below minimum", `{"model":"m","max_tokens":2000,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":512}}`, false, `"thinking.budget_tokens": 512 is below the minimum of 1024`},
		{"thinking enabled max_tokens not greater than budget", `{"model":"m","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":1024}}`, false, `"max_tokens": 1024 must be greater than "thinking.budget_tokens" (1024)`},
		{"thinking enabled valid", `{"model":"m","max_tokens":4096,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":1024}}`, true, ""},
		{"thinking adaptive no budget required", `{"model":"m","max_tokens":2000,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"adaptive"}}`, true, ""},
		{"thinking disabled no budget required", `{"model":"m","max_tokens":2000,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"}}`, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, ok := validateMessagesRequest([]byte(tc.body))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (msg=%q)", ok, tc.wantOK, msg)
			}
			if !ok && msg != tc.wantMsg {
				t.Fatalf("msg = %q, want %q", msg, tc.wantMsg)
			}
		})
	}
}
