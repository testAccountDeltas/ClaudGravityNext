// Phase 1 characterization test for the ported mitm package — proves
// checkConnectTarget still blocks every address class a past audit
// flagged (private/loopback/link-local, CGNAT 100.64.0.0/10, the reserved
// 0.0.0.0/8 "this network" block, and the reserved 240.0.0.0/4 class E
// block), and isAnthropicHost's exact-or-subdomain matching rule.
package mitm

import "testing"

func TestCheckConnectTarget_BlocksKnownUnsafeAddressClasses(t *testing.T) {
	blocked := []string{
		"127.0.0.1",       // loopback
		"10.0.0.5",        // RFC 1918 private
		"169.254.1.1",     // link-local (cloud metadata range)
		"100.64.0.1",      // CGNAT 100.64.0.0/10
		"0.0.0.1",         // reserved "this network" 0.0.0.0/8
		"240.0.0.1",       // reserved class E 240.0.0.0/4
		"255.255.255.255", // limited broadcast, inside 240.0.0.0/4's range check
	}
	for _, addr := range blocked {
		if _, err := checkConnectTarget(addr); err == nil {
			t.Errorf("checkConnectTarget(%q) must be blocked, got no error", addr)
		}
	}
}

func TestCheckConnectTarget_AllowsOrdinaryPublicIP(t *testing.T) {
	// A well-known, stable public IP (Google DNS) — never expected to be
	// blocked by any of the private/reserved checks.
	ips, err := checkConnectTarget("8.8.8.8")
	if err != nil {
		t.Fatalf("an ordinary public IP must be allowed, got error: %v", err)
	}
	if len(ips) != 1 || ips[0].String() != "8.8.8.8" {
		t.Fatalf("expected the exact input IP to be returned, got %v", ips)
	}
}

func TestIsAnthropicHost_ExactAndSubdomainMatchOnly(t *testing.T) {
	matches := []string{"anthropic.com", "api.anthropic.com", "claude.ai", "console.anthropic.com", "ANTHROPIC.COM", "anthropic.com."}
	for _, h := range matches {
		if !isAnthropicHost(h) {
			t.Errorf("isAnthropicHost(%q) must match (exact or subdomain, case-insensitive, trailing-dot tolerant)", h)
		}
	}
	nonMatches := []string{
		"anthropic.com.attacker.net", // substring, not a real subdomain
		"notclaude.ai.example.org",   // substring, not a real subdomain
		"evilanthropic.com",          // substring without a dot boundary
		"example.com",
	}
	for _, h := range nonMatches {
		if isAnthropicHost(h) {
			t.Errorf("isAnthropicHost(%q) must NOT match — it only contains the domain as a substring, not as a real (sub)domain", h)
		}
	}
}

func TestIsAnthropicHost_StripsPortBeforeMatching(t *testing.T) {
	if !isAnthropicHost("claude.ai:443") {
		t.Fatal("isAnthropicHost must strip a port before matching, as PassthroughToReal calls it with r.Host (port still attached)")
	}
}
