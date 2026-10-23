// Part of the stream package — see events.go for the package doc
// comment. Direct port of v1's ProcessGeminiChunk/StreamGeminiToClaude.
// Deliberately kept on gjson over raw bytes rather than decoded into
// internal/protocol/gemini's typed domain: this is the one path where a
// subtle decode mismatch would silently corrupt a live tool call or drop
// a signature mid-stream with no characterization test able to catch it
// before production traffic does — the same "minimal-diff where
// correctness risk is high" judgment call internal/schema's port made.
// Wiring in the typed domain here is a reasonable future improvement, not
// something this port attempts.
package stream

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"

	"claudgravity/internal/tokens"
)

// ChunkRecorder is the optional capture hook for every raw Gemini chunk
// this FSM processes — internal/speccapture's own methods satisfy this.
// A nil recorder is a silent no-op.
type ChunkRecorder interface {
	AppendGeminiChunk(rid uint64, chunk []byte)
	AppendRawGeminiChunk(rid uint64, chunk []byte)
}

// SetChunkRecorder wires an optional chunk-capture hook — same pattern as
// internal/accounts/internal/requestlog's SetBroadcaster from earlier
// phases.
func (f *FSM) SetChunkRecorder(r ChunkRecorder) {
	f.chunkRecorder = r
}

// ProcessGeminiChunk parses a single Gemini SSE JSON line and drives the
// state machine.
func (f *FSM) ProcessGeminiChunk(chunkJSON []byte) error {
	// An audit caught that StreamGeminiToClaude's read loop can have
	// several Gemini SSE "data:" lines in the SAME read() buffer — if an
	// earlier line already drove FinishStream to completion (hasFinished
	// set, message_stop + writer.Finish() already sent), a later line in
	// that same buffer would still run HandleText/HandleFunctionCall and
	// mutate FSM state (re-opening a content block) even though
	// writer.Finish() now makes its EmitEvent calls no-ops. Bail out
	// before touching any FSM state once finished, not just before
	// writing.
	f.mu.Lock()
	finished := f.hasFinished
	f.mu.Unlock()
	if finished {
		if f.chunkRecorder != nil {
			f.chunkRecorder.AppendGeminiChunk(f.rid, chunkJSON)
			f.chunkRecorder.AppendRawGeminiChunk(f.rid, chunkJSON)
		}
		root := gjson.ParseBytes(chunkJSON)
		if respNode := root.Get("response"); respNode.Exists() {
			root = respNode
		}
		if um := root.Get("usageMetadata"); um.Exists() {
			f.applyUsageMetadata(um)
			inTok := f.inputTokens
			if inTok <= 0 && f.estInputTokens > 0 {
				inTok = int64(f.estInputTokens)
			}
			if inTok > 0 && f.cacheTokens > 0 {
				setSessionCacheFraction(f.sessionID, float64(f.cacheTokens)/float64(inTok))
			}
		}
		return nil
	}
	if f.chunkRecorder != nil {
		f.chunkRecorder.AppendGeminiChunk(f.rid, chunkJSON)
		f.chunkRecorder.AppendRawGeminiChunk(f.rid, chunkJSON)
	}
	root := gjson.ParseBytes(chunkJSON)
	if respNode := root.Get("response"); respNode.Exists() {
		root = respNode
	}

	// Google can send an in-band error chunk mid-stream (e.g. a 429
	// RESOURCE_EXHAUSTED that only surfaces once generation is already
	// underway) and then just close the connection — that chunk has no
	// "candidates" node, so without this check it falls straight through
	// to the candidates-empty early return below, the read loop then
	// hits a normal EOF, and FinishStream("STOP") reports a clean
	// "end_turn" — the client sees a successful, if truncated, response
	// instead of learning generation actually failed.
	if errNode := root.Get("error"); errNode.Exists() {
		return f.handleInBandError(errNode)
	}

	// A prompt Google's safety filters reject outright (before any
	// candidate is even generated) comes back as a normal 200 OK with a
	// promptFeedback node and no "candidates"/"error" field at all — the
	// same silent-swallow failure mode as the error check above, under a
	// different key.
	if pf := root.Get("promptFeedback"); pf.Exists() {
		if br := pf.Get("blockReason").String(); br != "" && br != "BLOCK_REASON_UNSPECIFIED" {
			// A prompt-level block: refusal with no content (no injected
			// explanation text — the reason goes in stop_details). The
			// pending prefill must not be echoed onto a refusal.
			f.prefillText = ""
			f.refusalDetail = "promptFeedback.blockReason=" + br
			return f.FinishStream("SAFETY")
		}
	}

	if um := root.Get("usageMetadata"); um.Exists() {
		f.applyUsageMetadata(um)
	}

	candidates := root.Get("candidates")
	if !candidates.IsArray() || len(candidates.Array()) == 0 {
		if f.pendingFinishReason != "" {
			fr := f.pendingFinishReason
			f.pendingFinishReason = ""
			return f.FinishStream(fr)
		}
		return nil
	}

	// Look-ahead scan across all candidates and parts in this chunk for thoughtSignature
	// before processing any parts, so that a signature in any part or candidate
	// is captured before earlier parts can close an open thinking block.
	for _, c := range candidates.Array() {
		if sig := c.Get("thoughtSignature").String(); sig != "" {
			f.updatePendingThinkingSignature(sig)
		}
		if sig := c.Get("content.thoughtSignature").String(); sig != "" {
			f.updatePendingThinkingSignature(sig)
		}
		if p := c.Get("content.parts"); p.IsArray() {
			for _, part := range p.Array() {
				if sig := part.Get("thoughtSignature").String(); sig != "" {
					f.updatePendingThinkingSignature(sig)
				}
				if sig := part.Get("functionCall.thoughtSignature").String(); sig != "" {
					f.updatePendingThinkingSignature(sig)
				}
			}
		}
		if fr := c.Get("finishReason").String(); fr != "" && fr != "FINISH_REASON_UNSPECIFIED" {
			f.pendingFinishReason = fr
		}
	}
	if sig := root.Get("thoughtSignature").String(); sig != "" {
		f.updatePendingThinkingSignature(sig)
	}
	if fr := root.Get("finishReason").String(); fr != "" && fr != "FINISH_REASON_UNSPECIFIED" {
		f.pendingFinishReason = fr
	}

	cand := candidates.Array()[0]

	if err := f.processParts(cand.Get("content.parts")); err != nil {
		return err
	}
	f.collectGroundingMetadata(cand.Get("groundingMetadata"))

	fr := cand.Get("finishReason").String()
	if fr == "" || fr == "FINISH_REASON_UNSPECIFIED" {
		fr = f.pendingFinishReason
	}
	if fr != "" && fr != "FINISH_REASON_UNSPECIFIED" {
		f.pendingFinishReason = fr
		um := root.Get("usageMetadata")
		hasUsage := um.Exists() && (um.Get("candidatesTokenCount").Exists() || um.Get("promptTokenCount").Exists())
		if hasUsage || f.outputTokens > 0 {
			f.pendingFinishReason = ""
			return f.FinishStream(fr)
		}
		// Trailing usageMetadata split across chunks: buffer pendingFinishReason
		// and defer FinishStream so real usage is reflected in message_delta.
		return nil
	}

	return nil
}

