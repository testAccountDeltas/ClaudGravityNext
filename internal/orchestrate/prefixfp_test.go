package orchestrate

import (
	"strconv"
	"testing"
)

func fpBody(tools, sys string, msgs ...string) []byte {
	b := `{"tools":` + tools + `,"system_instruction":{"parts":[{"text":"` + sys + `"}]},"contents":[`
	for i, m := range msgs {
		if i > 0 {
			b += ","
		}
		b += m
	}
	return []byte(b + `]}`)
}

func fpMsg(role, text, sig string) string {
	s := `{"role":"` + role + `","parts":[{"text":"` + text + `"`
	if sig != "" {
		s += `,"thoughtSignature":"` + sig + `"`
	}
	return s + `}]}`
}

func fpMsgs(n int) []string {
	var out []string
	for i := 0; i < n; i++ {
		role := "user"
		if i%2 == 1 {
			role = "model"
		}
		out = append(out, fpMsg(role, "text"+strconv.Itoa(i), ""))
	}
	return out
}

func TestPrefixFingerprint_GrowthIsQuietAndChangesAreFound(t *testing.T) {
	prev := fingerprintBody(fpBody(`[1]`, "sys", fpMsgs(10)...))
	grown := fingerprintBody(fpBody(`[1]`, "sys", fpMsgs(12)...))
	if d := firstDiff(prev, grown); d != -1 {
		t.Fatalf("pure growth must not differ: %d", d)
	}
	changedEarly := append([]string{fpMsg("user", "CHANGED", "")}, fpMsgs(12)[1:]...)
	if d := firstDiff(grown, fingerprintBody(fpBody(`[1]`, "sys", changedEarly...))); d != 0 {
		t.Fatalf("the first difference is at 0, got %d", d)
	}
	late := append(fpMsgs(9), fpMsg("user", "newer", ""))
	if d := firstDiff(prev, fingerprintBody(fpBody(`[1]`, "sys", late...))); d < len(prev.msgs)-prefixNormalTail {
		t.Fatalf("a change in the newest messages is normal, d=%d", d)
	}
}

func TestPrefixFingerprint_DescribesWithoutQuotingText(t *testing.T) {
	if got := describeDiff(fpMsg("model", "x", "sigA"), fpMsg("model", "x", "sigB")); got != "only thoughtSignature" {
		t.Fatalf("describeDiff = %q", got)
	}
	if got := describeDiff(fpMsg("model", "x", ""), fpMsg("model", "y", "")); got != "part content" {
		t.Fatalf("describeDiff = %q", got)
	}
	if got := describeDiff("", fpMsg("model", "y", "")); got == "" {
		t.Fatal("a too large message still needs a description")
	}
}

func TestPrefixFingerprint_ToolsAndSystemAreTrackedSeparately(t *testing.T) {
	a := fingerprintBody(fpBody(`[1]`, "sys", fpMsgs(4)...))
	b := fingerprintBody(fpBody(`[1,2]`, "sys", fpMsgs(4)...))
	if a.tools == b.tools || a.system != b.system {
		t.Fatal("a tools change must not look like a system change")
	}
	c := fingerprintBody(fpBody(`[1]`, "sys2", fpMsgs(4)...))
	if a.system == c.system || a.tools != c.tools {
		t.Fatal("a system change must not look like a tools change")
	}
}

func TestLogPrefixChange_IsSafeAndBounded(t *testing.T) {
	logPrefixChange(1, "", []byte(`{}`)) // no conversation: nothing remembered
	for i := 0; i < prefixConversations+20; i++ {
		logPrefixChange(2, "conv-bound-"+strconv.Itoa(i), fpBody(`[1]`, "s", fpMsgs(3)...))
	}
	prefixMu.Lock()
	n := len(prefixLast)
	prefixMu.Unlock()
	if n > prefixConversations {
		t.Fatalf("memory is not bounded: %d conversations", n)
	}
	const key = "conv-change"
	logPrefixChange(3, key, fpBody(`[1]`, "s", fpMsgs(12)...))
	logPrefixChange(4, key, fpBody(`[1,2]`, "s2", append([]string{fpMsg("user", "CHANGED", "")}, fpMsgs(14)[1:]...)...)) // must log, not panic
}
