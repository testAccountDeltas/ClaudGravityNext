// Phase 1 characterization test for the ported tlsfingerprint package —
// the TLS-fingerprint-profile freeze test from
// superrelay/mitm_tlsfreeze_test.go (Phase 0 of this migration), deferred
// from internal/mitm's own characterization test because it exercises
// THIS package, which hadn't been ported yet at that point.
package tlsfingerprint

import (
	"testing"

	utls "github.com/refraction-networking/utls"
)

// TestFreeze_TLSFingerprintProfile guards the CONFIGURATION CHOICE, not the
// wire bytes. HelloChrome_Auto's actual ClientHello is randomized per
// connection BY DESIGN (see generateRandomizedSpec in the utls library
// source — real Chrome itself randomizes extension order/GREASE per
// connection for anti-fingerprinting reasons), so hashing raw bytes would
// make this test flaky AND wrong. What must never change silently is which
// ClientHelloID this package asks uTLS to mimic.
func TestFreeze_TLSFingerprintProfile(t *testing.T) {
	rt := NewFingerprintedRoundTripper()
	if rt.helloID != utls.HelloChrome_Auto {
		t.Fatalf("tlsfingerprint's ClientHelloID changed: got %v, want utls.HelloChrome_Auto — "+
			"this must stay frozen until Part 3 p.5 of the v2 spec is explicitly revisited with real "+
			"empirical validation against Google's edge, not changed incidentally during a file-split refactor",
			rt.helloID)
	}
}
