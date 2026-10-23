// Rule 8 — ConnectAllowlistSelfConsistency. NEW rule (never implemented
// in v1 or anywhere in v2 before this step) built from Part 7.6 #8's own
// description: "конфигурационный инвариант (не по трафику, а по
// config.json + коду): набор портов, разрешённых для CONNECT-туннеля в
// internal/mitm, обязан включать сконфигурированный tls_port самого
// шлюза. Это буквально сегодняшний живой инцидент (89de11b) — правило
// делает его невозможным в будущем на уровне CI, а не только на уровне
// памяти агента."
//
// Unlike every other rule in this package, this one is NOT traffic-based
// — it ignores the records argument entirely and instead checks a
// config+code invariant directly: that internal/mitm.AllowedConnectPorts
// (the single, named source of truth HandleConnectTunnel itself now
// calls — see that function's own doc comment in internal/mitm/tunnel.go)
// actually contains whatever tls_port a real config.json resolves to.
// ConfigPath defaults to "config.json" (the same default relative path
// convention as -dir's default "request_captures") and is overridable by
// cmd/speccheck's -config flag, so this rule can be pointed at the exact
// config a given gateway instance is running with the same the test
// wiring in internal/mitm's own characterization tests probes
// (configuredTLSPort / AllowedConnectPorts) does, just end-to-end against
// a real file instead of a hardcoded literal.
package rules

import (
	"fmt"

	"claudgravity/internal/config"
	"claudgravity/internal/mitm"
)

// ConfigPath is the config.json this rule reads tls_port from. Set by
// cmd/speccheck's -config flag before Check is called; defaults to the
// gateway's own default relative path.
var ConfigPath = "config.json"

func checkConnectAllowlistSelfConsistency() []Violation {
	cm, err := config.LoadConfig(ConfigPath)
	if err != nil {
		return []Violation{{
			RID:    "config",
			Detail: fmt.Sprintf("could not load %s to check CONNECT-allowlist self-consistency: %v", ConfigPath, err),
		}}
	}
	serverCfg := cm.GetServerConfig()
	allowed := mitm.AllowedConnectPorts(serverCfg.TLSPort)

	wantPort := fmt.Sprint(serverCfg.TLSPort)
	for _, p := range allowed {
		if p == wantPort {
			return nil
		}
	}
	// This can only happen if a future edit to AllowedConnectPorts
	// itself stops including localTLSPort — the exact regression class
	// commit 89de11b fixed, now caught here instead of in production.
	return []Violation{{
		RID:    "config",
		Detail: fmt.Sprintf("internal/mitm.AllowedConnectPorts(%d) = %v does not include the gateway's own configured tls_port %d (from %s) — this is the exact self-consistency regression fixed in superrelay commit 89de11b", serverCfg.TLSPort, allowed, serverCfg.TLSPort, ConfigPath),
	}}
}

var ConnectAllowlistSelfConsistency = Rule{
	Name: "ConnectAllowlistSelfConsistency",
	Check: func(records []CaptureRecord) []Violation {
		return checkConnectAllowlistSelfConsistency()
	},
}
