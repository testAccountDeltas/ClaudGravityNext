// Part of the tokens package — see text.go for the package doc comment.
// This file is the actual merge: ONE per-content-block pricing dispatcher
// used by both WeightedRequestTokens and ConservativeMessageTokens (via a
// textCost func parameter), replacing v1's two independently-maintained
// copies (token_estimator.go's inline switch inside
// estimateApproxClaudeTokens, and context_window.go's blockTokenEstimate).
//
// Deliberate behavior changes this merge makes (documented here, not
// hidden, per the migration spec's own instruction for this step):
//
//  1. A top-level "image" block in v1's token_estimator.go switch (used
//     for the "real" weighted estimate) priced EVERY image via
//     estimateImageTokens(data) with no url/svg special-casing — but the
//     SAME block type nested inside a tool_result (same file, few lines
//     up) and the conservative estimator's blockTokenEstimate (both
//     paths) DID special-case a url-sourced image (no "data" field at
//     all — price the "[Attached by URL: ...]" placeholder translator.go
//     actually sends) and an SVG image (real XML text, not a raster
//     estimateImageTokens can decode). This was plainly drift, not a
//     deliberate position-dependent choice — nothing comments on why a
//     top-level image should be priced differently from a nested one.
//     imageCost below applies the url/svg handling uniformly everywhere.
//
//  2. v1's two switches disagreed on an unrecognized block type
//     (server_tool_use, web_search_tool_result, or any future type):
//     token_estimator.go's switch has no default case at all, silently
//     contributing 0 tokens, while context_window.go's blockTokenEstimate
//     falls to `default: return flatTokenEstimate(bMap)`. Nothing
//     comments on the zero-case being intentional, and pricing a real
//     block at 0 risks under-reporting input_tokens and under-truncating
//     — the worse failure mode per context_window.go's own stated
//     philosophy. blockCost's default case now always applies the flat
//     estimate, matching context_window.go's (safer) choice.
package tokens

import (
	"encoding/json"
	"strings"

	"claudgravity/internal/protocol/claude"
)

// blockCost prices one content block. isLastAssistant mirrors v1's own
// gate: only the single most recent assistant turn's "thinking" block
// survives to actually reach Gemini (everything older is stripped before
// translation), so only that one message should price its thinking at
// real size — every other turn's thinking (and EVERY turn's
// redacted_thinking, which always gets replaced by a fixed placeholder,
// not just stripped from history) prices at 0 or the placeholder's size
// respectively. textCost selects which leaf formula a caller wants:
// WeightedCharTokens for the realistic estimate, FlatTextTokens for the
// pessimistic truncation-safety one.
// blockCost, imageCost, documentCost, estimateImageTokens and
// estimatePDFTokensFromPageCount price with the legacy Gemini 2.x profile
// (pinned by the characterization tests); production paths use the *P
// variants with an explicit Profile.
func blockCost(b claude.Block, isLastAssistant bool, textCost func(string) int) int {
	return blockCostP(b, isLastAssistant, textCost, Gemini2Profile)
}

// blockCostP is blockCost under an explicit model-family Profile (image
// and PDF page prices differ between Gemini 2.x and 3.x).
func blockCostP(b claude.Block, isLastAssistant bool, textCost func(string) int, prof Profile) int {
	switch b.Type {
	case "text":
		t, _ := b.AsText()
		return textCost(t.Text)

	case "thinking":
		if !isLastAssistant {
			return 0
		}
		t, _ := b.AsThinking()
		return textCost(t.Thinking)

	case "redacted_thinking":
		// translator.go always substitutes the fixed placeholder before
		// this ever reaches Gemini, on every turn, not just the latest —
		// price the placeholder's real size, always.
		return textCost("[Prior reasoning redacted]")

	case "tool_use":
		tu, _ := b.AsToolUse()
		return textCost(tu.Name) + textCost(string(tu.Input))

	case "server_tool_use":
		su, _ := b.AsServerToolUse()
		return textCost(su.Name) + textCost(string(su.Input))

	case "tool_result":
		tr, _ := b.AsToolResult()
		return toolResultCostP(tr.Content, isLastAssistant, textCost, prof)

	case "web_search_tool_result":
		ws, _ := b.AsWebSearchToolResult()
		total := 0
		for _, item := range ws.Content {
			total += textCost(item.Title) + textCost(item.URL) + textCost(item.Snippet)
		}
		return total

	case "image":
		img, _ := b.AsImage()
		return imageCostP(img.Source, textCost, prof)

	case "document":
		doc, _ := b.AsDocument()
		cost := documentCostP(doc.Source, textCost, prof)
		if doc.Title != "" {
			cost += textCost(doc.Title)
		}
		if doc.Context != "" {
			cost += textCost(doc.Context)
		}
		return cost

	default:
		// See this file's doc comment, point 2: an unrecognized block type
		// prices at the flat estimate of its own raw bytes, never 0.
		return FlatJSONTokens(b.Raw)
	}
}

