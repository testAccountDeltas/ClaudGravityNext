// Part of the tokens package — see text.go for the package doc comment.
// Byte-based attachment pruning. Gemini's inlineData ceiling is 20MB of
// base64 per request regardless of the token budget, so the oldest
// image/document attachments are shed once the total passes
// MaxInlineAttachmentBytes (below that ceiling) — including images and
// documents nested inside a tool_result's content array, which is where
// Claude Code puts screenshots.
package tokens

import (
	"encoding/json"
	"strings"

	"claudgravity/internal/protocol/claude"
)

// MaxInlineAttachmentBytes is the pruning target for total inline base64
// bytes: safely under the 20MB upstream limit.
const MaxInlineAttachmentBytes = 18 << 20

const attachmentPrunedText = "[Earlier image/document attachment omitted: total attachment size exceeded the upstream inline-data limit]"

type attachmentRef struct {
	msg, blk, item int // item == -1: the block itself; otherwise index inside a tool_result
	size           int
}

type attachmentProbe struct {
	Type   string `json:"type"`
	Source struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	} `json:"source"`
}

// inlineSize returns the base64 byte count of a block that becomes a
// Gemini inlineData part (binary image/PDF with a base64 source), else 0.
func (p attachmentProbe) inlineSize() int {
	if (p.Type != "image" && p.Type != "document") || p.Source.Type != "base64" {
		return 0
	}
	mt := strings.ToLower(p.Source.MediaType)
	if i := strings.Index(mt, ";"); i != -1 {
		mt = strings.TrimSpace(mt[:i])
	}
	if mt == "" {
		mt = "image/png"
		if p.Type == "document" {
			mt = "application/pdf"
		}
	}
	if mt == "image/svg+xml" || !(mt == "application/pdf" || strings.HasPrefix(mt, "image/")) {
		return 0
	}
	return len(p.Source.Data)
}

// InlineAttachmentBytes totals the inline base64 bytes of every
// user-role image/document attachment in req (top-level or nested in a
// tool_result).
func InlineAttachmentBytes(req claude.Request) int {
	total := 0
	for _, r := range collectAttachments(req.Messages) {
		total += r.size
	}
	return total
}

func collectAttachments(msgs []claude.Message) []attachmentRef {
	var refs []attachmentRef
	for mi, m := range msgs {
		if m.Role == claude.RoleAssistant {
			continue // assistant attachments become text placeholders downstream
		}
		for bi, b := range m.Content {
			switch b.Type {
			case "image", "document":
				var p attachmentProbe
				if json.Unmarshal(b.Raw, &p) == nil {
					if n := p.inlineSize(); n > 0 {
						refs = append(refs, attachmentRef{mi, bi, -1, n})
					}
				}
			case "tool_result":
				var tr struct {
					Content []json.RawMessage `json:"content"`
				}
				if json.Unmarshal(b.Raw, &tr) != nil {
					continue
				}
				for ii, item := range tr.Content {
					var p attachmentProbe
					if json.Unmarshal(item, &p) == nil {
						if n := p.inlineSize(); n > 0 {
							refs = append(refs, attachmentRef{mi, bi, ii, n})
						}
					}
				}
			}
		}
	}
	return refs
}

// PruneAttachmentsByBytes replaces the OLDEST inline attachments with a
// short text placeholder until the total is <= maxBytes. The newest
// attachment is never pruned (a single oversized attachment stays and is
// rejected with 413 by the caller). req is not mutated; the returned
// request is a copy when pruned > 0.
func PruneAttachmentsByBytes(req claude.Request, maxBytes int) (claude.Request, int) {
	refs := collectAttachments(req.Messages)
	total := 0
	for _, r := range refs {
		total += r.size
	}
	if total <= maxBytes || len(refs) < 2 {
		return req, 0
	}
	drop := map[[3]int]bool{}
	for i := 0; i < len(refs)-1 && total > maxBytes; i++ {
		drop[[3]int{refs[i].msg, refs[i].blk, refs[i].item}] = true
		total -= refs[i].size
	}
	placeholder := claude.NewBlock(claude.TextBlock{Type: "text", Text: attachmentPrunedText})
	msgs := make([]claude.Message, len(req.Messages))
	copy(msgs, req.Messages)
	changedMsg := map[int]bool{}
	for k := range drop {
		changedMsg[k[0]] = true
	}
	for mi := range changedMsg {
		blocks := make([]claude.Block, len(msgs[mi].Content))
		copy(blocks, msgs[mi].Content)
		for bi, b := range blocks {
			if drop[[3]int{mi, bi, -1}] {
				blocks[bi] = placeholder
			} else if b.Type == "tool_result" {
				blocks[bi] = pruneToolResultItems(b, mi, bi, drop, placeholder)
			}
		}
		msgs[mi].Content = blocks
	}
	req.Messages = msgs
	return req, len(drop)
}

func pruneToolResultItems(b claude.Block, mi, bi int, drop map[[3]int]bool, placeholder claude.Block) claude.Block {
	var obj map[string]json.RawMessage
	if json.Unmarshal(b.Raw, &obj) != nil {
		return b
	}
	var items []json.RawMessage
	if json.Unmarshal(obj["content"], &items) != nil {
		return b
	}
	changed := false
	for ii := range items {
		if drop[[3]int{mi, bi, ii}] {
			items[ii] = placeholder.Raw
			changed = true
		}
	}
	if !changed {
		return b
	}
	obj["content"], _ = json.Marshal(items)
	raw, err := json.Marshal(obj)
	if err != nil {
		return b
	}
	return claude.Block{Type: b.Type, Raw: raw}
}
