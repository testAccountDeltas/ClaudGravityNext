// Part of the orchestrate package. Gemini's implicit cache matches a request
// against earlier ones from the start: tools, system instruction, then the
// conversation message by message. When the start of a request changes from
// one turn to the next, everything behind the change is re-read at full price
// and the cache_read figure in the log collapses (seen as a value stuck near
// the 13-20k of system + tools). This file finds such changes without storing
// any text in the log: it remembers one 64-bit hash per message of the
// previous request of the same conversation and, for a request whose start
// changed, logs which part changed first and in what way.
package orchestrate

import (
	"hash/fnv"
	"strconv"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"claudgravity/internal/logx"
)

type prefixFP struct {
	tools, system uint64
	msgs          []uint64
	raw           []string // kept in memory only to describe the first difference
	toolNames     []string // declared function names, in order (names only)
}

const (
	prefixConversations = 256 // conversations remembered
	// A request legitimately differs from its predecessor in the last few
	// messages: the newest turn is appended, and the previous model turn loses
	// its thought parts once it is no longer the last one.
	prefixNormalTail = 3
	prefixMaxRawKeep = 64 << 10 // do not keep the raw text of very large messages
)

var (
	prefixMu    sync.Mutex
	prefixLast  = map[string]*prefixFP{}
	prefixOrder []string
)

func hash64(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

func fingerprintBody(body []byte) *prefixFP {
	fp := &prefixFP{
		tools:  hash64(gjson.GetBytes(body, "tools").Raw),
		system: hash64(gjson.GetBytes(body, "system_instruction").Raw + gjson.GetBytes(body, "systemInstruction").Raw),
	}
	gjson.GetBytes(body, "tools.0.functionDeclarations").ForEach(func(_, d gjson.Result) bool {
		fp.toolNames = append(fp.toolNames, d.Get("name").String())
		return true
	})
	gjson.GetBytes(body, "contents").ForEach(func(_, c gjson.Result) bool {
		fp.msgs = append(fp.msgs, hash64(c.Raw))
		if len(c.Raw) <= prefixMaxRawKeep {
			fp.raw = append(fp.raw, c.Raw)
		} else {
			fp.raw = append(fp.raw, "")
		}
		return true
	})
	return fp
}

// firstDiff is the index of the first message that differs, or -1.
func firstDiff(a, b *prefixFP) int {
	n := len(a.msgs)
	if len(b.msgs) < n {
		n = len(b.msgs)
	}
	for i := 0; i < n; i++ {
		if a.msgs[i] != b.msgs[i] {
			return i
		}
	}
	return -1
}

// describeDiff says in a phrase how two versions of one message differ,
// without printing any of the text.
func describeDiff(prev, cur string) string {
	if prev == "" || cur == "" {
		return "content (too large to compare)"
	}
	strip := func(s string) string {
		n := int(gjson.Get(s, "parts.#").Int())
		for i := 0; i < n; i++ {
			s, _ = sjson.Delete(s, "parts."+strconv.Itoa(i)+".thoughtSignature")
		}
		return s
	}
	if strip(prev) == strip(cur) {
		return "only thoughtSignature"
	}
	pn, cn := gjson.Get(prev, "parts.#").Int(), gjson.Get(cur, "parts.#").Int()
	if pn != cn {
		return "number of parts " + strconv.FormatInt(pn, 10) + " -> " + strconv.FormatInt(cn, 10)
	}
	return "part content"
}

// diffToolNames describes how the declared tools changed: which were added,
// which removed, or that only their order / schemas differ.
func diffToolNames(prev, cur []string) string {
	has := func(list []string) map[string]bool {
		m := make(map[string]bool, len(list))
		for _, n := range list {
			m[n] = true
		}
		return m
	}
	pm, cm := has(prev), has(cur)
	var added, removed []string
	for _, n := range cur {
		if !pm[n] {
			added = append(added, n)
		}
	}
	for _, n := range prev {
		if !cm[n] {
			removed = append(removed, n)
		}
	}
	switch {
	case len(added) == 0 && len(removed) == 0:
		return "same names, order or a schema changed"
	default:
		return "added=" + strings.Join(added, ",") + " removed=" + strings.Join(removed, ",")
	}
}

// logPrefixChange compares body with the previous request of the same
// conversation and logs when the start of the request changed.
func logPrefixChange(rid uint64, convKey string, body []byte) {
	if convKey == "" {
		return
	}
	cur := fingerprintBody(body)
	prefixMu.Lock()
	prev := prefixLast[convKey]
	if prev == nil {
		if len(prefixOrder) >= prefixConversations {
			delete(prefixLast, prefixOrder[0])
			prefixOrder = prefixOrder[1:]
		}
		prefixOrder = append(prefixOrder, convKey)
	}
	prefixLast[convKey] = cur
	prefixMu.Unlock()
	if prev == nil {
		return
	}
	toolsChanged, systemChanged := prev.tools != cur.tools, prev.system != cur.system
	d := firstDiff(prev, cur)
	abnormal := d >= 0 && d < len(prev.msgs)-prefixNormalTail
	if !toolsChanged && !systemChanged && !abnormal {
		return
	}
	what := ""
	if toolsChanged {
		what = " (" + diffToolNames(prev.toolNames, cur.toolNames) + ")"
	}
	if abnormal {
		what += " firstDiffMsg=" + strconv.Itoa(d) + " of " + strconv.Itoa(len(cur.msgs)) + " (previous request had " +
			strconv.Itoa(len(prev.msgs)) + ") differs in: " + describeDiff(prev.raw[d], cur.raw[d])
	}
	logx.LogGateway("[#%d] [Prefix] the start of this request differs from the previous one of the same conversation: tools=%v system=%v%s; "+
		"expect a cache miss behind the first change", rid, toolsChanged, systemChanged, what)
}
