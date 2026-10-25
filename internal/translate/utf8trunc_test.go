package translate

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

func TestCutUTF8Boundaries(t *testing.T) {
	s := strings.Repeat("я", 300) // 2 bytes per rune
	for _, max := range []int{0, 1, 2, 199, 200, 201, 500, 599, 600, 601} {
		got := cutUTF8(s, max)
		if !utf8.ValidString(got) || len(got) > max {
			t.Errorf("max %d: valid=%v len=%d", max, utf8.ValidString(got), len(got))
		}
		if max >= 600 && got != s {
			t.Errorf("max %d: short string must be returned whole", max)
		}
	}
	// 3- and 4-byte runes
	for _, r := range []string{"字", "😀"} {
		s := strings.Repeat(r, 400)
		for max := 190; max < 210; max++ {
			if got := cutUTF8(s, max); !utf8.ValidString(got) {
				t.Fatalf("%q max %d invalid", r, max)
			}
		}
	}
}

func TestToolResultPreviewsAreValidUTF8(t *testing.T) {
	tn := toolNameInfo{effectiveToolNames: map[string]bool{}, disambiguatedToolName: map[string]string{}}
	check := func(label string, parts []string) {
		t.Helper()
		got := gjson.Get(parts[0], "text").String()
		if !strings.Contains(got, "omitted") {
			t.Fatalf("%s: unexpected part %s", label, parts[0])
		}
		if !utf8.ValidString(got) || strings.ContainsRune(got, utf8.RuneError) {
			t.Errorf("%s: broken UTF-8 in %q", label, got)
		}
	}
	// 499 ASCII bytes then 2-byte runes: the 500-byte cut lands mid-rune.
	long := strings.Repeat("a", 499) + strings.Repeat("я", 50)
	block := gjson.Parse(`{"type":"tool_result","tool_use_id":"nope","content":` + jsonString(long) + `}`)
	check("orphan", convertToolResultBlock(block, "user", map[string]string{}, tn, gjson.Result{}))
	// 199 ASCII bytes then 3-byte runes for the 200-byte preview of an undeclared tool.
	long = strings.Repeat("a", 199) + strings.Repeat("字", 50)
	block = gjson.Parse(`{"type":"tool_result","tool_use_id":"u1","content":` + jsonString(long) + `}`)
	check("undeclared", convertToolResultBlock(block, "user", map[string]string{"u1": "Gone"}, tn, gjson.Result{}))
}