func (f *FSM) applyUsageMetadata(um gjson.Result) {
	if pt := um.Get("promptTokenCount").Int(); pt > 0 {
		// Single point of application for the streaming path's live
		// driftRatio measurement (Part 6.4/Part 11 DoD #4): this is the
		// first moment Gemini's REAL prompt token count exists next to
		// this request's own pre-flight estimate (f.estInputTokens,
		// carried in from reqctx.State.EstInputTokens by NewFSM) —
		// log it here, before anything downstream can overwrite
		// f.inputTokens with that estimate as a fallback (finish.go).
		if !f.driftLogged {
			tokens.LogDriftRatio(f.rid, f.estInputTokens, pt)
			f.driftLogged = true
		}
		f.inputTokens = pt
	}
	// Anthropic's own extended-thinking spec bills thinking tokens as
	// output tokens; a candidatesTokenCount of 0 with a non-zero
	// thoughtsTokenCount (a turn that's pure thinking, e.g. right
	// before a tool call) must still update outputTokens.
	ct := um.Get("candidatesTokenCount").Int()
	tt := um.Get("thoughtsTokenCount").Int()
	if ct > 0 || tt > 0 {
		f.outputTokens = ct + tt
		f.candTokens = ct
		f.thinkingTokens = tt
	}
	if cc := um.Get("cachedContentTokenCount").Int(); cc > 0 {
		f.cacheTokens = cc
	}
}

