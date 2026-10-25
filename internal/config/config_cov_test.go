package config

import (
	"os"
	"path/filepath"
	"testing"
)

func covLoad(t *testing.T) *ConfigManager {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cm, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return cm
}

func TestCov_ConnectPortRoundTrip(t *testing.T) {
	cm := covLoad(t)
	if cm.ConnectPort() != 0 {
		t.Fatal("unset port must be 0")
	}
	cm.SetConnectPort(8899)
	if cm.ConnectPort() != 8899 {
		t.Fatal("port not recorded")
	}
	// Runtime binding is never persisted to config.json.
	if err := cm.Save(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(cm.path)
	if string(data) == "" || contains(string(data), "8899") {
		t.Fatalf("connect port leaked into config.json: %s", data)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestCov_RandomSecretUniqueHex(t *testing.T) {
	a, b := randomSecret(), randomSecret()
	if len(a) != 48 || a == b {
		t.Fatalf("secret shape: %q %q", a, b)
	}
	for _, c := range a {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			t.Fatalf("non-hex %q", a)
		}
	}
}

func TestCov_GetAllTenantsReturnsCopy(t *testing.T) {
	cm := covLoad(t)
	k1, err := cm.CreateAPIKey("one", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	k2, _ := cm.CreateAPIKey("two", "", "", "")
	all := cm.GetAllTenants()
	if len(all) != 2 || all[k1].Name != "one" || all[k2].Name != "two" {
		t.Fatalf("tenants: %+v", all)
	}
	delete(all, k1) // mutating the copy must not affect the manager
	if _, ok := cm.GetTenant(k1); !ok {
		t.Fatal("GetAllTenants must return a copy")
	}
	if err := cm.DeleteAPIKey(k1); err != nil {
		t.Fatal(err)
	}
	if len(cm.GetAllTenants()) != 1 {
		t.Fatal("delete not reflected")
	}
}
