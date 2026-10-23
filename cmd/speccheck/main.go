// speccheck statically checks real gateway traffic (captured by
// internal/speccapture into request_captures/*.ndjson) against the 12
// specification invariants in pkg/speccheck/rules — ported from
// superrelay (v1)'s cmd/speccheck (rules 1-7) plus 5 new rules Part 7.6
// of ARCHITECTURE_V2_SPECIFICATION_AND_MIGRATION_PLAN.md calls for (rules
// 8-12) — see that package's registry.go for the full list. It reads the
// NDJSON format generically (no import of the gateway's own composition
// root) and never needs network access or live credentials.
//
// Two modes:
//   - Default (no -rid/-session): runs every rule against the whole
//     loaded corpus and reports violations.
//   - -rid/-session: prints a human-readable, chronological walkthrough
//     of the matching request(s) instead.
//
// Usage:
//
//	go run ./cmd/speccheck -dir request_captures
//	go run ./cmd/speccheck -dir request_captures -config config.json
//	go run ./cmd/speccheck -dir request_captures -rid 12345
//	go run ./cmd/speccheck -dir request_captures -session 11f8bfa9-5a04-4083-b4a4-c544f2816920
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"claudgravity/pkg/speccheck/rules"
)

// loadedRecord pairs a decoded record with the file it came from, so
// both modes below can report which .ndjson a finding/request lives in.
type loadedRecord struct {
	rec    rules.CaptureRecord
	source string
}

// loadCorpusDir reads Part 7.1's frozen 50-session characterization
// corpus (one whole JSON object per file, e.g.
// ../superrelay/testdata/characterization\NNN_ridX.json — the same
// format cmd/shadowcompare's own -corpus-dir reads) instead of
// request_captures/*.ndjson's one-line-per-record format. Rules that
// only read snapshotBody("claude_request"/"gemini_request"/...) work
// unchanged either way; "index.json" (the corpus's own manifest, not a
// capture record) is skipped.
func loadCorpusDir(dir string) ([]loadedRecord, []string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		return nil, files, fmt.Errorf("no *.json fixtures found in %s", dir)
	}
	sort.Strings(files)

	var out []loadedRecord
	for _, path := range files {
		if filepath.Base(path) == "index.json" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skipping %s: %v\n", path, err)
			continue
		}
		var rec rules.CaptureRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			fmt.Fprintf(os.Stderr, "%s: malformed fixture skipped: %v\n", path, err)
			continue
		}
		out = append(out, loadedRecord{rec: rec, source: filepath.Base(path)})
	}
	return out, files, nil
}

func loadRecords(dir string, sinceT, untilT time.Time) ([]loadedRecord, []string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.ndjson"))
	if err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		return nil, files, fmt.Errorf("no *.ndjson files found in %s", dir)
	}
	sort.Strings(files)

	var out []loadedRecord
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skipping %s: %v\n", path, err)
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 1024*1024), 64*1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			var rec rules.CaptureRecord
			if err := json.Unmarshal(line, &rec); err != nil {
				fmt.Fprintf(os.Stderr, "%s: malformed line skipped: %v\n", path, err)
				continue
			}
			ts := recordTimestamp(rec)
			if !sinceT.IsZero() || !untilT.IsZero() {
				parsed, err := time.Parse(time.RFC3339Nano, ts)
				if err == nil {
					if !sinceT.IsZero() && parsed.Before(sinceT) {
						continue
					}
					if !untilT.IsZero() && !parsed.Before(untilT) {
						continue
					}
				}
			}
			out = append(out, loadedRecord{rec: rec, source: filepath.Base(path)})
		}
		_ = f.Close()
	}
	return out, files, nil
}

func recordTimestamp(rec rules.CaptureRecord) string {
	if v, ok := rec["timestamp"].(string); ok {
		return v
	}
	return ""
}

func recordRID(rec rules.CaptureRecord) string {
	if v, ok := rec["rid"]; ok {
		return fmt.Sprint(v)
	}
	return "?"
}

func recordSessionID(rec rules.CaptureRecord) string {
	s, _ := rec["session_id"].(string)
	return s
}

func main() {
	dir := flag.String("dir", "request_captures", "directory of *.ndjson capture files")
	corpusDir := flag.String("corpus-dir", "", "instead of -dir: Part 7.1's frozen 50-session characterization corpus (one *.json fixture per file) — same format cmd/shadowcompare's own -corpus-dir reads")
	configPath := flag.String("config", "config.json", "path to config.json, used by the ConnectAllowlistSelfConsistency rule")
	since := flag.String("since", "", "RFC3339 timestamp; skip records before this")
	until := flag.String("until", "", "RFC3339 timestamp; skip records at/after this")
	rulesFlag := flag.String("rules", "all", "comma-separated rule names to run, or \"all\"")
	listRules := flag.Bool("list-rules", false, "print the available rule names and exit")
	ridFlag := flag.String("rid", "", "show the full lifecycle of one request by rid, instead of running rules")
	sessionFlag := flag.String("session", "", "show the full lifecycle of every request in one session, instead of running rules")
	flag.Parse()

	rules.ConfigPath = *configPath

	if *listRules {
		for _, r := range rules.AllRules {
			fmt.Println(r.Name)
		}
		return
	}

	var sinceT, untilT time.Time
	var err error
	if *since != "" {
		sinceT, err = time.Parse(time.RFC3339, *since)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bad -since: %v\n", err)
			os.Exit(2)
		}
	}
	if *until != "" {
		untilT, err = time.Parse(time.RFC3339, *until)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bad -until: %v\n", err)
			os.Exit(2)
		}
	}

	// Config/code invariants (rules 8/9) never need a loaded corpus at
	// all — load it best-effort so -rules=ConnectAllowlistSelfConsistency
	// (etc.) still works in a freshly-initialized checkout with no
	// request_captures/ directory yet.
	var records []loadedRecord
	var files []string
	var loadErr error
	if *corpusDir != "" {
		records, files, loadErr = loadCorpusDir(*corpusDir)
	} else {
		records, files, loadErr = loadRecords(*dir, sinceT, untilT)
	}
	if loadErr != nil && *ridFlag == "" && *sessionFlag == "" {
		fmt.Fprintln(os.Stderr, loadErr)
	}

	if *ridFlag != "" || *sessionFlag != "" {
		if loadErr != nil {
			fmt.Fprintln(os.Stderr, loadErr)
			os.Exit(1)
		}
		runShowMode(records, *ridFlag, *sessionFlag)
		return
	}

	selected := rules.AllRules
	if *rulesFlag != "all" && *rulesFlag != "" {
		want := make(map[string]bool)
		for _, n := range strings.Split(*rulesFlag, ",") {
			want[strings.TrimSpace(n)] = true
		}
		selected = nil
		for _, r := range rules.AllRules {
			if want[r.Name] {
				selected = append(selected, r)
			}
		}
	}

	recs := make([]rules.CaptureRecord, len(records))
	for i, lr := range records {
		recs[i] = lr.rec
	}
	sourceOf := make(map[string]string, len(records)) // rid -> source file, best-effort for reporting
	for _, lr := range records {
		sourceOf[recordRID(lr.rec)] = lr.source
	}

	var allViolations []rules.Violation
	for _, rule := range selected {
		for _, v := range rule.Check(recs) {
			v.Rule = rule.Name
			if v.Source == "" {
				v.Source = sourceOf[v.RID]
			}
			allViolations = append(allViolations, v)
		}
	}

	printReport(len(records), len(files), len(selected), allViolations)
	if len(allViolations) > 0 {
		os.Exit(1)
	}
}
