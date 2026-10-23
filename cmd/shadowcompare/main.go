// shadowcompare is the Phase 5 "shadow observation" tool from
// ARCHITECTURE_V2_SPECIFICATION_AND_MIGRATION_PLAN.md: it compares v1's
// (superrelay's) real, already-computed Claude-to-Gemini translation
// against v2's (ClaudGravityNext's) own computation of the SAME input —
// both PURE, local functions. It never calls Gemini: the real Gemini
// request/response for each sample was already captured once, by v1, when
// that real request actually happened (speccapture.go's
// request_captures/<date>.ndjson). Duplicating that network call here to
// "verify" it would double real quota spend on free Google accounts, which
// the spec's own scope section explicitly rules out (see "shadow-
// дублирование" in Part 3 п.4) — this tool only re-runs the CPU-bound
// translation step offline, on the input half of a capture that already
// happened.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"

	"claudgravity/internal/config"
	"claudgravity/internal/protocol/claude"
	"claudgravity/internal/tokens"
	"claudgravity/internal/translate"
)

func main() {
	dir := flag.String("dir", `../superrelay/request_captures`, "directory of v1's request_captures/*.ndjson")
	corpusDir := flag.String("corpus-dir", "", "instead of -dir: Part 7.1's frozen 50-session characterization corpus (one *.json fixture per file, e.g. ../superrelay/testdata/characterization) — used for DoD criterion 2, not live traffic")
	samplePercent := flag.Int("sample", 10, "percentage of eligible captures to compare (deterministic, by rid)")
	verboseKey := flag.String("verbose-key", "", "print every individual diff path (with its missing/extra/length annotation, never raw values) for exactly this record's Key, instead of just the bucketed summary")
	flag.Parse()

	var records []captureRecord
	totalLines := 0

	if *corpusDir != "" {
		files, err := filepath.Glob(filepath.Join(*corpusDir, "*.json"))
		if err != nil || len(files) == 0 {
			fmt.Fprintf(os.Stderr, "shadowcompare: no *.json fixtures found in %s: %v\n", *corpusDir, err)
			os.Exit(1)
		}
		sort.Strings(files)
		for _, f := range files {
			if filepath.Base(f) == "index.json" {
				continue
			}
			rec, ok := readCharacterizationFixture(f)
			totalLines++
			if ok {
				records = append(records, rec)
			}
		}
	} else {
		files, err := filepath.Glob(filepath.Join(*dir, "*.ndjson"))
		if err != nil || len(files) == 0 {
			fmt.Fprintf(os.Stderr, "shadowcompare: no *.ndjson files found in %s: %v\n", *dir, err)
			os.Exit(1)
		}
		sort.Strings(files)
		for _, f := range files {
			recs, n := readCaptureFile(f)
			records = append(records, recs...)
			totalLines += n
		}
	}

	eligible := make([]captureRecord, 0, len(records))
	for _, r := range records {
		if r.ClaudeRequestBody != nil && r.GeminiRequestBody != nil {
			eligible = append(eligible, r)
		}
	}

	sampled := make([]captureRecord, 0)
	for _, r := range eligible {
		if sampleBucket(r.Key, 100) < *samplePercent {
			sampled = append(sampled, r)
		}
	}

	rep := newReport()
	for _, r := range sampled {
		compareOne(r, rep, *verboseKey)
	}

	rep.Print(totalLines, len(eligible), len(sampled), *samplePercent)
}

// sampleBucket deterministically hashes key into [0,mod) — the same
// record always lands in the same bucket across runs (unlike math/rand),
// so "10% sample" is reproducible and comparable run to run, per the
// spec's own wording.
func sampleBucket(key string, mod int) int {
	h := fnv.New64a()
	fmt.Fprint(h, key)
	return int(h.Sum64() % uint64(mod))
}

type captureRecord struct {
	// Key uniquely identifies this record for sampling/reporting — "rid"
	// ALONE is not unique across files: it's a simple in-process counter
	// (server.go's nextReqID) that resets on every gateway restart, so the
	// same bare rid value appears many times across different capture
	// files (and even within one file, across restarts on the same day).
	// Key is "<filename>#<rid>#<line-number-in-file>", which is always
	// unique and still stable across re-runs of this tool.
	Key                string
	RID                uint64
	SessionID          string
	AgentKey           string
	ClaudeRequestBody  []byte // raw JSON, as captured (already redacted by v1's speccapture.go)
	GeminiRequestBody  json.RawMessage
	GeminiPromptTokens float64 // from gemini_response.body.usageMetadata.promptTokenCount, if present
}