// processParts handles one candidate's content.parts array. A look-ahead
// over THIS chunk's own parts (first loop) lets a LATER part's
// thoughtSignature already be captured before an EARLIER text part's
// HandleText call closes the thinking block — live captures confirm
// Gemini commonly delivers a thought part and its functionCall's
// thoughtSignature together in the SAME candidate. This doesn't fully
// close the gap when Gemini splits them across separate chunks (a known,
// deliberately deferred limitation — cross-chunk buffering carries real
// ordering/latency risk), but strictly improves the common same-chunk
// case with zero downside otherwise.
func (f *FSM) processParts(parts gjson.Result) error {
	if !parts.IsArray() {
		return nil
	}
	for _, part := range parts.Array() {
		sig := part.Get("thoughtSignature").String()
		if sig == "" {
			sig = part.Get("functionCall.thoughtSignature").String()
		}
		if sig != "" {
			f.lastTurnThoughtSignature = sig
			if f.thoughtSignature == "" {
				f.thoughtSignature = sig
			}
			f.updatePendingThinkingSignature(sig)
		}
	}
	for _, part := range parts.Array() {
		if f.HasFinished() {
			return nil // e.g. the client's max_tokens was reached mid-chunk
		}
		if err := f.processOnePart(part); err != nil {
			return err
		}
	}
	return nil
}

func (f *FSM) processOnePart(part gjson.Result) error {
	partSig := part.Get("thoughtSignature").String()
	if partSig == "" {
		partSig = part.Get("functionCall.thoughtSignature").String()
	}
	if partSig != "" {
		f.lastTurnThoughtSignature = partSig
		if f.thoughtSignature == "" {
			f.thoughtSignature = partSig
		}
		f.updatePendingThinkingSignature(partSig)
	}
	switch {
	case part.Get("thought").Bool():
		// The signature is often carried on a trailing chunk whose text
		// is empty — filtering on an empty chunk.text would silently
		// drop it. Capture it either way; only emit a thinking_delta
		// when there's text.
		if t := part.Get("text").String(); t != "" {
			return f.HandleThinking(t, partSig)
		}
		if partSig != "" {
			if err := f.EnsureMessageStart(); err != nil {
				return err
			}
			f.lastTurnThoughtSignature = partSig
			if f.currentBlock == BlockThinking {
				f.thoughtSignature = partSig
			}
		}
		return nil
	case part.Get("functionCall").Exists():
		fc := part.Get("functionCall")
		name := fc.Get("name").String()
		if orig, ok := f.reverseToolNameMap[name]; ok {
			// internal/translate (once ported) sanitizes this tool's name
			// for Gemini (e.g. strips a hyphen) — restore the original
			// before it reaches Claude Code.
			name = orig
		}
		args := fc.Get("args").Raw
		if args == "" || args == "null" {
			// Anthropic's tool_use.input must be an object; a literal
			// JSON null (Gemini can return bare "args": null for a
			// no-argument call) streamed through as partial_json "null"
			// instead of "{}", which Claude Code's incremental JSON
			// parser can't treat as an object.
			args = "{}"
		}
		callSig := partSig
		if callSig == "" {
			callSig = f.lastTurnThoughtSignature
		}
		return f.HandleFunctionCall(name, args, callSig)
	default:
		if t := part.Get("text").String(); t != "" {
			if f.currentBlock == BlockThinking && f.thoughtSignature == "" && partSig != "" {
				f.thoughtSignature = partSig
			}
			return f.HandleText(t)
		}
	}
	return nil
}

func (f *FSM) collectGroundingMetadata(gm gjson.Result) {
	if !gm.Exists() {
		return
	}
	if queries := gm.Get("webSearchQueries"); queries.IsArray() {
		for _, q := range queries.Array() {
			f.searchQueries = append(f.searchQueries, q.String())
		}
	}
	if chunks := gm.Get("groundingChunks"); chunks.IsArray() {
		for _, c := range chunks.Array() {
			uri := c.Get("web.uri").String()
			title := c.Get("web.title").String()
			if uri != "" {
				if title == "" {
					title = uri
				}
				f.groundingSources = append(f.groundingSources, GroundingSource{Title: title, URI: uri})
			}
		}
	}
}

// appendGroundingSourcesFooter appends the Google Search sources footer
// once generation finishes — never after a tool_use block, since a
// tool_use block ends the assistant's turn per Anthropic convention (the
// client replies with its tool_result next) and trailing text after it
// risks confusing stricter SDK parsers.
func (f *FSM) appendGroundingSourcesFooter() {
	if len(f.groundingSources) == 0 || f.toolUseOccurred {
		return
	}
	if f.textChars == 0 && f.lastText == "" {
		// Sources alone are not an answer; never let a footer turn an
		// empty turn into one.
		return
	}
	var sb strings.Builder
	if f.preferRussian {
		sb.WriteString("\n\n---\n**Источники Google Search:**\n")
	} else {
		sb.WriteString("\n\n---\n**Google Search Sources:**\n")
	}
	seen := make(map[string]bool)
	for _, src := range f.groundingSources {
		if !seen[src.URI] {
			seen[src.URI] = true
			sb.WriteString(fmt.Sprintf("- [%s](%s)\n", src.Title, src.URI))
		}
	}
	f.groundingSources = nil
	_ = f.HandleText(sb.String())
}