func toolResultCostP(content json.RawMessage, isLastAssistant bool, textCost func(string) int, prof Profile) int {
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return textCost(s)
	}
	var items []claude.Block
	if err := json.Unmarshal(content, &items); err == nil && len(items) > 0 {
		total := 0
		for _, item := range items {
			total += blockCostP(item, isLastAssistant, textCost, prof)
		}
		return total
	}
	// A bare string array element (content as ["line 1\n", "line 2\n"],
	// not typed blocks) doesn't unmarshal as []claude.Block (Block expects
	// an object with a "type" discriminator) — fall back to []string.
	var strs []string
	if err := json.Unmarshal(content, &strs); err == nil {
		total := 0
		for _, s := range strs {
			total += textCost(s)
		}
		return total
	}
	if len(content) == 0 {
		return 0
	}
	// A tool_result's content given as a bare JSON object (neither string
	// nor array — e.g. a tool returning structured {"text": "...", ...}
	// directly): price its raw JSON the same conservative way a single
	// untyped content block is priced.
	return textCost(string(content))
}

// imageCost prices an image source, applying the url/svg special cases
// uniformly (see this file's doc comment, point 1). textCost prices the
// URL placeholder text — v1's two files genuinely disagree here (weighted
// for the "real" estimate, flat for the conservative one), unlike
// estimateImageTokens/the svg decoded-byte-ratio below, which use the same
// fixed formula in both.
func imageCost(src claude.ImageSource, textCost func(string) int) int {
	return imageCostP(src, textCost, Gemini2Profile)
}

func imageCostP(src claude.ImageSource, textCost func(string) int, prof Profile) int {
	if src.Type == "url" {
		if src.URL != "" {
			return textCost("[Attached by URL: " + src.URL + "]")
		}
		return textCost("[Attached by URL: ]")
	}
	mediaType := strings.ToLower(src.MediaType)
	if idx := strings.Index(mediaType, ";"); idx != -1 {
		mediaType = strings.TrimSpace(mediaType[:idx])
	}
	if isSVGMediaType(mediaType) {
		decodedBytes := len(src.Data) * 3 / 4
		if est := decodedBytes / 2; est > 0 {
			return est
		}
		return 1
	}
	return estimateImageTokensP(src.Data, prof)
}

// documentCost prices a document source (base64 PDF/image/text, a
// pre-extracted "content" array/string, or a "url" placeholder) — direct
// merge of v1's two near-identical document-pricing blocks. textCost
// prices the placeholder/extracted-text branches only; the PDF-density,
// raster-image, and base64-text-document ratios below are the same fixed
// formula in both of v1's estimators, independent of which textCost a
// caller passes.
func documentCost(src claude.DocumentSource, textCost func(string) int) int {
	return documentCostP(src, textCost, Gemini2Profile)
}

func documentCostP(src claude.DocumentSource, textCost func(string) int, prof Profile) int {
	switch src.Type {
	case "content":
		return textCost(documentContentText(src.Content))
	case "url":
		if src.URL != "" {
			return textCost("[Attached by URL: " + src.URL + "]")
		}
		return textCost("[Attached by URL: ]")
	case "base64":
		mediaType := strings.ToLower(src.MediaType)
		if idx := strings.Index(mediaType, ";"); idx != -1 {
			mediaType = strings.TrimSpace(mediaType[:idx])
		}
		var est int
		pageCountTrusted := false
		switch {
		case mediaType == "application/pdf" || mediaType == "":
			// PDF token density (~1 token per 500 base64 chars) — a flat
			// len/2 would price a 2MB scanned PDF at ~1.33M tokens.
			if pageEst, ok := estimatePDFTokensP(src.Data, prof); ok {
				pageCountTrusted = true
				est = pageEst
			} else {
				est = len(src.Data) / 500
			}
		case strings.HasPrefix(mediaType, "image/") && !isSVGMediaType(mediaType):
			est = estimateImageTokensP(src.Data, prof)
		default:
			// Base64-encoded text document (code, logs, CSV) or SVG — decode to
			// raw text and price at the normal char/token ratio.
			decodedBytes := len(src.Data) * 3 / 4
			est = decodedBytes / 2
			if est < 1 {
				est = 1
			}
		}
		if !pageCountTrusted && (mediaType == "application/pdf" || mediaType == "") && est < minPDFTokenFloor {
			est = minPDFTokenFloor
		}
		return est
	default:
		return textCost(src.Data)
	}
}

// documentContentText extracts the real text from a document's "content"
// field (Anthropic's other text-document form) — either a literal string,
// or an array of {"type":"text","text":...} blocks / bare strings.
func documentContentText(content json.RawMessage) string {
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s
	}
	var items []json.RawMessage
	if err := json.Unmarshal(content, &items); err != nil {
		return ""
	}
	var sb strings.Builder
	for _, item := range items {
		var str string
		if err := json.Unmarshal(item, &str); err == nil {
			sb.WriteString(str)
			continue
		}
		var blk struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(item, &blk); err == nil && blk.Type == "text" {
			sb.WriteString(blk.Text)
		}
	}
	return sb.String()
}
