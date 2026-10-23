// Phase 1 characterization test for the ported config package — proves
// LoadConfig's defaulting, auto-generated-secret, and tenant-lifecycle
// behavior matches v1's config.go exactly.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	return path
}

func TestLoadConfig_DefaultsMatchV1(t *testing.T) {
	path := writeTestConfig(t, `{}`)
	cm, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	sc := cm.GetServerConfig()

	cases := map[string]struct{ got, want int }{
		"Port":                            {sc.Port, 8898},
		"PanicTimeoutSeconds":             {sc.PanicTimeoutSeconds, 60},
		"TTFTTimeoutSeconds":              {sc.TTFTTimeoutSeconds, 180},
		"ReadTimeoutSeconds":              {sc.ReadTimeoutSeconds, 600},
		"TLSPort":                         {sc.TLSPort, 8900}, // Port + 2
		"SpecCaptureRetentionDays":        {sc.SpecCaptureRetentionDays, 14},
		"SpecCaptureRawChunksMaxSessions": {sc.SpecCaptureRawChunksMaxSessions, 100},
	}
	for name, c := range cases {
		if c.got != c.want {
			t.Errorf("%s default changed from v1: got %d, want %d", name, c.got, c.want)
		}
	}
	if sc.PublicHost != "localhost" {
		t.Errorf("PublicHost default changed from v1: got %q, want %q", sc.PublicHost, "localhost")
	}
	if sc.SpecCaptureDir != "request_captures" {
		t.Errorf("SpecCaptureDir default changed from v1: got %q, want %q", sc.SpecCaptureDir, "request_captures")
	}
	if sc.SpecCaptureRawChunksMaxBytes != 50*1024*1024 {
		t.Errorf("SpecCaptureRawChunksMaxBytes default changed from v1: got %d, want %d", sc.SpecCaptureRawChunksMaxBytes, 50*1024*1024)
	}
	if !sc.SpecCaptureIsEnabled() {
		t.Error("SpecCaptureIsEnabled must default to true (nil-means-true) as in v1")
	}
	if sc.SpecCaptureRawChunksIsEnabled() {
		t.Error("SpecCaptureRawChunksIsEnabled must default to FALSE (opposite polarity from the redacted capture) as in v1")
	}
}

func TestLoadConfig_GeneratesAndPersistsSecretsOnce(t *testing.T) {
	path := writeTestConfig(t, `{}`)
	cm, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	pass1, secret1 := cm.DashboardPassword(), cm.ProxySecret()
	if pass1 == "" || secret1 == "" {
		t.Fatal("dashboard password / proxy secret must be auto-generated when absent, as in v1")
	}

	// Reloading from the now-persisted file must NOT regenerate them —
	// otherwise every restart would invalidate every client's configured
	// Proxy-Authorization / dashboard login, a real v1 incident class.
	cm2, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("reload LoadConfig: %v", err)
	}
	if cm2.DashboardPassword() != pass1 || cm2.ProxySecret() != secret1 {
		t.Fatal("secrets must be stable across reloads once persisted, as in v1")
	}
}

func TestAtomicWriteFile_NeverLeavesOnlyATempFile(t *testing.T) {
	path := writeTestConfig(t, `{"server":{}}`)
	cm, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if err := cm.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("atomicWriteFile must rename the temp file into place, not leave it dangling")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	var roundTrip Config
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("saved config.json is not valid JSON: %v", err)
	}
}

func TestCreateAndDeleteAPIKey_RoundTrip(t *testing.T) {
	path := writeTestConfig(t, `{}`)
	cm, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	key, err := cm.CreateAPIKey("Test Tenant", "", "", "")
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	tenant, ok := cm.GetTenant(key)
	if !ok {
		t.Fatal("newly created tenant must be retrievable immediately")
	}
	if tenant.Strategy != "sticky" || tenant.DefaultModel != "gemini-2.5-flash" || !tenant.StripSearchTools {
		t.Fatalf("CreateAPIKey defaults changed from v1: %+v", tenant)
	}
	if tenant.GoogleAuthType != "oauth_pool" {
		t.Fatalf("empty stickyAccount must default GoogleAuthType to oauth_pool as in v1, got %q", tenant.GoogleAuthType)
	}

	if err := cm.DeleteAPIKey(key); err != nil {
		t.Fatalf("DeleteAPIKey: %v", err)
	}
	if _, ok := cm.GetTenant(key); ok {
		t.Fatal("deleted tenant must no longer be retrievable")
	}
}

func TestCreateAPIKey_StickyAccountSetsOauthSticky(t *testing.T) {
	path := writeTestConfig(t, `{}`)
	cm, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	key, err := cm.CreateAPIKey("Sticky Tenant", "someone@example.com", "", "")
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	tenant, _ := cm.GetTenant(key)
	if tenant.GoogleAuthType != "oauth_sticky" {
		t.Fatalf("non-empty stickyAccount must set GoogleAuthType to oauth_sticky as in v1, got %q", tenant.GoogleAuthType)
	}
}

func TestGetTenant_EmptyKeyRejected(t *testing.T) {
	path := writeTestConfig(t, `{}`)
	cm, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	for _, k := range []string{"", "   ", "\t\n"} {
		if _, ok := cm.GetTenant(k); ok {
			t.Fatalf("GetTenant(%q) must return false for empty/whitespace key", k)
		}
	}
}

func TestTenantConfig_GoogleAPIKeyNotSerialized(t *testing.T) {
	tc := TenantConfig{
		Name:         "test",
		GoogleAPIKey: "secret-gemini-key",
	}
	data, err := json.Marshal(tc)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	if strings.Contains(string(data), "secret-gemini-key") || strings.Contains(string(data), "google_api_key") {
		t.Fatalf("GoogleAPIKey leaked in TenantConfig JSON: %s", string(data))
	}

	// Verify UnmarshalJSON can read google_api_key from config file
	rawJSON := []byte(`{"name":"test","google_api_key":"secret-gemini-key"}`)
	var unmarshaled TenantConfig
	if err := json.Unmarshal(rawJSON, &unmarshaled); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if unmarshaled.GoogleAPIKey != "secret-gemini-key" {
		t.Fatalf("expected GoogleAPIKey to be preserved on unmarshal, got %q", unmarshaled.GoogleAPIKey)
	}
}
