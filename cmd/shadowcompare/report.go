package main

import (
	"fmt"
	"sort"
	"strings"
)

type report struct {
	matched      int
	diffRecords  int
	errored      int
	pathBuckets  map[string]int // top-2-segment JSON-pointer prefix -> count of mismatching paths
	exampleRID   map[string]string
	errorSamples []string
	driftRatios  []float64
}

func newReport() *report {
	return &report{pathBuckets: make(map[string]int), exampleRID: make(map[string]string)}
}

func (r *report) addMatch(key string) { r.matched++ }

func (r *report) addDiff(key string, paths []string) {
	r.diffRecords++
	for _, p := range paths {
		bucket := bucketOf(p)
		r.pathBuckets[bucket]++
		if _, ok := r.exampleRID[bucket]; !ok {
			r.exampleRID[bucket] = key
		}
	}
}

func (r *report) addError(key string, err error) {
	r.errored++
	if len(r.errorSamples) < 10 {
		r.errorSamples = append(r.errorSamples, fmt.Sprintf("%s: %v", key, err))
	}
}

func (r *report) addDriftSample(ratio float64) {
	r.driftRatios = append(r.driftRatios, ratio)
}

// bucketOf groups a JSON-pointer path down to its first two segments
// (e.g. "/tools/3/functionDeclarations/0/parameters/..." -> "/tools/*")
// so the report reads as "N diffs under /tools/*" instead of one
// unreadable line per individual array index.
func bucketOf(path string) string {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) == 0 {
		return "/"
	}
	seg := parts[0]
	if len(parts) == 1 {
		return "/" + seg
	}
	return "/" + seg + "/*"
}

func (r *report) Print(totalLines, eligible, sampled, samplePercent int) {
	fmt.Printf("shadowcompare: %d total captured lines, %d had both claude_request+gemini_request, %d sampled at %d%%\n",
		totalLines, eligible, sampled, samplePercent)
	fmt.Printf("  exact structural match (after redaction): %d\n", r.matched)
	fmt.Printf("  structural diffs found:                   %d\n", r.diffRecords)
	fmt.Printf("  translate/parse errors:                   %d\n", r.errored)

	if len(r.pathBuckets) > 0 {
		fmt.Println("\nDiff buckets (JSON-pointer prefix -> occurrence count, example rid):")
		buckets := make([]string, 0, len(r.pathBuckets))
		for b := range r.pathBuckets {
			buckets = append(buckets, b)
		}
		sort.Slice(buckets, func(i, j int) bool { return r.pathBuckets[buckets[i]] > r.pathBuckets[buckets[j]] })
		for _, b := range buckets {
			fmt.Printf("  %-45s %5d   (e.g. %s)\n", b, r.pathBuckets[b], r.exampleRID[b])
		}
	}

	if len(r.errorSamples) > 0 {
		fmt.Println("\nSample errors:")
		for _, e := range r.errorSamples {
			fmt.Println("  " + e)
		}
	}

	if len(r.driftRatios) > 0 {
		inBand := 0
		for _, ratio := range r.driftRatios {
			if ratio >= 1.00 && ratio <= 1.30 {
				inBand++
			}
		}
		pct := 100 * float64(inBand) / float64(len(r.driftRatios))
		fmt.Printf("\nToken drift (v2 WeightedRequestTokens estimate / Gemini's real promptTokenCount), band [1.00,1.30]:\n")
		fmt.Printf("  %d samples, %d in band (%.1f%%)\n", len(r.driftRatios), inBand, pct)
	} else {
		fmt.Println("\nToken drift: no sampled capture had a usable gemini_response.usageMetadata.promptTokenCount — skipped.")
	}
}
