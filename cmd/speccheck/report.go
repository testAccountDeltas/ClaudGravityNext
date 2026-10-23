// report.go formats speccheck's two output modes — the violation report
// (printReport) and the per-request chronological walkthrough
// (runShowMode/showRecord, ported from superrelay (v1)'s
// cmd/speccheck/show.go) — kept separate from main.go per Part 4.3's own
// description of this file's purpose: "формат вывода меняется чаще, чем
// CLI-парсинг."
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"claudgravity/pkg/speccheck/rules"
)

func printReport(numRecords, numFiles, numRules int, violations []rules.Violation) {
	fmt.Printf("speccheck: %d record(s) across %d file(s), %d rule(s) run\n", numRecords, numFiles, numRules)
	if len(violations) == 0 {
		fmt.Println("no violations found")
		return
	}

	byRule := make(map[string][]rules.Violation)
	for _, v := range violations {
		byRule[v.Rule] = append(byRule[v.Rule], v)
	}
	ruleNames := make([]string, 0, len(byRule))
	for name := range byRule {
		ruleNames = append(ruleNames, name)
	}
	sort.Strings(ruleNames)

	for _, name := range ruleNames {
		vs := byRule[name]
		fmt.Printf("\n=== %s (%d violation(s)) ===\n", name, len(vs))
		for _, v := range vs {
			fmt.Printf("  [%s rid=%s %s] %s\n", v.Source, v.RID, v.Timestamp, v.Detail)
		}
	}
	fmt.Printf("\n%d total violation(s)\n", len(violations))
}

func runShowMode(records []loadedRecord, ridFlag, sessionFlag string) {
	matched := 0
	for _, lr := range records {
		if ridFlag != "" && recordRID(lr.rec) != ridFlag {
			continue
		}
		if sessionFlag != "" && recordSessionID(lr.rec) != sessionFlag {
			continue
		}
		showRecord(lr.rec)
		matched++
	}
	if matched == 0 {
		if ridFlag != "" {
			fmt.Fprintf(os.Stderr, "no captured request with rid=%s found\n", ridFlag)
		} else {
			fmt.Fprintf(os.Stderr, "no captured request with session=%s found\n", sessionFlag)
		}
		os.Exit(1)
	}
	fmt.Printf("%d request(s) shown\n", matched)
}

// showRecord prints one captured request as a readable, chronological
// walkthrough of all four legs. Bodies are pretty-printed; streaming
// legs (chunks/events) are collapsed into a compact sequence summary.
func showRecord(rec rules.CaptureRecord) {
	m := map[string]interface{}(rec)
	fmt.Println(strings.Repeat("=", 78))
	header := fmt.Sprintf("rid=%s  %s", recordRID(rec), recordTimestamp(rec))
	if sid, ok := m["session_id"].(string); ok && sid != "" {
		header += "  session=" + sid
	}
	if ak, ok := m["agent_key"].(string); ok && ak != "" {
		header += "  agent=" + ak
	}
	fmt.Println(header)
	fmt.Println(strings.Repeat("=", 78))

	showLeg(m, "claude_request", "1. Claude Code -> Gateway (incoming request)")
	showLeg(m, "gemini_request", "2. Gateway -> Gemini (translated request)")
	showResponseLeg(m, "gemini_response", "3. Gemini -> Gateway (upstream response)")
	showResponseLeg(m, "claude_response", "4. Gateway -> Claude Code (final response)")
	fmt.Println()
}

func showLeg(m map[string]interface{}, key, title string) {
	leg, ok := m[key].(map[string]interface{})
	if !ok {
		fmt.Printf("\n--- %s: not captured ---\n", title)
		return
	}
	fmt.Printf("\n--- %s ---\n", title)
	if headers, ok := leg["headers"].(map[string]interface{}); ok && len(headers) > 0 {
		fmt.Println("headers:")
		printIndentedJSON(headers, "  ")
	}
	if body, ok := leg["body"]; ok {
		fmt.Println("body:")
		printIndentedJSON(body, "  ")
	}
}

// showResponseLeg handles the two legs that can ALSO be a streamed
// accumulation ({"chunks":[...]} / {"events":[...]}) instead of one body.
func showResponseLeg(m map[string]interface{}, key, title string) {
	legRaw, ok := m[key]
	if !ok {
		fmt.Printf("\n--- %s: not captured ---\n", title)
		return
	}
	leg, ok := legRaw.(map[string]interface{})
	if !ok {
		fmt.Printf("\n--- %s: not captured ---\n", title)
		return
	}
	fmt.Printf("\n--- %s ---\n", title)
	if headers, ok := leg["headers"].(map[string]interface{}); ok && len(headers) > 0 {
		fmt.Println("headers:")
		printIndentedJSON(headers, "  ")
	}
	if body, ok := leg["body"]; ok {
		fmt.Println("body:")
		printIndentedJSON(body, "  ")
		return
	}
	if chunks, ok := leg["chunks"].([]interface{}); ok {
		fmt.Printf("streamed: %d Gemini chunk(s)\n", len(chunks))
		for i, c := range chunks {
			fmt.Printf("  chunk[%d]:\n", i)
			printIndentedJSON(c, "    ")
		}
		return
	}
	if events, ok := leg["events"].([]interface{}); ok {
		fmt.Printf("streamed: %d SSE event(s):\n", len(events))
		printEventSequence(events)
	}
}

// printEventSequence collapses consecutive same-named events into one
// "x N" line, and shows every other event's structural fields in full.
func printEventSequence(events []interface{}) {
	asMap := func(v interface{}) map[string]interface{} {
		mm, _ := v.(map[string]interface{})
		return mm
	}
	asString := func(v interface{}) string {
		s, _ := v.(string)
		return s
	}
	i := 0
	for i < len(events) {
		ev := asMap(events[i])
		name := asString(ev["event"])
		j := i
		for j < len(events) && asString(asMap(events[j])["event"]) == name {
			j++
		}
		count := j - i
		if count > 1 {
			fmt.Printf("  %s  x%d\n", name, count)
		} else {
			fmt.Printf("  %s\n", name)
			if data, ok := ev["data"]; ok {
				printIndentedJSON(data, "    ")
			}
		}
		i = j
	}
}

func printIndentedJSON(v interface{}, indent string) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Printf("%s<unprintable: %v>\n", indent, err)
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		fmt.Println(indent + line)
	}
}
