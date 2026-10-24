package orchestrate

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func tfTextCand(text, finish string) string {
	fr := ""
	if finish != "" {
		fr = `,"finishReason":"` + finish + `"`
	}
	return `{"content":{"role":"model","parts":[{"text":` + q(text) + `}]}` + fr + `}`
}

// streamedStop returns (text, stop_reason, stop_sequence) of an SSE body.
// tfWrapTok is tfWrap with a chosen candidatesTokenCount (the exact visible
// count the cap logic trusts when it is within max_tokens).
func tfWrapTok(cand string, out int) string {
	return fmt.Sprintf(`{"response":{"candidates":[%s],"usageMetadata":{"promptTokenCount":1000,"candidatesTokenCount":%d}}}`, cand, out)
}

func streamedStop(body string) (string, string, string) {
	var text strings.Builder
	var reason, seq string
	for _, l := range strings.Split(body, "\n") {
		if !strings.HasPrefix(l, "data: ") {
			continue
		}
		d := l[6:]
		if gjson.Get(d, "delta.type").String() == "text_delta" {
			text.WriteString(gjson.Get(d, "delta.text").String())
		}
		if gjson.Get(d, "type").String() == "message_delta" {
			reason, seq = gjson.Get(d, "delta.stop_reason").String(), gjson.Get(d, "delta.stop_sequence").String()
		}
	}
	return text.String(), reason, seq
}

func TestStopSeq_NotForwardedToGemini(t *testing.T) {
	var sent string
	tfRun(t, fmt.Sprintf(tfReq, `,"stop_sequences":["5","STOP"]`), false, func(n int, body string) (int, string) {
		sent = body
		return 200, tfWrap(tfTextCand("ok", "STOP"))
	})
	if sent == "" || strings.Contains(sent, "stopSequences") {
		t.Fatalf("stop sequences must be matched by the gateway, not forwarded (it would hide the signal): %s", sent)
	}
}

// Streaming and non-streaming must agree for the same upstream text.
func TestStopSeq_StreamingNonStreamingParity(t *testing.T) {
	cases := []struct {
		name, req string
		chunks    []string
		wantText  string
		wantStop  string
		wantSeq   string
	}{
		{"mid text", `,"stop_sequences":["5"]`, []string{"1,2,3,4,5,6,7"}, "1,2,3,4,", "stop_sequence", "5"},
		{"split across chunks", `,"stop_sequences":["<END>"]`, []string{"abc <E", "N", "D> tail"}, "abc ", "stop_sequence", "<END>"},
		{"earliest of two", `,"stop_sequences":["zz","b"]`, []string{"aabbzz"}, "aa", "stop_sequence", "b"},
		{"no match", `,"stop_sequences":["NOPE"]`, []string{"plain ", "answer"}, "plain answer", "end_turn", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var sse []string
			full := strings.Join(c.chunks, "")
			for i, ch := range c.chunks {
				fr := ""
				if i == len(c.chunks)-1 {
					fr = "STOP"
				}
				sse = append(sse, tfWrap(tfTextCand(ch, fr)))
			}
			rs := tfRun(t, fmt.Sprintf(tfReq, c.req), true, func(int, string) (int, string) { return 200, tfSSE(sse...) })
			gotText, gotStop, gotSeq := streamedStop(rs.body)
			if gotText != c.wantText || gotStop != c.wantStop || gotSeq != c.wantSeq {
				t.Fatalf("stream: %q %q %q, body=%s", gotText, gotStop, gotSeq, rs.body)
			}
			rn := tfRun(t, fmt.Sprintf(tfReq, c.req), false, func(int, string) (int, string) {
				return 200, tfWrap(tfTextCand(full, "STOP"))
			})
			nText := gjson.Get(rn.body, "content.0.text").String()
			if nText != c.wantText || gjson.Get(rn.body, "stop_reason").String() != c.wantStop ||
				gjson.Get(rn.body, "stop_sequence").String() != c.wantSeq {
				t.Fatalf("non-stream differs from stream: %s", rn.body)
			}
		})
	}
}

func TestStopSeq_NonStreaming_MatchAtStart_EmptyContent(t *testing.T) {
	r := tfRun(t, fmt.Sprintf(tfReq, `,"stop_sequences":["##"]`), false, func(int, string) (int, string) {
		return 200, tfWrap(tfTextCand("## nothing before", "STOP"))
	})
	if gjson.Get(r.body, "stop_reason").String() != "stop_sequence" || len(gjson.Get(r.body, "content").Array()) != 0 {
		t.Fatalf("%s", r.body)
	}
}

func TestStopSeq_MaxTokensCapBeforeMatch_CapWins_BothPaths(t *testing.T) {
	long := strings.Repeat("word ", 200)
	req := `{"model":"claude-sonnet-4-5","max_tokens":5,"stop_sequences":["END"],"messages":[{"role":"user","content":"hi"}]}`
	rs := tfRun(t, req, true, func(int, string) (int, string) { return 200, tfSSE(tfWrapTok(tfTextCand(long+"END", "STOP"), 300)) })
	_, st, seq := streamedStop(rs.body)
	if st != "max_tokens" || seq != "" {
		t.Fatalf("stream: cap reached before the match must win: %s", rs.body)
	}
	rn := tfRun(t, req, false, func(int, string) (int, string) { return 200, tfWrapTok(tfTextCand(long+"END", "STOP"), 300) })
	if gjson.Get(rn.body, "stop_reason").String() != "max_tokens" || gjson.Get(rn.body, "stop_sequence").Exists() && gjson.Get(rn.body, "stop_sequence").Type != gjson.Null {
		t.Fatalf("non-stream: %s", rn.body)
	}
}

func TestStopSeq_MatchBeforeCap_StopSequenceWins_BothPaths(t *testing.T) {
	req := `{"model":"claude-sonnet-4-5","max_tokens":500,"stop_sequences":["END"],"messages":[{"role":"user","content":"hi"}]}`
	rs := tfRun(t, req, true, func(int, string) (int, string) { return 200, tfSSE(tfWrap(tfTextCand("short END tail tail", "STOP"))) })
	if tx, st, seq := streamedStop(rs.body); tx != "short " || st != "stop_sequence" || seq != "END" {
		t.Fatalf("stream: %q %q %q", tx, st, seq)
	}
	rn := tfRun(t, req, false, func(int, string) (int, string) { return 200, tfWrap(tfTextCand("short END tail tail", "STOP")) })
	if gjson.Get(rn.body, "stop_reason").String() != "stop_sequence" || gjson.Get(rn.body, "content.0.text").String() != "short " {
		t.Fatalf("non-stream: %s", rn.body)
	}
}
