package anthropicerr

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Google reports an oversized prompt as an HTTP 400 such as
// "The input token count (1234567) exceeds the maximum number of tokens
// allowed (1048576)." Claude Code auto-compacts only on Anthropic's own
// wording (400 invalid_request_error, "prompt is too long: N tokens > M
// maximum"), so the upstream error is translated to exactly that.
var (
	googleCountsPattern = regexp.MustCompile(`(?i)token count\s*\(?(\d+)\)?[^0-9]{0,80}?(?:maximum|limit|allowed)[^0-9]{0,60}\(?(\d+)\)?`)
	googleNumberPattern = regexp.MustCompile(`\d{4,}`)
)

// IsContextExceeded reports whether an upstream failure is Google's
// "prompt exceeds the context window" 400. A genuine session-bucket wall
// on a tiny prompt is distinguished by the caller via the pre-flight
// estimate, not here.
func IsContextExceeded(status int, errText string) bool {
	if status != 400 {
		return false
	}
	l := strings.ToLower(errText)
	switch {
	case strings.Contains(l, "input token count"),
		strings.Contains(l, "exceeds the maximum number of token"),
		strings.Contains(l, "token count") && strings.Contains(l, "exceed"),
		strings.Contains(l, "too long") && strings.Contains(l, "token"):
		return true
	}
	return false
}

// PromptTooLongMessage builds Anthropic's exact overflow message. The
// actual token counts are taken from Google's error text when present;
// otherwise estTokens and limit are used (estTokens is bumped past limit
// when needed so the message always reads as an overflow).
func PromptTooLongMessage(errText string, estTokens, limit int64) string {
	actual, max := estTokens, limit
	if m := googleCountsPattern.FindStringSubmatch(errText); m != nil {
		a, e1 := strconv.ParseInt(m[1], 10, 64)
		b, e2 := strconv.ParseInt(m[2], 10, 64)
		if e1 == nil && e2 == nil && a > 0 && b > 0 {
			actual, max = a, b
		}
	} else if nums := googleNumberPattern.FindAllString(errText, -1); len(nums) >= 2 {
		a, e1 := strconv.ParseInt(nums[0], 10, 64)
		b, e2 := strconv.ParseInt(nums[1], 10, 64)
		if e1 == nil && e2 == nil && a > b {
			actual, max = a, b
		}
	}
	if max <= 0 {
		max = 1048576
	}
	if actual <= max {
		actual = max + 1
	}
	return fmt.Sprintf("prompt is too long: %d tokens > %d maximum", actual, max)
}
