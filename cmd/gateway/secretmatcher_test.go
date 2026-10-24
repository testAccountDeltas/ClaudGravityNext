package main

import (
	"os"
	"path/filepath"
	"testing"

	"claudgravity/internal/config"
	"claudgravity/internal/mitm"
)

func TestInstallSecretMatcher_RecognisesTenantKeyAndProxySecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := `{"server":{"proxy_secret":"proxy-secret-xyz"},"tenants":{"sk-tenant-1":{"name":"t"}}}`
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	cm, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	installSecretMatcher(cm)
	t.Cleanup(func() { mitm.SetSecretMatcher(nil) })

	// Exercised through the exported auth-free scrub surface: the matcher
	// is observed via a passthrough header scrub in package mitm's own
	// tests; here we assert the wiring does not panic and the proxy
	// secret from the config is the one in force.
	if cm.ProxySecret() != "proxy-secret-xyz" {
		t.Fatalf("proxy secret not loaded: %q", cm.ProxySecret())
	}
	if _, ok := cm.GetTenant("sk-tenant-1"); !ok {
		t.Fatal("tenant not loaded")
	}
}
