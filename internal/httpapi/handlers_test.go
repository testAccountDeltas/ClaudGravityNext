package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleModels_RequiresAuth(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	handleModels(cm, w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestHandleModels_EmptyOrWhitespaceKeyIs401(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	for _, headerVal := range []string{"", "   ", "Bearer ", "Bearer    "} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		if strings.HasPrefix(headerVal, "Bearer") {
			r.Header.Set("Authorization", headerVal)
		} else {
			r.Header.Set("x-api-key", headerVal)
		}
		handleModels(cm, w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("key %q: status = %d, want %d", headerVal, w.Code, http.StatusUnauthorized)
		}
	}
}

func TestHandleModels_ListsKnownModels(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	r.Header.Set("x-api-key", "sk-good")
	handleModels(cm, w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var resp struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Data) == 0 {
		t.Fatal("expected at least the static knownModels entries")
	}
}

func TestHandleModelByID_UnknownReturns404(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/models/does-not-exist", nil)
	r.Header.Set("x-api-key", "sk-good")
	r.SetPathValue("id", "does-not-exist")
	handleModelByID(cm, w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHandleModelByID_AliasResolves(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/models/sonnet", nil)
	r.Header.Set("x-api-key", "sk-good")
	r.SetPathValue("id", "sonnet")
	handleModelByID(cm, w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d — bare subagent-model aliases must resolve", w.Code, http.StatusOK)
	}
}

func TestHandleCountTokens_RejectsEmptyMessages(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(`{"model":"m","messages":[]}`))
	r.Header.Set("x-api-key", "sk-good")
	handleCountTokens(cm, w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleCountTokens_ReturnsPositiveEstimate(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	w := httptest.NewRecorder()
	body := `{"model":"claude-opus-4-5","messages":[{"role":"user","content":"hello there, how are you today?"}]}`
	r := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(body))
	r.Header.Set("x-api-key", "sk-good")
	handleCountTokens(cm, w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	var resp struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.InputTokens < 1 {
		t.Errorf("input_tokens = %d, want >= 1", resp.InputTokens)
	}
	// Every other successful response sets organization/workspace
	// headers — count_tokens must not be the one exception.
	if w.Header().Get("anthropic-organization-id") == "" {
		t.Error("anthropic-organization-id missing on a successful count_tokens response")
	}
}

func TestHandleCountTokens_MergeAnthropicBetaHeader(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	w := httptest.NewRecorder()
	body := `{"model":"claude-3-7-sonnet-20250219","messages":[{"role":"user","content":"count these tokens"}],"betas":["existing-beta"]}`
	r := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(body))
	r.Header.Set("x-api-key", "sk-good")
	r.Header.Add("anthropic-beta", "token-counting-2024-11-01")
	r.Header.Add("anthropic-beta", "prompt-caching-2024-07-31, existing-beta")
	handleCountTokens(cm, w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	var resp struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.InputTokens < 1 {
		t.Errorf("input_tokens = %d, want >= 1", resp.InputTokens)
	}
}

func TestHandleUsage_NoAccountPoolReturnsSyntheticFallback(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	r.Header.Set("x-api-key", "sk-good")
	handleUsage(cm, w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var resp struct {
		Account string `json:"account"`
		Groups  []struct {
			Buckets []struct {
				RemainingPercent float64 `json:"remaining_percent"`
			} `json:"buckets"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Account != "" {
		t.Errorf("account = %q, want empty (no OAuth pool for an api_key tenant)", resp.Account)
	}
	if len(resp.Groups) == 0 || len(resp.Groups[0].Buckets) == 0 {
		t.Fatal("expected a synthetic usage group/bucket, got none")
	}
}

func TestLegacyHandlers(t *testing.T) {
	cases := []struct {
		name       string
		handler    http.HandlerFunc
		wantStatus int
	}{
		{"deprecated complete", handleDeprecatedComplete, http.StatusBadRequest},
		{"unsupported batches", handleUnsupportedBatches, http.StatusBadRequest},
		{"unknown v1 path", handleUnknownV1Path, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/v1/whatever", nil)
			tc.handler(w, r)
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tc.wantStatus)
			}
		})
	}
}

func TestBuildModelCapabilities_HierarchicalStructure(t *testing.T) {
	capsThinking := buildModelCapabilities(true, true)
	if img, ok := capsThinking["image_input"].(map[string]interface{}); !ok || img["supported"] != true {
		t.Errorf("image_input.supported = %v, want true", capsThinking["image_input"])
	}
	if so, ok := capsThinking["structured_outputs"].(map[string]interface{}); !ok || so["supported"] != true {
		t.Errorf("structured_outputs.supported = %v, want true", capsThinking["structured_outputs"])
	}
	th, ok := capsThinking["thinking"].(map[string]interface{})
	if !ok || th["supported"] != true {
		t.Fatalf("thinking.supported = %v, want true", capsThinking["thinking"])
	}
	types, ok := th["types"].(map[string]interface{})
	if !ok {
		t.Fatalf("thinking.types missing")
	}
	if en, ok := types["enabled"].(map[string]interface{}); !ok || en["supported"] != true {
		t.Errorf("thinking.types.enabled.supported = %v, want true", types["enabled"])
	}
	if ad, ok := types["adaptive"].(map[string]interface{}); !ok || ad["supported"] != true {
		t.Errorf("thinking.types.adaptive.supported = %v, want true", types["adaptive"])
	}
	eff, ok := capsThinking["effort"].(map[string]interface{})
	if !ok || eff["supported"] != true {
		t.Fatalf("effort.supported = %v, want true", capsThinking["effort"])
	}
	for _, level := range []string{"low", "medium", "high", "xhigh", "max"} {
		if lvl, ok := eff[level].(map[string]interface{}); !ok || lvl["supported"] != true {
			t.Errorf("effort.%s.supported = %v, want true", level, eff[level])
		}
	}

	capsNoThinking := buildModelCapabilities(false, true)
	if th, ok := capsNoThinking["thinking"].(map[string]interface{}); !ok || th["supported"] != false {
		t.Errorf("thinking.supported = %v, want false", capsNoThinking["thinking"])
	}
	if _, ok := capsNoThinking["effort"]; ok {
		t.Errorf("effort should not be present when thinking is false")
	}
}

func TestNewModelsInCatalog(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	modelsToCheck := []string{
		"claude-opus-5",
		"claude-sonnet-5",
		"claude-fable-5",
		"claude-mythos-5",
		"claude-mythos-5-1",
	}

	for _, modelID := range modelsToCheck {
		t.Run(modelID, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/v1/models/"+modelID, nil)
			r.Header.Set("x-api-key", "sk-good")
			r.SetPathValue("id", modelID)
			handleModelByID(cm, w, r)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
			}
			var m map[string]interface{}
			if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if m["id"] != modelID {
				t.Errorf("id = %v, want %s", m["id"], modelID)
			}
			if m["max_input_tokens"] != float64(1048576) {
				t.Errorf("max_input_tokens = %v, want 1048576", m["max_input_tokens"])
			}
			if m["max_tokens"] != float64(128000) {
				t.Errorf("max_tokens = %v, want 128000", m["max_tokens"])
			}
			caps, ok := m["capabilities"].(map[string]interface{})
			if !ok {
				t.Fatalf("capabilities missing or not map")
			}
			th, ok := caps["thinking"].(map[string]interface{})
			if !ok || th["supported"] != true {
				t.Errorf("thinking.supported = %v, want true", th)
			}
		})
	}
}

func TestBatches_AllSubpathsReturn400(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	router := NewRouter(cm)

	paths := []string{
		"/v1/messages/batches",
		"/v1/messages/batches/",
		"/v1/messages/batches/msgbatch_01",
		"/v1/messages/batches/msgbatch_01/results",
		"/v1/messages/batches/msgbatch_01/cancel",
	}

	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
				w := httptest.NewRecorder()
				r := httptest.NewRequest(method, p, nil)
				r.Header.Set("x-api-key", "sk-good")
				router.ServeHTTP(w, r)

				if w.Code != http.StatusBadRequest {
					t.Fatalf("%s %s status = %d, want %d (body: %s)", method, p, w.Code, http.StatusBadRequest, w.Body.String())
				}
				if !strings.Contains(w.Body.String(), "Message batches are not supported") {
					t.Errorf("body = %s, want mention of unsupported message batches", w.Body.String())
				}
				if w.Header().Get("anthropic-version") != "2023-06-01" {
					t.Errorf("anthropic-version header missing or incorrect")
				}
			}
		})
	}
}

func TestEmbeddings_Returns768DimensionVector(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	router := NewRouter(cm)

	t.Run("single string input", func(t *testing.T) {
		body := `{"model":"text-embedding-004","input":"semantic vector representation"}`
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(body))
		r.Header.Set("x-api-key", "sk-good")
		router.ServeHTTP(w, r)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d, body = %s", w.Code, http.StatusOK, w.Body.String())
		}
		var resp struct {
			Object string `json:"object"`
			Model  string `json:"model"`
			Data   []struct {
				Object    string    `json:"object"`
				Index     int       `json:"index"`
				Embedding []float64 `json:"embedding"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.Object != "list" {
			t.Errorf("object = %q, want list", resp.Object)
		}
		if len(resp.Data) != 1 {
			t.Fatalf("len(data) = %d, want 1", len(resp.Data))
		}
		if resp.Data[0].Object != "embedding" {
			t.Errorf("data[0].object = %q, want embedding", resp.Data[0].Object)
		}
		if resp.Data[0].Index != 0 {
			t.Errorf("data[0].index = %d, want 0", resp.Data[0].Index)
		}
		if len(resp.Data[0].Embedding) != 768 {
			t.Fatalf("len(embedding) = %d, want 768", len(resp.Data[0].Embedding))
		}
	})

	t.Run("array input", func(t *testing.T) {
		body := `{"model":"text-embedding-004","input":["first query","second query"]}`
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(body))
		r.Header.Set("x-api-key", "sk-good")
		router.ServeHTTP(w, r)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d, body = %s", w.Code, http.StatusOK, w.Body.String())
		}
		var resp struct {
			Object string `json:"object"`
			Data   []struct {
				Index     int       `json:"index"`
				Embedding []float64 `json:"embedding"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(resp.Data) != 2 {
			t.Fatalf("len(data) = %d, want 2", len(resp.Data))
		}
		for i, d := range resp.Data {
			if d.Index != i {
				t.Errorf("data[%d].index = %d, want %d", i, d.Index, i)
			}
			if len(d.Embedding) != 768 {
				t.Errorf("data[%d].embedding len = %d, want 768", i, len(d.Embedding))
			}
		}
	})
}