// readCaptureFile parses one request_captures/<date>.ndjson file. Lines
// that don't even parse as JSON, or that are missing a body, are skipped —
// this is best-effort over real production data, not a strict schema.
func readCaptureFile(path string) ([]captureRecord, int) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0
	}
	defer f.Close()

	var out []captureRecord
	n := 0
	lineNo := 0
	base := filepath.Base(path)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		lineNo++
		if len(line) == 0 {
			continue
		}
		n++
		var raw struct {
			RID       uint64 `json:"rid"`
			SessionID string `json:"session_id"`
			AgentKey  string `json:"agent_key"`

			ClaudeRequest *struct {
				Body json.RawMessage `json:"body"`
			} `json:"claude_request"`
			GeminiRequest *struct {
				Body json.RawMessage `json:"body"`
			} `json:"gemini_request"`
			GeminiResponse *struct {
				Body json.RawMessage `json:"body"`
			} `json:"gemini_response"`
		}
		if err := json.Unmarshal(line, &raw); err != nil {
			continue
		}
		rec := captureRecord{
			Key:       fmt.Sprintf("%s#%d#%d", base, raw.RID, lineNo),
			RID:       raw.RID,
			SessionID: raw.SessionID,
			AgentKey:  raw.AgentKey,
		}
		if raw.ClaudeRequest != nil && len(raw.ClaudeRequest.Body) > 0 && !isUnparsedFallback(raw.ClaudeRequest.Body) {
			rec.ClaudeRequestBody = raw.ClaudeRequest.Body
		}
		if raw.GeminiRequest != nil && len(raw.GeminiRequest.Body) > 0 && !isUnparsedFallback(raw.GeminiRequest.Body) {
			rec.GeminiRequestBody = raw.GeminiRequest.Body
		}
		if raw.GeminiResponse != nil && len(raw.GeminiResponse.Body) > 0 {
			var gr struct {
				UsageMetadata struct {
					PromptTokenCount float64 `json:"promptTokenCount"`
				} `json:"usageMetadata"`
			}
			if json.Unmarshal(raw.GeminiResponse.Body, &gr) == nil {
				rec.GeminiPromptTokens = gr.UsageMetadata.PromptTokenCount
			}
		}
		// Streaming captures don't have a flat gemini_response.body —
		// speccapture.go instead records gemini_response.chunks[], one per
		// SSE chunk from Gemini. usageMetadata only appears (and is only
		// cumulative/final) on the LAST chunk's response, so that's the one
		// to read promptTokenCount from. Found by direct inspection of a
		// real 2026-10-19 capture: rec.GeminiPromptTokens was silently
		// staying 0 for every streaming record even though usageMetadata
		// was genuinely present on disk, because this reader only knew the
		// non-streaming "flat body" shape.
		if rec.GeminiPromptTokens == 0 {
			var top struct {
				GeminiResponse *struct {
					Chunks []struct {
						Response struct {
							UsageMetadata struct {
								PromptTokenCount float64 `json:"promptTokenCount"`
							} `json:"usageMetadata"`
						} `json:"response"`
					} `json:"chunks"`
				} `json:"gemini_response"`
			}
			if json.Unmarshal(line, &top) == nil && top.GeminiResponse != nil && len(top.GeminiResponse.Chunks) > 0 {
				last := top.GeminiResponse.Chunks[len(top.GeminiResponse.Chunks)-1]
				rec.GeminiPromptTokens = last.Response.UsageMetadata.PromptTokenCount
			}
		}
		out = append(out, rec)
	}
	return out, n
}

