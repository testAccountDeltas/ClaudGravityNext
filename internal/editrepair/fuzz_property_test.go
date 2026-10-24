package editrepair

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

type propResult struct{ ok, nomatch, corrupt int }

// runProperty drives RepairEditToolCall with randomly generated (file, block,
// distorted quote, intended edit) cases and checks the one thing that matters:
// when the repaired call still applies to the file, it must produce exactly the
// intended file. "nomatch" (Edit would refuse and the model retries) is
// tolerable; "corrupt" (a wrong edit lands silently) is not.
func runProperty(t *testing.T, seed int64, cases int, unit string, d distortion) (propResult, []string) {
	t.Helper()
	r := rand.New(rand.NewSource(seed))
	dir := t.TempDir()
	path := filepath.Join(dir, "f.src")
	sid := "prop-" + d.name + "-" + strconv.Quote(unit) + "-" + strconv.FormatInt(seed, 10)
	RecordSessionCWD(sid, dir)

	var res propResult
	var bad []string
	for i := 0; i < cases; i++ {
		lines := genLines(r, 12+r.Intn(30))
		a := r.Intn(len(lines))
		b := a + 1 + r.Intn(7)
		if b > len(lines) {
			b = len(lines)
		}
		block := lines[a:b]
		// Keep cases unambiguous: block must hold at least one unique line and
		// start and end on real lines (a quote never begins/ends on blank ones).
		if block[0].text == "" || block[len(block)-1].text == "" || !hasUnique(block) {
			continue
		}
		edited := editBlock(r, block)
		if len(edited) == 0 || edited[0].text == "" || edited[len(edited)-1].text == "" {
			continue
		}
		if d.skipLine0Edit && edited[0] != block[0] {
			continue
		}
		file := joinLines(lines, unit) + "\n"
		if d.needsStyle && indentStyleOf(file) == "" {
			continue
		}
		oldTrue, newTrue := joinLines(block, unit)+d.suffix, joinLines(edited, unit)+d.suffix
		if d.crlfFile {
			file = strings.ReplaceAll(file, "\n", "\r\n")
			oldTrue = strings.ReplaceAll(oldTrue, "\n", "\r\n")
			newTrue = strings.ReplaceAll(newTrue, "\n", "\r\n")
		}
		want := strings.Replace(file, oldTrue, newTrue, 1)

		if err := os.WriteFile(path, []byte(file), 0o644); err != nil {
			t.Fatal(err)
		}
		oldQ, newQ := distort(block, unit, d)+d.suffix, distort(edited, unit, d)+d.suffix
		args, _ := json.Marshal(map[string]any{"file_path": path, "old_string": oldQ, "new_string": newQ})
		out, _ := RepairEditToolCall("Edit", string(args), sid)
		gotOld, gotNew := gjson.Get(out, "old_string").String(), gjson.Get(out, "new_string").String()

		switch {
		case !strings.Contains(file, gotOld):
			res.nomatch++
		case strings.Replace(file, gotOld, gotNew, 1) == want:
			res.ok++
		default:
			res.corrupt++
			if len(bad) < 3 {
				bad = append(bad, fmt.Sprintf("seed=%d case=%d\nFILE:\n%s\nMODEL old:\n%q\nMODEL new:\n%q\nGOT old:\n%q\nGOT new:\n%q\nWANT new:\n%q",
					seed, i, file, oldQ, newQ, gotOld, gotNew, newTrue))
			}
		}
	}
	return res, bad
}

func hasUnique(block []fuzzLine) bool {
	for _, l := range block {
		if strings.HasPrefix(l.text, "stmt") || strings.HasPrefix(l.text, "if c") {
			return true
		}
	}
	return false
}

// TestPropertyRepairNeverCorrupts reports, per distortion, how many random
// edits were repaired correctly, refused, or silently corrupted. Every
// distortion not marked informational must be corruption-free.
func TestPropertyRepairNeverCorrupts(t *testing.T) {
	cases, seeds := 250, []int64{20261025, 7, 424242}
	if testing.Short() {
		cases, seeds = 60, seeds[:1]
	}
	var summary []string
	for _, unit := range []string{"\t", "    "} {
		for _, d := range distortions(unit) {
			if d.onlyUnit != "" && d.onlyUnit != unit {
				continue
			}
			res, bad := runProperty(t, 20261025, cases, unit, d)
			summary = append(summary, fmt.Sprintf("unit=%-6q %-28s ok=%-4d refused=%-4d CORRUPT=%d", unit, d.name, res.ok, res.nomatch, res.corrupt))
			if res.corrupt > 0 && !d.informational {
				t.Errorf("unit=%q distortion=%s: %d corrupting repairs; first:\n%s", unit, d.name, res.corrupt, strings.Join(bad[:1], "\n"))
			}
		}
	}
	sort.Strings(summary)
	t.Log("\n" + strings.Join(summary, "\n"))
}
