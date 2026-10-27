// Part of the tiering package. By default every Claude family alias is served
// by a Gemini model. An operator can point a family (for now: sonnet, opus,
// haiku) at one of the native third-party models the CloudCode backend also
// hosts, via server.model_overrides in config.json.
package tiering

import (
	"strings"
	"sync"
)

// NativeModels are the third-party models the gateway passes through as-is.
var NativeModels = []string{"claude-sonnet-4-6", "claude-opus-4-6-thinking", "gpt-oss-120b-medium"}

// IsNativeModel reports whether id is one of NativeModels.
func IsNativeModel(id string) bool {
	for _, m := range NativeModels {
		if id == m {
			return true
		}
	}
	return false
}

var (
	overridesMu sync.RWMutex
	overrides   = map[string]string{}
)

// SetFamilyOverrides installs the family -> native model map. Unknown families
// and targets that are not native models are ignored (returned as skipped) so
// a typo in the configuration can never route traffic to a model that does not
// exist.
func SetFamilyOverrides(m map[string]string) (skipped []string) {
	next := map[string]string{}
	for fam, target := range m {
		fam = strings.ToLower(strings.TrimSpace(fam))
		target = strings.TrimSpace(target)
		switch fam {
		case "sonnet", "opus", "haiku":
		default:
			skipped = append(skipped, fam)
			continue
		}
		if !IsNativeModel(target) {
			skipped = append(skipped, fam+"="+target)
			continue
		}
		next[fam] = target
	}
	overridesMu.Lock()
	overrides = next
	overridesMu.Unlock()
	return skipped
}

func familyOverride(fam string) string {
	overridesMu.RLock()
	defer overridesMu.RUnlock()
	return overrides[fam]
}
