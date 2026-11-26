package tiering

import "strings"

type tierSpec struct {
	prefix      string
	tieredModel string
	noneModel   string
}

var geminiSubTierSpecs = []tierSpec{
	{
		prefix:      "gemini-3.8-flash",
		tieredModel: "gemini-3.8-flash-tiered",
		noneModel:   "gemini-3.8-flash-medium",
	},
	{
		prefix:      "gemini-4-hydrogen",
		tieredModel: "gemini-4-hydrogen-tiered-xhigh",
		noneModel:   "gemini-4-hydrogen-low",
	},
}

// resolveGeminiSubTier maps Claude effort/request-class hints onto a Gemini
// 3.8 Flash (fable/sonnet) or Gemini 4 Hydrogen (opus) sub-tier model and
// thinking budget. Both families expose 4 Google levels; Claude's 5 effort
// options map to low, medium, high, tiered/ModeAuto (xhigh), and tiered/max.
func resolveGeminiSubTier(
	model, effort, reqClass string,
	midClass, isLightweightSubagent bool,
	midThinkingBudget, maxThinkingBudget int,
) (string, int, bool) {
	var spec *tierSpec
	for i := range geminiSubTierSpecs {
		if strings.HasPrefix(model, geminiSubTierSpecs[i].prefix) {
			spec = &geminiSubTierSpecs[i]
			break
		}
	}
	if spec == nil {
		return "", 0, false
	}

	wasTieredClass := model == spec.prefix ||
		model == spec.prefix+"-tiered" ||
		strings.HasPrefix(model, spec.prefix+"-tiered-")

	if reqClass == "auxiliary" || isLightweightSubagent {
		return spec.prefix + "-low", 2048, true
	}
	if midClass && effort != "low" && effort != "none" && effort != "off" && effort != "disabled" {
		return spec.prefix + "-medium", midThinkingBudget, true
	}

	switch effort {
	case "low":
		return spec.prefix + "-low", 2048, true
	case "medium":
		return spec.prefix + "-medium", 8192, true
	case "high":
		return spec.prefix + "-high", 16384, true
	case "xhigh", "ultracode":
		return spec.tieredModel, -1, true
	case "max":
		return spec.tieredModel, maxThinkingBudget, true
	case "none", "off", "disabled":
		return spec.noneModel, 0, true
	default:
		if wasTieredClass {
			return spec.tieredModel, -1, true
		}
		switch model {
		case spec.prefix + "-low":
			return model, 2048, true
		case spec.prefix + "-high":
			return model, 16384, true
		case spec.prefix + "-xhigh":
			return model, -1, true
		default:
			return spec.prefix + "-medium", 8192, true
		}
	}
}
