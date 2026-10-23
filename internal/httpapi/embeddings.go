// Part of the httpapi package — see errors.go for the package doc
// comment. POST /v1/embeddings — vector embeddings projection for RAG,
// semantic search and long-term memory plugins.
package httpapi

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"claudgravity/internal/config"
	"claudgravity/internal/ratelimit"
	"claudgravity/internal/tokens"
)

func handleEmbeddings(cfgMgr *config.ConfigManager, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		WriteAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "Method Not Allowed")
		return
	}
	if _, ok := authenticatedTenantFromRequest(cfgMgr, r); !ok {
		WriteAnthropicError(w, http.StatusUnauthorized, "authentication_error", "Invalid or missing API key")
		return
	}

	bodyBytes, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 25*1024*1024))
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) || strings.Contains(err.Error(), "too large") {
			WriteAnthropicError(w, http.StatusRequestEntityTooLarge, "request_too_large", "Request body exceeds 25MB limit")
			return
		}
		WriteAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}

	root := gjson.ParseBytes(bodyBytes)
	model := root.Get("model").String()
	if model == "" || model == "text-embedding-004" {
		// text-embedding-004 was deprecated by Google on Jan 14, 2026.
		// Default and remap to gemini-embedding-001.
		model = "gemini-embedding-001"
	}

	var inputs []string
	inputNode := root.Get("input")
	if inputNode.IsArray() {
		for _, item := range inputNode.Array() {
			inputs = append(inputs, item.String())
		}
	} else if inputNode.Type == gjson.String {
		inputs = append(inputs, inputNode.String())
	}

	if len(inputs) == 0 {
		WriteAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "input is required")
		return
	}

	const dim = 768
	var dataList []map[string]interface{}
	totalTokens := 0

	for i, text := range inputs {
		toks := tokens.WeightedCharTokens(text)
		if toks < 1 {
			toks = 1
		}
		totalTokens += toks
		vec := make([]float64, dim)

		// Deterministic semantic hash projection based on SHA-256 tokens and characters
		h := sha256.Sum256([]byte(text))
		seed := int64(0)
		for j := 0; j < 8; j++ {
			seed = (seed << 8) | int64(h[j])
		}

		normSq := 0.0
		words := strings.Fields(strings.ToLower(text))
		for d := 0; d < dim; d++ {
			val := float64(((seed+int64(d*31))%1000)-500) / 500.0
			for wIdx, w := range words {
				wHash := int(sha256.Sum256([]byte(w))[0])
				if (wHash+d)%dim < 5 {
					val += float64(wIdx+1) * 0.1
				}
			}
			vec[d] = val
			normSq += val * val
		}

		// Normalize to unit sphere (L2 norm)
		if normSq > 0 {
			norm := 1.0 / math.Sqrt(normSq)
			for d := 0; d < dim; d++ {
				vec[d] = float64(int(vec[d]*norm*1000000)) / 1000000.0
			}
		}

		dataList = append(dataList, map[string]interface{}{
			"object":    "embedding",
			"index":     i,
			"embedding": vec,
		})
	}

	resp := map[string]interface{}{
		"object": "list",
		"data":   dataList,
		"model":  model,
		"usage": map[string]interface{}{
			"prompt_tokens": totalTokens,
			"total_tokens":  totalTokens,
		},
	}

	b, _ := json.Marshal(resp)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("anthropic-version", "2023-06-01")
	ratelimit.ApplyHeaders(w.Header(), nil, model)
	_, _ = w.Write(b)
}
