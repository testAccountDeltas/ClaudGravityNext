// Part of the accounts package's port from superrelay (v1)'s
// auth_manager.go — see pool.go for the split rationale. This file
// holds the cached model catalog.
package accounts

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

const CloudCodeModelsEndpoint = "https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels"

type AvailableModelInfo struct {
	ID                string  `json:"id"`
	DisplayName       string  `json:"display_name"`
	ModelProvider     string  `json:"model_provider"`
	MaxTokens         int64   `json:"max_tokens"`
	MaxOutput         int64   `json:"max_output_tokens"`
	Thinking          bool    `json:"supports_thinking"`
	ThinkingBudget    int     `json:"thinking_budget"`
	MinThinkingBudget int     `json:"min_thinking_budget"`
	QuotaRemaining    float64 `json:"quota_remaining"`
	ResetTime         string  `json:"reset_time,omitempty"`
}

type CachedModelCatalog struct {
	mu        sync.RWMutex
	fetchedAt time.Time
	models    []AvailableModelInfo
	modelMap  map[string]AvailableModelInfo
}

var globalModelCatalog = &CachedModelCatalog{
	modelMap: make(map[string]AvailableModelInfo),
}

func (c *CachedModelCatalog) GetModel(id string) (AvailableModelInfo, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m, ok := c.modelMap[id]
	return m, ok
}

func (c *CachedModelCatalog) GetAll() []AvailableModelInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]AvailableModelInfo, len(c.models))
	copy(out, c.models)
	return out
}

// ModelCatalog exposes the package-level singleton to callers outside
// this package (the dashboard, once ported) — same package-level-
// singleton-plus-accessor convention used throughout this migration.
func ModelCatalog() *CachedModelCatalog { return globalModelCatalog }

func (am *AccountManager) FetchAvailableModels(forceRefresh bool) ([]AvailableModelInfo, error) {
	globalModelCatalog.mu.RLock()
	if !forceRefresh && len(globalModelCatalog.models) > 0 && time.Since(globalModelCatalog.fetchedAt) < 10*time.Minute {
		cached := make([]AvailableModelInfo, len(globalModelCatalog.models))
		copy(cached, globalModelCatalog.models)
		globalModelCatalog.mu.RUnlock()
		return cached, nil
	}
	globalModelCatalog.mu.RUnlock()

	// An audit caught this used to pick only the FIRST eligible account
	// and `break`, so a token-refresh failure for that one specific
	// account (expired refresh token, a transient Google-side error)
	// returned an error for the WHOLE model catalog even when other
	// accounts in the pool had perfectly valid, refreshable tokens — a
	// single bad account was a single point of failure for every
	// caller's model list. Try each eligible account in turn instead.
	accounts := am.GetAccounts()
	var activeAcc *Record
	var accToken string
	var lastErr error
	for _, a := range accounts {
		if a.Disabled || a.RefreshToken == "" {
			continue
		}
		tok := a.AccessToken
		if tok == "" || time.Now().After(a.ExpiresAt) {
			refreshed, err := am.RefreshAccessToken(a.Email)
			if err != nil {
				lastErr = fmt.Errorf("refresh token for %s: %w", a.Email, err)
				continue
			}
			tok = refreshed
		}
		activeAcc = a
		accToken = tok
		break
	}
	if activeAcc == nil {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("no active Antigravity account available")
	}

	// The Antigravity client talks to the daily/sandbox CloudCode hosts; the
	// production host is only the last resort (same order as the quota calls).
	var respBytes []byte
	var fetchErr error
	for _, host := range QuotaHosts {
		req, err := http.NewRequest("POST", host+"/v1internal:fetchAvailableModels", bytes.NewReader([]byte("{}")))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+accToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", AntigravityUserAgent)

		resp, err := httpClient().Do(req)
		if err != nil {
			fetchErr = fmt.Errorf("do fetchAvailableModels: %w", err)
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			fetchErr = fmt.Errorf("fetchAvailableModels HTTP %d: %s", resp.StatusCode, string(b))
			continue
		}
		respBytes, fetchErr = b, nil
		break
	}
	if respBytes == nil {
		return nil, fetchErr
	}

	root := gjson.ParseBytes(respBytes)
	modelsObj := root.Get("models")
	if !modelsObj.Exists() {
		return nil, fmt.Errorf("no 'models' map in response")
	}

	var result []AvailableModelInfo
	modelsObj.ForEach(func(key, val gjson.Result) bool {
		modelKey := key.String()
		disp := val.Get("displayName").String()
		if disp == "" {
			disp = modelKey
		}
		prov := val.Get("modelProvider").String()
		maxTok := val.Get("maxTokens").Int()
		maxOut := val.Get("maxOutputTokens").Int()
		th := val.Get("supportsThinking").Bool()
		thBgt := int(val.Get("thinkingBudget").Int())
		minThBgt := int(val.Get("minThinkingBudget").Int())
		qRem := val.Get("quotaInfo.remainingFraction").Float()
		resetTime := val.Get("quotaInfo.resetTime").String()

		result = append(result, AvailableModelInfo{
			ID:                modelKey,
			DisplayName:       disp,
			ModelProvider:     prov,
			MaxTokens:         maxTok,
			MaxOutput:         maxOut,
			Thinking:          th,
			ThinkingBudget:    thBgt,
			MinThinkingBudget: minThBgt,
			QuotaRemaining:    qRem,
			ResetTime:         resetTime,
		})
		return true
	})

	mMap := make(map[string]AvailableModelInfo)
	for _, m := range result {
		mMap[m.ID] = m
	}

	globalModelCatalog.mu.Lock()
	globalModelCatalog.models = result
	globalModelCatalog.modelMap = mMap
	globalModelCatalog.fetchedAt = time.Now()
	globalModelCatalog.mu.Unlock()

	return result, nil
}
