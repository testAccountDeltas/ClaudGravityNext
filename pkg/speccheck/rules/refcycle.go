// Rule 11 — SchemaRefCycleTermination. NEW rule, built from Part 7.6
// #11's own description: "для каждой захваченной схемы инструмента с
// $ref обработка CleanJSONSchemaForGemini обязана завершиться за
// конечное число шагов (таймаут в тесте) — защита от регрессии именно в
// pathVisited-алгоритме." internal/schema/refs.go's convertRefsToHints
// guards against runaway $ref expansion with a per-branch recursion
// check plus a hard expansionCount cap of 10 (see that file) — this rule
// is the independent, traffic-driven proof that guard still works,
// rather than trusting the cap's own existence as self-evidently
// sufficient.
package rules

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"claudgravity/internal/schema"
)

// schemaCleanTimeout is deliberately generous (2s) for a single tool
// schema — any real captured schema from Claude Code's own tool set
// cleans in low single-digit milliseconds; a genuine pathVisited
// regression means an infinite or exponential-blowup loop, which blows
// well past this either way.
const schemaCleanTimeout = 2 * time.Second

func checkSchemaRefCycleTermination(rec CaptureRecord) []Violation {
	body := asMap(rec.snapshotBody("claude_request"))
	if body == nil {
		return nil
	}
	var out []Violation
	for _, t := range asArray(body["tools"]) {
		tool := asMap(t)
		schemaRaw, ok := tool["input_schema"]
		if !ok || schemaRaw == nil {
			continue
		}
		schemaBytes, err := json.Marshal(schemaRaw)
		if err != nil {
			continue
		}
		schemaStr := string(schemaBytes)
		if !strings.Contains(schemaStr, `"$ref"`) {
			continue // this rule only exercises the $ref-cycle path
		}
		name := asString(tool["name"])

		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = schema.CleanJSONSchemaForGemini(schemaStr)
		}()
		select {
		case <-done:
			// Terminated within budget — nothing to report.
		case <-time.After(schemaCleanTimeout):
			out = append(out, violation(rec, fmt.Sprintf(
				"tool %q schema with $ref did not finish CleanJSONSchemaForGemini within %v — possible $ref cycle-detection regression in internal/schema/refs.go's pathVisited algorithm",
				name, schemaCleanTimeout)))
		}
	}
	return out
}

var SchemaRefCycleTermination = Rule{
	Name: "SchemaRefCycleTermination",
	Check: func(records []CaptureRecord) []Violation {
		var out []Violation
		for _, rec := range records {
			out = append(out, checkSchemaRefCycleTermination(rec)...)
		}
		return out
	},
}
