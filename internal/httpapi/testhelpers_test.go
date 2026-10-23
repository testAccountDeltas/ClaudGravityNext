package httpapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"claudgravity/internal/config"
)

// newTestConfigManager writes a minimal config.json with one tenant
// (keyed by apiKey) and loads it, matching internal/config's own
// characterization-test pattern (writeTestConfig + LoadConfig) — a real
// *config.ConfigManager, not a hand-built struct, since its fields are
// private.
func newTestConfigManager(t *testing.T, apiKey string) *config.ConfigManager {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{
		"tenants": map[string]interface{}{
			apiKey: map[string]interface{}{
				"name":               "test-tenant",
				"google_auth_type":   "api_key",
				"google_api_key":     "YOUR_GEMINI_API_KEY_HERE",
				"default_model":      "gemini-3.8-flash-tiered",
				"strip_search_tools": true,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal test config: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, body, 0644); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	cm, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return cm
}
