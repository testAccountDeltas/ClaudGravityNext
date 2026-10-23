// Package config is a port of superrelay (v1)'s config.go, split across
// four files by responsibility per
// ARCHITECTURE_V2_SPECIFICATION_AND_MIGRATION_PLAN.md Part 4.3:
// config.go (Config/ConfigManager/LoadConfig/Save — this file),
// server.go (ServerConfig), tenant.go (TenantConfig + API-key lifecycle),
// secrets.go (secret generation + crash-safe write + the two secret
// getters). Splitting a 280-line v1 file into four ~60-110-line files is
// the file-size discipline from Part 4.1, not a sign anything here was
// restructured — every function's BODY is unchanged from v1.
//
// One real behavior change from v1, not a refactor-for-its-own-sake: v1's
// LoadConfig directly mutated three package-level vars
// (AntigravityClientID/ClientSecret/UserAgent) that live in
// auth_manager.go — this only worked because v1 is one single `package
// main` where every file can reach every other file's globals. That
// cross-file global-state coupling is exactly the Part 1.5 diagnosis
// (ad-hoc shared state instead of an explicit interface), and it can't
// even compile here: internal/config is a leaf package with no
// dependency on internal/accounts (which doesn't exist yet — Phase 2).
// The three ServerConfig fields (GoogleOAuthClientID, GoogleOAuthClientSecret,
// AntigravityUserAgent) are preserved unchanged below; applying them to
// internal/accounts' OAuth client config becomes that package's own
// init-time responsibility (reading cfgMgr.GetServerConfig()), not
// something config.go reaches sideways to do.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Config is the full config.json document.
type Config struct {
	Server  ServerConfig            `json:"server"`
	Tenants map[string]TenantConfig `json:"tenants"`
}

type ConfigManager struct {
	mu   sync.RWMutex
	cfg  *Config
	path string

	// connectPort is the actual, resolved CONNECT-tunnel port this
	// process is listening on (cmd/gateway/main.go's own
	// -connect-port flag, defaulting to Server.Port+1 when unset) — set
	// once at startup via SetConnectPort, after ConfigManager itself is
	// constructed, since the resolved value depends on a flag main.go
	// parses after loading config. Not persisted to config.json: this is
	// this process's own runtime binding, not configuration.
	connectPort int
}

// SetConnectPort records the CONNECT-tunnel port main.go actually bound,
// for ConnectPort() below to report back to callers like
// dashboard/bootstrap_api.go's /api/bootstrap (GAP-O2: that endpoint used
// to report Server.Port — the plain API port — instead of the real
// CONNECT-proxy port a client must point its HTTPS_PROXY at).
func (cm *ConfigManager) SetConnectPort(port int) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.connectPort = port
}

// ConnectPort returns the port SetConnectPort last recorded, or 0 if
// nothing has set it yet (the CONNECT-tunnel listener failed to start, or
// SetConnectPort hasn't run yet).
func (cm *ConfigManager) ConnectPort() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.connectPort
}

func LoadConfig(path string) (*ConfigManager, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config json: %w", err)
	}

	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8898
	}
	if cfg.Server.PanicTimeoutSeconds == 0 {
		// Live production logs show real, successful TTFTs regularly
		// landing in the 35-52s range for Opus-class ModeAuto (unlimited
		// thinking budget) requests — 30s fired this "panic" early-headers
		// fallback 8 times in a single ~20-minute window, each one
		// permanently losing the ability to fail over to another account
		// if Google had answered with a 429 instead of a real response a
		// moment later (see executeStreamingWithWatchdog's earlyHeadersSent gate).
		cfg.Server.PanicTimeoutSeconds = 60
	}
	if cfg.Server.TTFTTimeoutSeconds == 0 {
		cfg.Server.TTFTTimeoutSeconds = 180
	}
	if cfg.Server.ReadTimeoutSeconds == 0 {
		cfg.Server.ReadTimeoutSeconds = 600
	}
	if cfg.Server.TLSPort == 0 {
		cfg.Server.TLSPort = cfg.Server.Port + 2
	}
	if cfg.Server.PublicHost == "" {
		cfg.Server.PublicHost = "localhost"
	}
	if cfg.Tenants == nil {
		cfg.Tenants = make(map[string]TenantConfig)
	}
	if cfg.Server.SpecCaptureDir == "" {
		cfg.Server.SpecCaptureDir = "request_captures"
	}
	if cfg.Server.SpecCaptureRetentionDays == 0 {
		cfg.Server.SpecCaptureRetentionDays = 14
	}
	if cfg.Server.SpecCaptureRawChunksMaxSessions == 0 {
		cfg.Server.SpecCaptureRawChunksMaxSessions = 100
	}
	if cfg.Server.SpecCaptureRawChunksMaxBytes == 0 {
		cfg.Server.SpecCaptureRawChunksMaxBytes = 50 * 1024 * 1024
	}

	// NOTE: v1 applied GoogleOAuthClientID/ClientSecret/AntigravityUserAgent
	// to auth_manager.go's package-level vars right here. See this file's
	// doc comment — that wiring now belongs to internal/accounts' own
	// init, reading these same ServerConfig fields, once that package
	// exists (Phase 2).

	cm := &ConfigManager{
		cfg:  &cfg,
		path: path,
	}

	needsSave := false
	if cfg.Server.DashboardPassword == "" {
		cfg.Server.DashboardPassword = randomSecret()
		needsSave = true
	}
	if cfg.Server.ProxySecret == "" {
		cfg.Server.ProxySecret = randomSecret()
		needsSave = true
	}
	if needsSave {
		if err := cm.Save(); err != nil {
			return nil, fmt.Errorf("persist generated secrets: %w", err)
		}
	}

	return cm, nil
}

func (cm *ConfigManager) Save() error {
	cm.mu.RLock()
	data, err := json.MarshalIndent(cm.cfg, "", "  ")
	cm.mu.RUnlock()
	if err != nil {
		return err
	}
	return atomicWriteFile(cm.path, data)
}

func (cm *ConfigManager) GetServerConfig() ServerConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.cfg.Server
}
