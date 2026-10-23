// Part of the config package's port from superrelay (v1)'s config.go —
// see config.go in this directory for the split rationale. This file
// holds the secret-generation and crash-safe-write helpers.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"

	"claudgravity/internal/atomicfile"
)

func randomSecret() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(fmt.Sprintf("fallback-%d", os.Getpid())))
	}
	return hex.EncodeToString(b)
}

// atomicWriteFile is config.json's own crash-safe write — see
// internal/atomicfile's doc comment for why the implementation itself
// lives in its own leaf package (internal/accounts needs the identical
// behavior for OAuth credential files, with no reason to depend on
// internal/config just to reach it).
func atomicWriteFile(path string, data []byte) error {
	return atomicfile.WriteFile(path, data)
}

func (cm *ConfigManager) DashboardPassword() string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.cfg.Server.DashboardPassword
}

func (cm *ConfigManager) ProxySecret() string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.cfg.Server.ProxySecret
}
