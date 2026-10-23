// Part of the translate package — see translate.go for the package doc
// comment. Direct port of v1's translator.go Steps 7-8: thinking/output
// token configuration with dynamic model limits, sampling
// hyperparameters, structured outputs, and uncensored safety settings.
package translate

import (
	"encoding/json"
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"claudgravity/internal/accounts"
	"claudgravity/internal/logx"
	"claudgravity/internal/schema"
)

// applyGenerationConfig fills out's generationConfig and safetySettings.
// Returns the updated out and the possibly-adjusted thinking budget (v1
// can lower budget to fit under a model's output ceiling — see the
// "budget is at ceiling" case below — so the caller's own returned
// budget must reflect that adjustment, not just the value tiering first
// picked).
func applyGenerationConfig(out string, root gjson.Result, modelName string, budget int, isAntigravity bool) (string, int) {
	modelMeta, hasMeta := accounts.ModelCatalog().GetModel(modelName)

	// GAP-P0-01/GAP-TL-01/GAP-TH-05: Gemini unconditionally rejects a
	// request that combines forced function calling
	// (toolConfig.functionCallingConfig.mode == "ANY", set by
	// applyToolsDeclaration — which always runs before this function,
	// see translate.go) with thinkingConfig, failing the WHOLE request
	// with "Thinking is not supported when function_calling_config.mode
	// is ANY". An earlier version of this fix forced budget=0 but still
	// entered the block below, which could re-raise budget right back up
	// via the MinThinkingBudget floor on a model with a nonzero floor
	// (GAP-TL-01) — and separately, it's unverified whether Gemini
	// objects to a nonzero budget specifically or to thinkingConfig's
	// mere PRESENCE alongside mode:ANY at all (GAP-TH-05, unresolved
	// without a live call). Skip the whole block — and the floor logic
	// inside it — entirely when mode is forced to ANY, the strictly
	// safer reading of both.
	forcedAny := gjson.Get(out, "toolConfig.functionCallingConfig.mode").String() == "ANY"

	requestedMax := root.Get("max_tokens").Int()
	isZeroMaxTokens := root.Get("max_tokens").Exists() && requestedMax == 0
	if isZeroMaxTokens {
		budget = 0
	}

	// budget==0 is a real, deliberate signal: the standard Anthropic
	// "thinking": {"type": "disabled"} request maps to it, and it must
	// still send an explicit thinkingBudget:0 rather than fall through to
	// Gemini's own default (models that think by default would otherwise
	// keep thinking with no cap at all). Only known-false
	// (hasMeta && !modelMeta.Thinking) skips thinkingConfig entirely — a
	// 3rd-party native-passthrough model that genuinely doesn't support
	// thinking gets Thinking is not supported for this model if sent one;
	// an unrecognized model still gets the existing best-effort behavior.
	internalReasoning := 0
	if !forcedAny && (!hasMeta || modelMeta.Thinking) {
		// GAP-TH-04: the floor only applies to a STRICTLY POSITIVE
		// budget about to be sent — budget==0 is the client's explicit
		// "disabled" request, which must stay disabled even on a model
		// with a nonzero MinThinkingBudget floor. The floor's own
		// purpose (a model whose thinking can't be fully turned off)
		// only matters once thinking is already going to be active;
		// re-enabling it from an explicit 0 defeated the client's own
		// "disabled" request.
		if budget > 0 && hasMeta && modelMeta.MinThinkingBudget > 0 && budget < modelMeta.MinThinkingBudget {
			budget = modelMeta.MinThinkingBudget
		}
		// GAP-TH-08: thinking.display=="omitted" asks for thinking to
		// still run (influencing output quality/budget) but never be
		// RETURNED to the client — distinct from budget==0 (thinking
		// doesn't run at all). Gemini's own knob for "run it but don't
		// return it" is includeThoughts:false.
		includeThoughts := budget != 0
		sendBudget := budget
		if budget == 0 && hasMeta && modelMeta.MinThinkingBudget > 0 {
			// The client did not enable thinking, but this model cannot
			// switch it off (catalog MinThinkingBudget > 0). Run the
			// smallest reasoning the model allows, never return it
			// (includeThoughts stays false), and reserve exactly that
			// much extra maxOutputTokens below so it cannot starve the
			// visible answer. Visible output is still capped at the
			// client's max_tokens by the stream layer.
			internalReasoning = modelMeta.MinThinkingBudget // thinkingBudget sent = the model's smallest allowed
			sendBudget = internalReasoning
		}
		if root.Get("thinking.display").String() == "omitted" {
			includeThoughts = false
		}
		out, _ = sjson.Set(out, "generationConfig.thinkingConfig.includeThoughts", includeThoughts)
		if budget < 0 {
			if isAntigravity {
				// -1 enables ModeAuto unbracketed — Antigravity/CloudCode
				// only.
				out, _ = sjson.Set(out, "generationConfig.thinkingConfig.thinkingBudget", -1)
			} else {
				// GAP-TH-03: the public Gemini Developer API (a direct
				// API key, not Antigravity/CloudCode) rejects -1 with
				// HTTP 400 — it has no ModeAuto. Send a concrete budget
				// instead of the auto sentinel.
				out, _ = sjson.Set(out, "generationConfig.thinkingConfig.thinkingBudget", 8192)
			}
		} else {
			out, _ = sjson.Set(out, "generationConfig.thinkingConfig.thinkingBudget", sendBudget)
		}
	}
	if forcedAny {
		budget = 0
		out, _ = sjson.Delete(out, "generationConfig.thinkingConfig")
	}

	// GAP-T5: whether thinking is actually ACTIVE for this model/budget —
	// not just "budget is nonzero". A model this function already decided
	// not to send thinkingConfig to at all (hasMeta && !modelMeta.Thinking,
	// the outer `if !hasMeta || modelMeta.Thinking` above never ran) must
	// not have its temperature forced to 1.0 either: tiering.go's budget
	// default (8192) is computed independently of whether the resolved
	// model even supports thinking, so budget!=0 alone used to force
	// temperature:1.0 on every request to a non-thinking model (e.g.
	// gemini-2.5-flash-lite, which never matches tiering's own
	// "gemini-2.5-flash" exact-string special case), silently breaking
	// temperature:0 determinism for code generation on exactly the
	// cheapest/fastest model family that most needs it.
	thinkingActive := !forcedAny && (!hasMeta || modelMeta.Thinking) && budget != 0

	// Carry over sampling hyperparameters: previously dropped entirely,
	// so a client asking for temperature:0 (deterministic code
	// generation) silently got Gemini's own default (commonly ~1.0)
	// instead.
	if temp := root.Get("temperature"); temp.Exists() {
		t := temp.Float()
		if thinkingActive && t != 1.0 {
			// Anthropic's own spec requires temperature == 1.0 whenever
			// thinking is enabled (any other value is rejected outright
			// on the real API) — passing a client's temperature:0 (the
			// common default for deterministic code generation) straight
			// through on top of a thinking budget measured live to
			// degrade Gemini 2.5/3.8's reasoning into repetitive thought
			// loops instead of erroring, a much harder failure to notice
			// and debug.
			t = 1.0
		}
		out, _ = sjson.Set(out, "generationConfig.temperature", t)
	}
	if topP := root.Get("top_p"); topP.Exists() {
		out, _ = sjson.Set(out, "generationConfig.topP", topP.Float())
	}
	if topK := root.Get("top_k"); topK.Exists() {
		// GAP-T6 (partial, conservative): top_k<=0 is never a valid
		// sampling value on any Gemini model generation — forwarding it
		// verbatim earns an unconditional 400. The claimed upper bound
		// (40) is model-dependent and NOT independently confirmed against
		// this gateway's actual (newer, gemini-3.8-class) model family, so
		// it's deliberately left unclamped here rather than risk silently
		// degrading sampling diversity on a model that accepts a higher
		// value just fine.
		if tk := topK.Int(); tk > 0 {
			out, _ = sjson.Set(out, "generationConfig.topK", tk)
		}
	}
	if stopSeqs := root.Get("stop_sequences"); stopSeqs.IsArray() {
		// GAP-T7: an empty string or a duplicate stop sequence both fail
		// the whole request with a 400 — filter both before the 5-entry
		// cap below, so a client-side duplicate doesn't also crowd out a
		// genuinely distinct 5th sequence.
		seen := make(map[string]bool)
		var seqs []string
		for _, s := range stopSeqs.Array() {
			v := s.String()
			if v == "" || seen[v] {
				continue
			}
			seen[v] = true
			seqs = append(seqs, v)
		}
		// Google hard-caps generationConfig.stopSequences at 5 entries —
		// anything past that fails the whole request with a 400
		// INVALID_ARGUMENT instead of just ignoring the extras.
		if len(seqs) > 5 {
			logx.LogGateway("[Translate] client sent %d stop_sequences, truncating to Gemini's 5-entry limit (dropped: %v)", len(seqs), seqs[5:])
			seqs = seqs[:5]
		}
		if len(seqs) > 0 {
			seqsJSON, _ := json.Marshal(seqs)
			out, _ = sjson.SetRaw(out, "generationConfig.stopSequences", string(seqsJSON))
		}
	}

	// Structured Outputs: translate Anthropic output_config.format into
	// Gemini responseSchema. Some official SDK call shapes (e.g.
	// client.messages.parse()) send the format at the request's TOP
	// level as "output_format" instead of nested under "output_config" —
	// fall back to that root field when the nested one is absent.
	fmtNode := root.Get("output_config.format")
	if !fmtNode.Exists() {
		fmtNode = root.Get("output_format")
	}
	if fmtNode.Exists() {
		switch fmtNode.Get("type").String() {
		case "json_schema":
			out, _ = sjson.Set(out, "generationConfig.responseMimeType", "application/json")
			if schemaNode := fmtNode.Get("schema"); schemaNode.Exists() {
				cleanedSchema := schema.CleanJSONSchemaForGemini(schemaNode.Raw)
				out, _ = sjson.SetRaw(out, "generationConfig.responseSchema", cleanedSchema)
			}
		case "json", "json_object":
			// A non-strict free-form JSON request (no "schema" key, just
			// {"type":"json"} — "any valid JSON, no fixed shape
			// required") must still tell the model to actually emit JSON,
			// or it commonly wraps its answer in a markdown code fence
			// instead of raw JSON.
			out, _ = sjson.Set(out, "generationConfig.responseMimeType", "application/json")
		}
	}

	if isZeroMaxTokens {
		// Anthropic cache pre-warming allows max_tokens: 0. Upstream Gemini requires
		// maxOutputTokens >= 1, so request 1 token with thinking disabled.
		requestedMax = 1
		budget = 0
		out, _ = sjson.Delete(out, "generationConfig.thinkingConfig")
	} else if requestedMax <= 0 {
		if hasMeta && modelMeta.MaxOutput > 0 {
			requestedMax = modelMeta.MaxOutput
		} else {
			requestedMax = 8192
		}
	} else if hasMeta && modelMeta.MaxOutput > 0 && requestedMax > modelMeta.MaxOutput {
		// Clamp requested tokens to model's certified ceiling (prevent
		// Google 400 Bad Request).
		requestedMax = modelMeta.MaxOutput
	}

	// Gemini's maxOutputTokens is a single shared ceiling for thinking AND
	// the visible answer combined — unlike Claude's max_tokens, which only
	// caps the visible output. Under ModeAuto (budget == -1) the model can
	// freely spend an unbounded share of that SAME ceiling on invisible
	// thinking before Claude Code's own (often small, e.g. 8192) max_tokens
	// request is even considered; whatever's left can truncate mid
	// tool-call-argument JSON. Give ModeAuto the model's full certified
	// ceiling regardless of what the client asked for — there's no cost
	// downside on this quota-based backend to requesting a higher ceiling
	// the model doesn't end up using.
	if !isZeroMaxTokens && budget < 0 {
		if hasMeta && modelMeta.MaxOutput > 0 {
			requestedMax = modelMeta.MaxOutput
		} else {
			requestedMax = 64000
		}
	}

	// For models with extended thinking enabled: Gemini shares maxOutputTokens
	// between reasoning thoughts and visible output. In Anthropic Messages API,
	// max_tokens strictly caps visible output. To prevent reasoning from starving
	// the visible output (and to ensure maxOutputTokens > budget), add budget to requestedMax
	// up to the model's certified ceiling.
	if !isZeroMaxTokens && budget > 0 {
		ceiling := int64(64000)
		if hasMeta && modelMeta.MaxOutput > 0 {
			ceiling = modelMeta.MaxOutput
		}
		combined := int64(budget) + requestedMax
		if combined <= ceiling {
			requestedMax = combined
		} else {
			requestedMax = ceiling
			if int64(budget) >= ceiling {
				budget = int(ceiling) - 1024
				out, _ = sjson.Set(out, "generationConfig.thinkingConfig.thinkingBudget", budget)
			}
		}
	}

	// Unavoidable internal reasoning of a model that cannot disable thinking
	// (client did not enable it): a generous allowance on top of max_tokens
	// (InternalReasoningAllowance, never below the model floor), clamped to
	// the model ceiling. The actual thinking is still capped by sending the
	// model's smallest allowed thinkingBudget above; the stream/non-stream
	// layers enforce the client's max_tokens on the VISIBLE answer.
	if !isZeroMaxTokens && internalReasoning > 0 {
		requestedMax += int64(internalAllowance(internalReasoning))
		if ceiling := outputCeiling(modelMeta, hasMeta); requestedMax > ceiling {
			requestedMax = ceiling
		}
	}

	out, _ = sjson.Set(out, "generationConfig.maxOutputTokens", requestedMax)

	// Uncensored Safety Settings.
	safetyCategories := []string{
		"HARM_CATEGORY_HARASSMENT",
		"HARM_CATEGORY_HATE_SPEECH",
		"HARM_CATEGORY_SEXUALLY_EXPLICIT",
		"HARM_CATEGORY_DANGEROUS_CONTENT",
		"HARM_CATEGORY_CIVIC_INTEGRITY",
	}
	for i, cat := range safetyCategories {
		out, _ = sjson.Set(out, fmt.Sprintf("safetySettings.%d.category", i), cat)
		threshold := "OFF"
		if !isAntigravity || cat == "HARM_CATEGORY_CIVIC_INTEGRITY" {
			threshold = "BLOCK_NONE"
		}
		out, _ = sjson.Set(out, fmt.Sprintf("safetySettings.%d.threshold", i), threshold)
	}

	return out, budget
}
