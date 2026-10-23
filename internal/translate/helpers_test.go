package translate

import (
	"encoding/base64"
	"strings"
)

// tinyPNGB64 / tinyPDFB64 are minimal payloads that pass magic-byte checks.
const (
	tinyPNGB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB"
	tinyPDFB64 = "JVBERi0xLjQK"
)

// bigPNGB64 returns a valid-magic PNG payload padded to roughly n base64 chars.
func bigPNGB64(n int) string {
	raw, _ := base64.StdEncoding.DecodeString(tinyPNGB64)
	return base64.StdEncoding.EncodeToString(append(raw, []byte(strings.Repeat("\x00", n*3/4))...))
}