// readCharacterizationFixture parses one file of the Part 7.1 frozen
// 50-session corpus (testdata/characterization/NNN_ridX.json) — same field
// names as one ndjson line (claude_request.body/gemini_request.body/
// gemini_response.body), but one whole JSON object per file instead of one
// line per record. This is the literal corpus DoD criterion 2 names, not
// request_captures/*.ndjson live traffic.
func readCharacterizationFixture(path string) (captureRecord, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return captureRecord{}, false
	}
	var raw struct {
		// rid in this corpus format is a bare JSON number (unlike index.json's
		// string "rid", and unlike request_captures/*.ndjson's own numeric
		// rid) — not read here at all since Key is the filename, not rid;
		// deliberately NOT a typed field, so its number-vs-string shape can
		// never break decoding of the two fields actually needed.
		ClaudeRequest *struct {
			Body json.RawMessage `json:"body"`
		} `json:"claude_request"`
		GeminiRequest *struct {
			Body json.RawMessage `json:"body"`
		} `json:"gemini_request"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return captureRecord{}, false
	}
	rec := captureRecord{Key: filepath.Base(path)}
	if raw.ClaudeRequest != nil && len(raw.ClaudeRequest.Body) > 0 && !isUnparsedFallback(raw.ClaudeRequest.Body) {
		rec.ClaudeRequestBody = raw.ClaudeRequest.Body
	}
	if raw.GeminiRequest != nil && len(raw.GeminiRequest.Body) > 0 && !isUnparsedFallback(raw.GeminiRequest.Body) {
		rec.GeminiRequestBody = raw.GeminiRequest.Body
	}
	return rec, true
}

// isUnparsedFallback reports whether a captured body is speccapture.go's
// own "{"_unparsed_len":N}" stand-in for a body that failed to parse as
// JSON at capture time — not real translatable content, and definitely
// not something ConvertClaudeRequestToGemini should ever be fed.
func isUnparsedFallback(body []byte) bool {
	var m map[string]interface{}
	if json.Unmarshal(body, &m) != nil {
		return false
	}
	_, ok := m["_unparsed_len"]
	return ok && len(m) == 1
}

// compareOne translates r's captured Claude request through v2's real
// pipeline and diffs the result against what v1 actually produced for the
// SAME input, which is already sitting in the capture.
func compareOne(r captureRecord, rep *report, verboseKey string) {
	agentKey := r.AgentKey
	if agentKey == "" {
		agentKey = "main"
	}
	tenant := config.TenantConfig{
		DefaultModel:     "gemini-3.8-flash-tiered",
		ThinkingBudget:   -1,
		StripSearchTools: true,
	}

	result, err := translate.ConvertClaudeRequestToGemini(r.ClaudeRequestBody, tenant, "", agentKey, r.SessionID, "", true)
	if err != nil {
		rep.addError(r.Key, err)
		return
	}

	var v2Body interface{}
	if err := json.Unmarshal(result.GeminiBody, &v2Body); err != nil {
		rep.addError(r.Key, fmt.Errorf("v2 output didn't parse as JSON: %w", err))
		return
	}
	var v1Body interface{}
	if err := json.Unmarshal(r.GeminiRequestBody, &v1Body); err != nil {
		rep.addError(r.Key, fmt.Errorf("captured v1 gemini_request.body didn't parse as JSON: %w", err))
		return
	}

	v2Redacted := redactSkeleton(v2Body)
	v1Redacted := redactSkeleton(v1Body)

	var paths []string
	diffJSON("", v1Redacted, v2Redacted, &paths)
	if len(paths) == 0 {
		rep.addMatch(r.Key)
	} else {
		rep.addDiff(r.Key, paths)
	}
	if verboseKey != "" && r.Key == verboseKey {
		fmt.Printf("\n--- verbose diff for %s (%d paths) ---\n", r.Key, len(paths))
		for _, p := range paths {
			fmt.Println("  " + p)
		}
	}

	// Best-effort token-drift signal: v2's own weighted estimate of the
	// SAME captured Claude request, against Gemini's real, already-billed
	// promptTokenCount for that same real historical call (ground truth —
	// not v1's estimate, since v1 never logs its own estimate into the
	// capture; Google's real number is a strictly better baseline anyway).
	if r.GeminiPromptTokens > 0 {
		var req claude.Request
		if err := json.Unmarshal(r.ClaudeRequestBody, &req); err == nil {
			est := tokens.WeightedRequestTokens(req)
			ratio := float64(est) / r.GeminiPromptTokens
			rep.addDriftSample(ratio)
		}
	}
}
