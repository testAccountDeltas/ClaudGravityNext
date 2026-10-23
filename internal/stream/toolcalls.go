// Part of the stream package — see events.go for the package doc
// comment. Direct port of v1's HandleFunctionCall/nextToolCallID/
// normalizeToolCallArgsToObject/toolPrimaryStringParam — the one chunk of
// this file that stays a near-literal transcription, since the tool-call
// repair pipeline it drives (internal/editrepair, internal/schema) is
// already-ported, already-tested business logic, not something this
// rewrite has reason to touch.
package stream

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/sjson"

	"claudgravity/internal/editrepair"
	"claudgravity/internal/schema"
	"claudgravity/internal/toolargs"
	"claudgravity/internal/toolcallid"
	"claudgravity/internal/toolsig"
)

// nextToolCallID delegates to internal/toolcallid, shared with
// internal/orchestrate's non-streaming path — see that package's doc
// comment for why this moved out of being this file's own private copy.
func nextToolCallID(name string) string {
	return toolcallid.Next(name)
}

// normalizeToolCallArgsToObject delegates to internal/toolargs, shared
// with internal/translate's history-replay path — see that package's
// doc comment for why this moved out of being this file's own private
// copy.
func normalizeToolCallArgsToObject(name, argsRaw string) string {
	return toolargs.NormalizeToArgsObject(name, argsRaw)
}

// HandleFunctionCall runs a tool call's args through the full repair
// pipeline (numeric-enum coercion, Edit/Grep/Glob repair, synthetic
// "reason" placeholder stripping) and streams the resulting tool_use
// block.
func (f *FSM) HandleFunctionCall(name string, argsRaw string, thoughtSignature string) error {
	if f.disableParallelToolUse && f.toolUseOccurred {
		// Client asked for at most one tool call per turn — silently drop
		// any call past the first rather than streaming a second tool_use
		// block the client explicitly said it can't handle.
		return nil
	}
	argsRaw = normalizeToolCallArgsToObject(name, argsRaw)
	argsRaw = schema.CoerceNumericEnumArgs(f.sessionID, name, argsRaw)
	if repaired, ok := editrepair.RepairEditToolCall(name, argsRaw, f.cwdKey); ok {
		argsRaw = repaired
	}
	switch strings.ToLower(name) {
	case "grep":
		if repaired, ok := editrepair.RepairGrepToolCall(argsRaw); ok {
			argsRaw = repaired
		}
	case "glob":
		if repaired, ok := editrepair.RepairGlobToolCall(argsRaw); ok {
			argsRaw = repaired
		}
	}
	if f.syntheticReasonTools[name] {
		// This tool's Gemini-bound schema only has a "reason" property
		// because its real schema had none at all — never part of the
		// tool's actual declared arguments, and a strict handler
		// (additionalProperties: false) rejects an argument it never
		// declared.
		if cleaned, err := sjson.Delete(argsRaw, "reason"); err == nil {
			argsRaw = cleaned
		}
	}
	argsRaw = schema.StripNestedOpenDictReason(f.sessionID, name, argsRaw)
	if err := f.EnsureMessageStart(); err != nil {
		return err
	}

	effectiveSig := thoughtSignature
	if effectiveSig == "" && f.currentBlock == BlockThinking {
		effectiveSig = f.thoughtSignature
	}
	if effectiveSig == "" {
		effectiveSig = f.lastTurnThoughtSignature
	}
	if effectiveSig == "" {
		effectiveSig = toolsig.GetSessionSignature(f.cwdKey)
	}
	if f.currentBlock == BlockThinking && f.thoughtSignature == "" && effectiveSig != "" {
		f.thoughtSignature = effectiveSig
	}
	if effectiveSig != "" {
		f.updatePendingThinkingSignature(effectiveSig)
	}

	// An audit caught that the assistant prefill (prefill.go) was only
	// ever applied from HandleText — if the model's very first action in
	// the turn is a tool call, HandleText never runs at all and
	// f.prefillEmitted stays false forever, silently dropping the
	// prefill from the whole turn. Emit it here as its own leading text
	// block (HandleText("") with nothing else pending just streams the
	// prefill via ApplyPrefill) before the tool_use block opens below —
	// Anthropic allows a text block before tool_use, just not after it.
	// Note: Must run AFTER effectiveSig is attached to BlockThinking above,
	// because HandleText calls closeCurrentBlock() on any open thinking block.
	if f.prefillText != "" && !f.prefillEmitted {
		if err := f.HandleText(""); err != nil {
			return err
		}
	}

	if f.textHoldBuffer != "" {
		_ = f.FlushTextHoldBuffer("")
	}

	if err := f.closeCurrentBlock(); err != nil {
		return err
	}
	f.currentIndex++
	f.currentBlock = BlockToolUse
	f.toolUseOccurred = true
	f.toolNames = append(f.toolNames, name)
	f.toolCallSignatures = append(f.toolCallSignatures, fmt.Sprintf("%s:%s", name, argsRaw))

	callID := nextToolCallID(name)
	f.toolCallIDs = append(f.toolCallIDs, callID)
	setToolSignature(callID, effectiveSig)

	startPayload := map[string]interface{}{
		"type":  "content_block_start",
		"index": f.currentIndex,
		"content_block": map[string]interface{}{
			"type":  "tool_use",
			"id":    callID,
			"name":  name,
			"input": map[string]interface{}{},
		},
	}
	b, _ := json.Marshal(startPayload)
	if err := f.writer.EmitEvent("content_block_start", string(b)); err != nil {
		return err
	}

	deltaPayload := map[string]interface{}{
		"type":  "content_block_delta",
		"index": f.currentIndex,
		"delta": map[string]interface{}{
			"type":         "input_json_delta",
			"partial_json": argsRaw,
		},
	}
	b, _ = json.Marshal(deltaPayload)
	if err := f.writer.EmitEvent("content_block_delta", string(b)); err != nil {
		return err
	}

	return f.closeCurrentBlock()
}
