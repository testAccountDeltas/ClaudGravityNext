// Command livecheck runs a fixed set of end-to-end scenarios against a RUNNING
// gateway (local or VPS) with real Gemini behind it, and exits non-zero if the
// contract with Claude Code drifted: streaming shape, tool_use round trips,
// stop sequences, image input, context_management, error envelopes, etc.
//
//	go run ./cmd/livecheck -url http://localhost:8901 -key <tenant key>
//
// Model output is not deterministic, so each scenario is retried (-retries)
// before it counts as a failure; a scenario that only passed on a retry is
// reported as FLAKY. Checks that depend on upstream behaviour the gateway
// cannot control (implicit cache hits) return a warning, not a failure.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

type scenario struct {
	name string
	run  func(t *target) error
}

// warnError marks a soft failure: reported, but the exit code stays 0.
type warnError struct{ msg string }

func (w warnError) Error() string { return w.msg }

func allScenarios() []scenario {
	var all []scenario
	all = append(all, basicScenarios()...)
	all = append(all, toolScenarios()...)
	all = append(all, miscScenarios()...)
	return all
}

func main() {
	url := flag.String("url", "http://localhost:8901", "gateway base URL")
	key := flag.String("key", os.Getenv("LIVECHECK_KEY"), "tenant API key (or LIVECHECK_KEY)")
	model := flag.String("model", "gemini-3.8-flash", "model id to request")
	only := flag.String("only", "", "comma-separated substrings; run matching scenarios only")
	retries := flag.Int("retries", 2, "extra attempts per scenario before it counts as failed")
	timeout := flag.Duration("timeout", 3*time.Minute, "per-request HTTP timeout")
	list := flag.Bool("list", false, "list scenarios and exit")
	flag.Parse()

	all := allScenarios()
	if *list {
		for _, s := range all {
			fmt.Println(s.name)
		}
		return
	}
	if *key == "" {
		fmt.Fprintln(os.Stderr, "livecheck: -key (or LIVECHECK_KEY) is required")
		os.Exit(2)
	}
	t := &target{base: strings.TrimRight(*url, "/"), key: *key, model: *model, http: &http.Client{Timeout: *timeout}}

	var failed, warned, flaky, passed int
	start := time.Now()
	for _, s := range all {
		if !selected(s.name, *only) {
			continue
		}
		t0 := time.Now()
		var err error
		attempts := 0
		for attempts <= *retries {
			attempts++
			if err = s.run(t); err == nil {
				break
			}
			if _, soft := err.(warnError); soft {
				break
			}
		}
		dur := time.Since(t0).Round(100 * time.Millisecond)
		switch e := err.(type) {
		case nil:
			if attempts > 1 {
				flaky++
				fmt.Printf("FLAKY %-28s %6s  passed on attempt %d\n", s.name, dur, attempts)
			} else {
				passed++
				fmt.Printf("ok    %-28s %6s\n", s.name, dur)
			}
		case warnError:
			warned++
			fmt.Printf("WARN  %-28s %6s  %s\n", s.name, dur, e.msg)
		default:
			failed++
			fmt.Printf("FAIL  %-28s %6s  %v\n", s.name, dur, err)
		}
	}
	fmt.Printf("\n%d ok, %d flaky, %d warn, %d failed in %s against %s\n",
		passed, flaky, warned, failed, time.Since(start).Round(time.Second), t.base)
	if failed > 0 {
		os.Exit(1)
	}
}

func selected(name, only string) bool {
	if only == "" {
		return true
	}
	for _, p := range strings.Split(only, ",") {
		if p != "" && strings.Contains(name, p) {
			return true
		}
	}
	return false
}
