// Part of the config package's port from superrelay (v1)'s config.go —
// see config.go in this directory for the split rationale. This file
// holds TenantConfig and the API-key lifecycle operations.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// TenantConfig is one entry under config.json's ["tenants"] map, keyed by
// API key. GoogleAPIKey is never serialized to JSON to prevent credential leakage.
type TenantConfig struct {
	Name             string `json:"name"`
	GoogleAuthType   string `json:"google_auth_type"` // "api_key", "oauth_pool", "oauth_sticky"
	GoogleAPIKey     string `json:"-"`
	GoogleBaseURL    string `json:"google_base_url"` // default: https://generativelanguage.googleapis.com
	DefaultModel     string `json:"default_model"`
	ThinkingBudget   int    `json:"thinking_budget"` // -1 for auto / high
	StripSearchTools bool   `json:"strip_search_tools"`
	// StripSearchToolsGemini3 re-enables Grep/Glob stripping on Gemini 3.x
	// models (where they are declared by default).
	StripSearchToolsGemini3 bool   `json:"strip_search_tools_gemini3"`
	StickyAccount           string `json:"sticky_account"` // Specific email pinned to this API key
	Strategy                string `json:"strategy"`       // "sticky", "round_robin", "highest_quota"
}

// UnmarshalJSON preserves GoogleAPIKey when reading from config.json on disk
// while keeping it unexported in normal JSON serialization.
func (t *TenantConfig) UnmarshalJSON(data []byte) error {
	type Alias TenantConfig
	aux := &struct {
		GoogleAPIKey string `json:"google_api_key"`
		*Alias
	}{
		Alias: (*Alias)(t),
	}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	if aux.GoogleAPIKey != "" {
		t.GoogleAPIKey = aux.GoogleAPIKey
	}
	return nil
}

func (cm *ConfigManager) GetTenant(apiKey string) (TenantConfig, bool) {
	if strings.TrimSpace(apiKey) == "" {
		return TenantConfig{}, false
	}
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	t, ok := cm.cfg.Tenants[apiKey]
	return t, ok
}

func (cm *ConfigManager) CreateAPIKey(name string, stickyAccount string, strategy string, model string) (string, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	bytes := make([]byte, 16)
	_, _ = rand.Read(bytes)
	key := "sk-superrelay-" + hex.EncodeToString(bytes)

	if strategy == "" {
		strategy = "sticky"
	}
	if model == "" {
		model = "gemini-2.5-flash"
	}

	authType := "oauth_pool"
	if stickyAccount != "" {
		authType = "oauth_sticky"
	}

	tenant := TenantConfig{
		Name:           name,
		GoogleAuthType: authType,
		GoogleBaseURL:  "https://daily-cloudcode-pa.sandbox.googleapis.com",
		DefaultModel:   model,
		ThinkingBudget: -1,
		// Stripped by default: Grep/Glob's schema is rich enough (pattern,
		// path, glob, output_mode, -i/-n/-A/-B/-C, context, multiline,
		// head_limit, offset) that Gemini has real room to mangle a call —
		// forgetting the one required "pattern" field, confusing which
		// field a flag belongs in. Revisit once there's a defensive
		// repair pass for these two tools (same idea as RepairEditToolCall)
		// or concrete live evidence Gemini actually handles them cleanly
		// through this schema.
		StripSearchTools: true,
		StickyAccount:    stickyAccount,
		Strategy:         strategy,
	}

	cm.cfg.Tenants[key] = tenant

	data, err := json.MarshalIndent(cm.cfg, "", "  ")
	if err == nil {
		_ = atomicWriteFile(cm.path, data)
	}

	return key, nil
}

func (cm *ConfigManager) DeleteAPIKey(apiKey string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	delete(cm.cfg.Tenants, apiKey)
	data, err := json.MarshalIndent(cm.cfg, "", "  ")
	if err == nil {
		_ = atomicWriteFile(cm.path, data)
	}
	return nil
}

func (cm *ConfigManager) GetAllTenants() map[string]TenantConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	res := make(map[string]TenantConfig)
	for k, v := range cm.cfg.Tenants {
		res[k] = v
	}
	return res
}
