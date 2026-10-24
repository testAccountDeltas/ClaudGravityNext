package translate

import "unicode/utf8"

// cutUTF8 returns s limited to at most max bytes without splitting a
// multibyte rune (a plain s[:max] can emit invalid UTF-8, which then
// breaks JSON encoding or shows as U+FFFD to the model).
func cutUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}
