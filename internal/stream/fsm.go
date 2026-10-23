// Part of the stream package — see events.go for the package doc
// comment. FSM replaces v1's SSEStreamer, built against
// reqctx.Params/State instead of NewSSEStreamer's 15 positional
// parameters (the Part 1.4 failure mode internal/reqctx exists to fix).
package stream

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"claudgravity/internal/loopguard"
	"claudgravity/internal/reqctx"
	"claudgravity/internal/sanitize"
	"claudgravity/internal/toolsig"
)

// BlockType is which Anthropic content block (if any) is currently open.
type BlockType int

const (
	BlockNone BlockType = iota
	BlockThinking
	BlockText
	BlockToolUse
)

// GroundingSource is one Google Search result surfaced via Gemini's
// native grounding, appended as a footer once generation finishes.
type GroundingSource struct {
	Title string `json:"title"`
	URI   string `json:"uri"`
}

// FSM drives one request's Gemini-chunks-to-Claude-SSE-events state
// machine.
type FSM struct {
	writer        *SafeSSEWriter
	rid           uint64
	chunkRecorder ChunkRecorder

	currentBlock BlockType
	currentIndex int

	mu          sync.Mutex // guards hasFinished only — FinishWithError can race the main chunk-processing goroutine via a watchdog timeout
	hasStarted  bool
	hasFinished bool
	driftLogged bool // guards tokens.LogDriftRatio in chunk.go — Gemini repeats usageMetadata.promptTokenCount across multiple chunks of the same stream, not just the last one

	messageID string
	model     string

	inputTokens    int64
	outputTokens   int64
	cacheTokens    int64
	thinkingTokens int64 // GAP-TH-07: the thinking-only subset of outputTokens, for usage.output_tokens_details.thinking_tokens

	// Client max_tokens enforcement on visible text (visiblecap.go).
	visibleCap         int64   // client max_tokens; 0 = no cap
	visibleEst         float64 // estimated tokens of text emitted so far
	candTokens         int64   // upstream candidatesTokenCount (visible, excludes thoughts)
	capReached         bool
	retryEmptyMaxToken bool // an empty MAX_TOKENS turn that only thought may be retried once with a larger allowance

	toolUseOccurred    bool
	textChars          int
	thinkingChars      int
	toolNames          []string
	toolCallSignatures []string
	toolCallIDs        []string
	lastText           string
	lastThinking       string

	searchQueries    []string
	groundingSources []GroundingSource

	sessionID           string
	requestedSafeguards bool

	// cwdKey is sessionID, qualified with ":instanceID" for a subagent —
	// GAP-ST1: internal/editrepair's GetSessionCWD/getSessionSnapshot key
	// a subagent's own recorded CWD/file-snapshot state this exact way
	// (see internal/editrepair/cwd.go's parentSessionKey), and
	// internal/orchestrate/nonstreaming.go already builds this same
	// compound key for its own RepairEditToolCall call. The streaming FSM
	// used to pass bare sessionID instead, so a subagent's own CWD
	// override was invisible to its tool-call repair pipeline (falling
	// back to the parent thread's CWD instead) on every streaming
	// request — which is most Claude Code traffic.
	cwdKey string

	// thoughtSignature is the most recent thoughtSignature seen for the
	// OPEN thinking block, flushed as signature_delta on close.
	thoughtSignature string
	// lastTurnThoughtSignature is durable across the turn; inherited by
	// function calls and parallel tool calls.
	lastTurnThoughtSignature string
	// thinkingFinished is true once a thinking block has been opened and
	// closed — Anthropic allows at most one, so a later "thought" chunk
	// (Gemini can interleave thought->text->thought) must not reopen one.
	thinkingFinished bool

	hasPriorTurns          bool
	estInputTokens         int
	preferRussian          bool
	syntheticReasonTools   map[string]bool
	cachePrefixFrac        float64
	cacheFrac1h            float64
	disableParallelToolUse bool
	reverseToolNameMap     map[string]string
	stopSequences          []string
	textHoldBuffer         string
	maxStopLen             int

	prefillText         string
	prefillEmitted      bool
	pendingFinishReason string

	// finalFinishReason / finalStopReason: what the upstream said and what
	// was reported, for the per-request log (Stats).
	finalFinishReason string
	finalStopReason   string
	// refusalDetail overrides stop_details.explanation (a prompt-level
	// promptFeedback block has no candidate finishReason to cite).
	refusalDetail string
}

