// Package sessionkey holds GenerateStableSessionID, a small pure utility
// originally embedded in v1's warmup.go but genuinely shared by TWO
// unrelated concerns: warmup.go's own Antigravity session stickiness, and
// request_logger.go's DeriveSessionID fallback (when a request carries no
// explicit session header/metadata). Those two destination packages
// (internal/warmup, internal/requestlog) have no other reason to depend on
// each other, so per the single-ownership rule in
// ARCHITECTURE_V2_SPECIFICATION_AND_MIGRATION_PLAN.md Part 4.2, this
// function gets its own small leaf package that both import, rather than
// living in one of them and creating an artificial cross-dependency (or
// being duplicated, which is the exact Divergent Duplication failure mode
// Part 1.2 diagnoses). This is a deliberate, documented departure from the
// Part 4.3 file tree's original list — discovered while actually porting
// the code, not speculated in advance.
package sessionkey

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"
	"strconv"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

var (
	randMu  sync.Mutex
	randGen = rand.New(rand.NewSource(time.Now().UnixNano()))
)

// GenerateStableSessionID derives a stable session identifier from a
// Gemini request payload's first user turn, or returns a random one if
// no stable text is available.
func GenerateStableSessionID(payload []byte, generation int, fallbackKey string) string {
	// fallbackKey (DeriveSessionID's result — the real x-claude-code-session-id/
	// x-session-id header when the client sends one) is stable for the
	// conversation's whole lifetime and takes priority over hashing the first
	// user turn's text. The text used to be hashed first instead: once
	// TruncateConversationToFit replaces dropped old messages with its own
	// synthetic "[Gateway notice: ...]" message, THAT became the new "first
	// user turn" on the next call and changed the hash, silently handing
	// Antigravity a brand new sessionId mid-conversation and resetting its
	// server-side prefix cache — the same failure class DeriveConversationAnchor
	// in selector.go had for account affinity, just one layer deeper.
	hashKey := fallbackKey
	if hashKey == "" {
		contents := gjson.GetBytes(payload, "request.contents")
		if !contents.Exists() {
			contents = gjson.GetBytes(payload, "contents")
		}
		if contents.IsArray() {
			for _, content := range contents.Array() {
				if content.Get("role").String() == "user" {
					if text := content.Get("parts.0.text").String(); text != "" {
						hashKey = text
						break
					}
				}
			}
		}
	}
	if hashKey != "" {
		h := sha256.New()
		h.Write([]byte(hashKey))
		if generation > 0 {
			// Deterministic-but-distinct per generation: same
			// conversation keeps hitting the SAME bumped bucket
			// on retry (idempotent), without colliding with gen 0.
			fmt.Fprintf(h, ":gen%d", generation)
		}
		sum := h.Sum(nil)
		n := int64(binary.BigEndian.Uint64(sum[:8])) & 0x7FFFFFFFFFFFFFFF
		return "-" + strconv.FormatInt(n, 10)
	}
	randMu.Lock()
	n := randGen.Int63n(9_000_000_000_000_000_000)
	randMu.Unlock()
	return "-" + strconv.FormatInt(n, 10)
}
