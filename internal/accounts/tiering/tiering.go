// Package tiering is a port of superrelay (v1)'s tiering.go — a
// separate subpackage of internal/accounts per
// ARCHITECTURE_V2_SPECIFICATION_AND_MIGRATION_PLAN.md Part 6.5: this
// logic is about the REQUEST (which model/effort to route to), not about
// the account pool itself, so the package boundary is explicit rather
// than "it happened to live next to auth_manager.go in v1". Fully
// self-contained — no dependency on internal/accounts' own types.
package tiering

import "strings"

// ResolveTieringAndEffort handles dynamic tiering and effort mapping:
// 1. Prioritizes ultra-fast gemini-3.8-flash-tiered with prompt caching for claude-* models.
// 2. Maps *-tiered models based on output_config.effort or auxiliary requests.
// 3. Configures thinking budget:
//   - low -> 2048 (gemini-3.8-flash-low)
//   - medium -> 8192 (gemini-3.8-flash-medium, safe production default)
//   - high -> 16384 (gemini-3.8-flash-high)
//   - xhigh / max / ultracode -> -1 (gemini-3.8-flash-tiered with Native Google ModeAuto)
//   - auxiliary -> 2048 (gemini-3.8-flash-low, eliminates 20s stalls on metadata)
func ResolveTieringAndEffort(claudeReqJSON map[string]interface{}, reqClass string, agentKey string) (string, int) {
	model, _ := claudeReqJSON["model"].(string)

	// An audit caught that this function only ever checked reqClass ==
	// "auxiliary" for the cheap/fast tier — reqClass == "subagent" (the
	// generic Task-tool fallback) was never special-cased at all, and a
	// lightweight Explore subagent (ParseAgentInfo's agentKey ==
	// "sub:Explore") inherited the SAME heavy ModeAuto/unlimited-thinking
	// budget as the main Opus-class thread, adding 30-60s of thinking
	// latency to what should be a quick, cheap search. Explore (and any
	// other investigation-only subagent with no edit/write tools, by
	// convention named the same way) gets the auxiliary-style cheap tier
	// treatment below instead.
	// An audit caught this only ever matched "sub:Explore" literally —
	// other read-only, investigation-only subagent types (doc lookup,
	// trivial config edits, a read-only transcript scan) got the SAME
	// heavy ModeAuto/unlimited-thinking treatment as a full Opus-class
	// coding subagent, for the same reason Explore originally needed
	// this fix. Scoped to the specific known-lightweight types rather
	// than "any subagent", since most subagent types (general-purpose,
	// code-reviewer, etc.) genuinely do need full reasoning capacity.
	isLightweightSubagent := false
	switch strings.ToLower(agentKey) {
	case "sub:explore", "sub:claude-code-guide", "sub:statusline-setup", "sub:fewer-permission-prompts":
		isLightweightSubagent = true
	}

	// 1. Explicit native models preservation
	if model == "claude-sonnet-4-6" || model == "claude-opus-4-6-thinking" || model == "gpt-oss-120b-medium" {
		// Native 3P models supported by CloudCode - keep if explicitly requested
	} else if strings.HasPrefix(model, "claude-opus") || strings.HasPrefix(model, "claude-3-opus") ||
		strings.HasPrefix(model, "claude-fable") || strings.HasPrefix(model, "claude-mythos") ||
		model == "opus" || model == "fable" || model == "mythos" {
		// Canonical mapping matching Python relay-proxy: Opus maps to gemini-3.8-flash-tiered
		// An audit caught that Claude Code's Agent/Task tool schema allows
		// the bare aliases "sonnet"/"opus"/"haiku"/"fable" (no "claude-"
		// prefix) for a subagent's model field — none of the HasPrefix
		// checks here matched those, so every subagent requesting the
		// CHEAPEST tier ("haiku") silently fell through to the
		// unmapped-model fallback below and got escalated to the
		// heaviest ModeAuto/unlimited-thinking tier instead, burning
		// 30-60s of extra TTFT and quota on what should've been a quick,
		// cheap subagent call. Handle the bare aliases explicitly.
		model = "gemini-3.8-flash-tiered"
	} else if strings.HasPrefix(model, "claude-sonnet") || strings.HasPrefix(model, "claude-3-5-sonnet") || strings.HasPrefix(model, "claude-3-7-sonnet") || strings.HasPrefix(model, "claude-3-sonnet") || model == "sonnet" {
		model = "gemini-3.8-flash-medium"
	} else if strings.HasPrefix(model, "claude-haiku") || strings.HasPrefix(model, "claude-3-5-haiku") || strings.HasPrefix(model, "claude-3-haiku") || model == "haiku" {
		model = "gemini-2.5-flash"
	} else if model != "" && !strings.HasPrefix(model, "gemini-") {
		// Fallback for unmapped or legacy models (claude-2.1, claude-instant, etc.)
		model = "gemini-3.8-flash-tiered"
	}
	// A Round 15 audit caught that an empty model (the request simply
	// didn't set one) fell into the generic unmapped/legacy fallback
	// above like any other unrecognized name, forcing
	// "gemini-3.8-flash-tiered" — which made the translator's own
	// `if modelName == "" { modelName = tenant.DefaultModel }` fallback
	// permanently dead code, since this function could never actually
	// return "". Leaving model empty here doesn't break the rest of this
	// function: every later check below is an explicit prefix/equality
	// match against a real model name, so an empty model simply falls
	// through to the generic non-tiered budget defaults, same as any
	// other model this function doesn't specifically recognize.

	effort := ""
	if outCfg, ok := claudeReqJSON["output_config"].(map[string]interface{}); ok {
		if eff, ok := outCfg["effort"].(string); ok {
			effort = strings.ToLower(eff)
		}
	}
	// Mid-conversation messages can carry per-turn output_config (e.g. dynamic effort level)
	// Only scan messages if effort is not explicitly set at the top-level output_config!
	if effort == "" {
		if msgs, ok := claudeReqJSON["messages"].([]interface{}); ok {
			for i := len(msgs) - 1; i >= 0; i-- {
				if m, ok := msgs[i].(map[string]interface{}); ok {
					if outCfg, ok := m["output_config"].(map[string]interface{}); ok {
						if eff, ok := outCfg["effort"].(string); ok && eff != "" {
							effort = strings.ToLower(eff)
							break
						}
					}
				}
			}
		}
	}

	// 2. Dynamic sub-tier and budget mapping for Gemini 3.8 / tiered models
	budget := 8192 // default medium

	if strings.HasPrefix(model, "gemini-3.8-flash") || model == "gemini-3.8-flash-tiered" {
		// Opus/Fable/Mythos were already mapped to the top "tiered"
		// (ModeAuto) class above specifically because they're the
		// flagship tier — but Sonnet maps to "-medium" and enters this
		// exact same switch too, so without remembering which one this
		// was, the no-effort default case below silently downgraded every
		// Opus-class request with no explicit output_config.effort
		// (anything not setting CLAUDE_CODE_EFFORT_LEVEL) from "tiered"
		// down to plain "-medium" with a capped 8192 budget.
		wasTieredClass := model == "gemini-3.8-flash-tiered"
		base := "gemini-3.8-flash"
		if reqClass == "auxiliary" || isLightweightSubagent {
			model = base + "-low"
			budget = 2048
		} else {
			switch effort {
			case "low":
				if wasTieredClass {
					// Opus/Fable/Mythos never degrade all the way to the
					// bottom tier even on an explicit "low" — same floor
					// the rest of this class gets below.
					model = base + "-medium"
					budget = 8192
				} else {
					model = base + "-low"
					budget = 2048
				}
			case "medium", "high", "xhigh", "max", "ultracode":
				if wasTieredClass {
					model = "gemini-3.8-flash-tiered"
					budget = -1
				} else if effort == "medium" {
					model = base + "-medium"
					budget = 8192
				} else if effort == "high" {
					model = base + "-high"
					budget = 16384
				} else {
					model = "gemini-3.8-flash-tiered"
					budget = -1 // Native Google ModeAuto
				}
			case "none", "off", "disabled":
				// A Round 15 audit caught that these literal thinking-off
				// values (as opposed to the standard Anthropic
				// thinking.type=="disabled" object, or no effort at all)
				// fell into default: below, which for an Opus-class
				// request selected the HEAVIEST ModeAuto/unlimited-
				// thinking tier — the exact opposite of what was
				// explicitly asked for, costing ~30-40s of extra latency
				// and quota the caller was specifically trying to avoid.
				model = base + "-medium"
				budget = 0
			default:
				if wasTieredClass {
					model = "gemini-3.8-flash-tiered"
					budget = -1
				} else {
					// Default when no explicit effort is passed: balanced medium
					model = base + "-medium"
					budget = 8192
				}
			}
		}
	} else if strings.HasSuffix(model, "-tiered") {
		base := strings.TrimSuffix(model, "-tiered")
		if reqClass == "auxiliary" || isLightweightSubagent {
			model = base + "-low"
			budget = 2048
		} else {
			switch effort {
			case "low":
				model = base + "-low"
				budget = 2048
			case "medium":
				model = base + "-medium"
				budget = 8192
			case "high":
				model = base + "-high"
				budget = 16384
			case "xhigh", "max", "ultracode":
				model = base + "-tiered"
				budget = -1
			case "none", "off", "disabled":
				model = base + "-medium"
				budget = 0
			default:
				model = base + "-medium"
				budget = 8192
			}
		}
	} else {
		// Non-tiered models thinking budget
		//
		// An audit caught two related gaps for Haiku (gemini-2.5-flash),
		// whose whole point is a fast, cheap, thinking-free response:
		// (1) the auxiliary/lightweight-subagent branch below ran
		// UNCONDITIONALLY and forced budget=2048 even for Haiku, before
		// the switch's own Haiku zero-budget default case ever got a
		// chance to run; (2) Claude Code's CLAUDE_CODE_EFFORT_LEVEL env
		// var (read client-side, arriving here as output_config.effort)
		// is a GLOBAL default applied to every model including Haiku —
		// "medium"/"low" land straight in the switch's explicit case
		// branches, never reaching the Haiku-specific default either.
		// Only a clear, deliberate high-effort escalation (xhigh/max/
		// ultracode) is still honored for Haiku; that's unlikely to be a
		// mere inherited global default and more likely a genuine ask
		// for Haiku to actually reason hard on this one call.
		if model == "gemini-2.5-flash" && effort != "xhigh" && effort != "max" && effort != "ultracode" {
			budget = 0
		} else if strings.HasSuffix(model, "-thinking") && effort == "" {
			// A Round 15 audit caught that a "-thinking"-suffixed native
			// model (e.g. "claude-opus-4-6-thinking", preserved verbatim
			// by the native-models branch above) never matched any of
			// this switch's -high/-low/-medium suffix checks in its
			// default: case, so with no explicit effort it silently kept
			// budget at this function's generic 8192 initializer instead
			// of the unlimited ModeAuto reasoning its name and purpose
			// call for. Only applies with no explicit effort override —
			// an explicit low/medium/high/xhigh request below still wins.
			budget = -1
		} else if reqClass == "auxiliary" || isLightweightSubagent {
			budget = 2048
		} else {
			switch effort {
			case "low":
				budget = 2048
			case "medium":
				budget = 8192
			case "high":
				budget = 16384
			case "xhigh", "max", "ultracode":
				budget = -1
			case "none", "off", "disabled":
				budget = 0
			default:
				if strings.HasSuffix(model, "-high") {
					budget = 16384
				} else if strings.HasSuffix(model, "-low") {
					budget = 2048
				} else if strings.HasSuffix(model, "-medium") {
					budget = 8192
				}
			}
		}
	}

	// Standard Anthropic "thinking" object: explicit client request takes precedence
	// over output_config.effort (e.g. environment variable CLAUDE_CODE_EFFORT_LEVEL).
	if th, ok := claudeReqJSON["thinking"].(map[string]interface{}); ok {
		var btVal float64
		hasBT := false
		if v, ok := th["budget_tokens"].(float64); ok {
			btVal = v
			hasBT = true
		} else if v, ok := th["budget_tokens"].(int); ok {
			btVal = float64(v)
			hasBT = true
		} else if v, ok := th["budget_tokens"].(int64); ok {
			btVal = float64(v)
			hasBT = true
		}

		switch th["type"] {
		case "disabled":
			budget = 0
		case "enabled":
			if hasBT {
				if btVal == 0 {
					budget = 0
				} else if btVal > 0 {
					budget = int(btVal)
				}
			}
		case "adaptive":
			// ModeAuto is the closest Gemini-side equivalent
			// to "let the model decide its own thinking depth".
			budget = -1
		case "between_tools":
			// GAP-TH-09: fell into no case at all before, leaving
			// budget at whatever generic default was already
			// computed — same ModeAuto treatment as "adaptive", since
			// Gemini has no distinct "reason between tool calls"
			// knob of its own.
			budget = -1
		default:
			if hasBT && btVal == 0 {
				budget = 0
			}
		}
	}

	claudeReqJSON["model"] = model
	return model, budget
}