// NewFSM constructs an FSM for one request. p's fields are the request's
// immutable parameters; st's are this ATTEMPT's resolved model/estimate —
// the same two types that replace v1's 15-positional-parameter
// NewSSEStreamer.
func NewFSM(writer *SafeSSEWriter, rid uint64, p *reqctx.Params, st *reqctx.State) *FSM {
	cwdKey := p.SessionID
	if p.InstanceID != "" {
		cwdKey = p.SessionID + ":" + p.InstanceID
	}
	maxStopLen := 0
	for _, s := range p.StopSequences {
		if len(s) > maxStopLen {
			maxStopLen = len(s)
		}
	}
	return &FSM{
		writer:                 writer,
		rid:                    rid,
		messageID:              fmt.Sprintf("msg_%d", time.Now().UnixNano()),
		model:                  p.ModelName,
		currentIndex:           -1,
		sessionID:              p.SessionID,
		cwdKey:                 cwdKey,
		requestedSafeguards:    p.HasSafeguards,
		estInputTokens:         st.EstInputTokens,
		preferRussian:          p.PreferRussian,
		syntheticReasonTools:   p.SyntheticReasonTools,
		cachePrefixFrac:        st.CachePrefixFrac,
		cacheFrac1h:            st.CacheFrac1h,
		disableParallelToolUse: p.DisableParallelToolUse,
		reverseToolNameMap:     p.ReverseToolNameMap,
		hasPriorTurns:          p.HasPriorTurns,
		stopSequences:          p.StopSequences,
		maxStopLen:             maxStopLen,
		prefillText:            p.PrefillText,
		visibleCap:             p.MaxTokens,
		retryEmptyMaxToken:     !st.OutputAllowanceBumped,
	}
}

// closeCurrentBlock closes whatever content block is open, flushing a
// thinking block's own signature_delta first — skipping that silently is
// what pushes a replayed conversation toward the
// "skip_thought_signature_validator" placeholder (internal/translate, once
// ported) since the real signature was never captured and forwarded.
func (f *FSM) closeCurrentBlock() error {
	if f.currentBlock == BlockNone || f.currentIndex < 0 {
		return nil
	}
	if f.currentBlock == BlockText && f.textHoldBuffer != "" {
		_ = f.FlushTextHoldBuffer("")
	}
	if f.currentBlock == BlockThinking {
		f.thinkingFinished = true
		sig := f.thoughtSignature
		if sig == "" || sig == "skip_thought_signature_validator" {
			if f.lastTurnThoughtSignature != "" && f.lastTurnThoughtSignature != "skip_thought_signature_validator" {
				sig = f.lastTurnThoughtSignature
			}
		}
		// GAP-TH-06: cache the real signature at the session level too,
		// not just under a tool_use id — a pure-text turn (no tool call)
		// has no id for internal/translate's replay-side fallback to
		// recover a real signature by.
		if sig == "" || sig == "skip_thought_signature_validator" {
			if cached := toolsig.GetSessionSignature(f.cwdKey); cached != "" && cached != "skip_thought_signature_validator" {
				sig = cached
			}
		}
		if sig == "" {
			sig = "skip_thought_signature_validator"
		} else {
			toolsig.SetSessionSignature(f.cwdKey, sig)
			f.lastTurnThoughtSignature = sig
		}

		// Strict Anthropic SSE event ordering: signature_delta MUST precede
		// content_block_stop for this block index. A content_block_delta after
		// content_block_stop violates the accumulator contract.
		sigPayload := map[string]interface{}{
			"type":  "content_block_delta",
			"index": f.currentIndex,
			"delta": map[string]interface{}{
				"type":      "signature_delta",
				"signature": sig,
			},
		}
		b, _ := json.Marshal(sigPayload)
		if err := f.writer.EmitEvent("content_block_delta", string(b)); err != nil {
			return err
		}
	}
	f.thoughtSignature = ""
	stopPayload := map[string]interface{}{
		"type":  "content_block_stop",
		"index": f.currentIndex,
	}
	b, _ := json.Marshal(stopPayload)
	f.currentBlock = BlockNone
	return f.writer.EmitEvent("content_block_stop", string(b))
}

// HandleThinking streams one thinking delta, opening a new "thinking"
// block first if needed. Gemini can interleave thought->text->thought;
// each later thought becomes its own additional thinking block (valid in
// the Anthropic content array) rather than being folded into HandleText —
// leaking reasoning tokens into the user-visible text stream would be far
// worse. Thinking after a tool_use block is still dropped: the turn ends
// at the tool call.
func (f *FSM) HandleThinking(text string, thoughtSignature string) error {
	if err := f.EnsureMessageStart(); err != nil {
		return err
	}
	if f.toolUseOccurred && f.currentBlock != BlockThinking {
		return nil
	}
	if f.currentBlock != BlockThinking {
		if err := f.closeCurrentBlock(); err != nil {
			return err
		}
		f.currentIndex++
		f.currentBlock = BlockThinking

		startPayload := map[string]interface{}{
			"type":  "content_block_start",
			"index": f.currentIndex,
			"content_block": map[string]interface{}{
				"type":     "thinking",
				"thinking": "",
			},
		}
		b, _ := json.Marshal(startPayload)
		if err := f.writer.EmitEvent("content_block_start", string(b)); err != nil {
			return err
		}
	}
	if thoughtSignature != "" {
		f.thoughtSignature = thoughtSignature
		f.lastTurnThoughtSignature = thoughtSignature
	}

	deltaPayload := map[string]interface{}{
		"type":  "content_block_delta",
		"index": f.currentIndex,
		"delta": map[string]interface{}{
			"type":     "thinking_delta",
			"thinking": text,
		},
	}
	f.thinkingChars += len(text)
	f.lastThinking += text
	b, _ := json.Marshal(deltaPayload)
	return f.writer.EmitEvent("content_block_delta", string(b))
}

// HandleText streams one text delta, opening a new "text" block first if
// needed, and applying the assistant-prefill exactly once via
// ApplyPrefill (prefill.go). Text arriving after a tool_use has already
// fired in this turn is dropped: Anthropic requires tool_use to be the
// LAST content block whenever stop_reason ends up "tool_use", and a
// strict client parsing content[len-1] as the tool call breaks outright
// if a text block follows it.
func (f *FSM) HandleText(text string) error {
	if err := f.EnsureMessageStart(); err != nil {
		return err
	}
	if f.toolUseOccurred {
		return nil
	}
	f.lastText += text
	text = f.ApplyPrefill(text)
	text = sanitize.StripSyntheticXMLTags(text)
	if loopguard.HasIgnoreLoopTag(text) {
		text = loopguard.StripIgnoreLoopTag(text)
	}
	if text == "" {
		return nil
	}
	if f.thoughtSignature != "" {
		f.updatePendingThinkingSignature(f.thoughtSignature)
	}

	f.textHoldBuffer += text
	f.textHoldBuffer = sanitize.StripSyntheticXMLTags(f.textHoldBuffer)

	holdLen := f.maxStopLen
	if partialTag := sanitize.PartialSyntheticTagSuffix(f.textHoldBuffer); partialTag != "" {
		if len(partialTag) > holdLen {
			holdLen = len(partialTag)
		}
	}

	if holdLen > 0 {
		if len(f.textHoldBuffer) <= holdLen {
			return nil
		}
		emitLen := len(f.textHoldBuffer) - holdLen
		for emitLen > 0 && !utf8.RuneStart(f.textHoldBuffer[emitLen]) {
			emitLen--
		}
		if emitLen <= 0 {
			return nil
		}
		text = f.textHoldBuffer[:emitLen]
		f.textHoldBuffer = f.textHoldBuffer[emitLen:]
	} else {
		text = f.textHoldBuffer
		f.textHoldBuffer = ""
	}

	if err := f.emitTextDelta(text); err != nil {
		return err
	}
	if f.capReached {
		// The client's max_tokens is spent: stop forwarding anything else
		// and end the turn (a no-op when already inside FinishStream).
		f.textHoldBuffer = ""
		return f.FinishStream("MAX_TOKENS")
	}
	return nil
}

// HasFinished reports whether FinishStream or FinishWithError has already completed.
func (f *FSM) HasFinished() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hasFinished
}
